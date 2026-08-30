package e2e

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"wsproxy/internal/allow"
	"wsproxy/internal/client"
	"wsproxy/internal/proto"
	"wsproxy/internal/tunnel"
)

func socks5ReplyCode(t *testing.T, proxy, dest, user, pass string) byte {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxy, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(c, rep); err != nil {
		t.Fatal(err)
	}
	if rep[1] != 2 {
		t.Fatalf("socks method %d", rep[1])
	}
	auth := []byte{1, byte(len(user))}
	auth = append(auth, user...)
	auth = append(auth, byte(len(pass)))
	auth = append(auth, pass...)
	if _, err := c.Write(auth); err != nil {
		t.Fatal(err)
	}
	arep := make([]byte, 2)
	if _, err := io.ReadFull(c, arep); err != nil {
		t.Fatal(err)
	}
	if arep[1] != 0 {
		t.Fatalf("socks auth %d", arep[1])
	}
	host, portStr, err := net.SplitHostPort(dest)
	if err != nil {
		t.Fatal(err)
	}
	var port uint16
	fmt.Sscanf(portStr, "%d", &port)
	req := []byte{5, 1, 0, 3, byte(len(host))}
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 10)
	if _, err := io.ReadFull(c, head); err != nil {
		t.Fatal(err)
	}
	return head[1]
}

func TestAllowClientName(t *testing.T) {
	srv, httpPort, _, _, agentTok := startServer(t)
	sets, err := allow.ParseSets(nil, []string{"box1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetAllow(sets)

	startClient(httpPort, agentTok, "box2")
	time.Sleep(400 * time.Millisecond)
	if srv.Hub.Has("box2") {
		t.Fatal("box2 should be rejected")
	}

	startClient(httpPort, agentTok, "box1")
	waitClient(t, srv.Hub, "box1")
}

func TestAllowSourceIP(t *testing.T) {
	srv, httpPort, _, _, _ := startServer(t)
	sets, err := allow.ParseSets([]string{"10.0.0.1"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetAllow(sets)

	health, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", httpPort))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, health.Body)
	health.Body.Close()
	if health.StatusCode != 200 {
		t.Fatalf("health %d", health.StatusCode)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", httpPort))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
}

func TestAllowTargetServer(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "allow-ok")
	}))
	defer ok.Close()
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "should-not")
	}))
	defer deny.Close()
	okHost := mustHost(t, ok.URL)
	denyHost := mustHost(t, deny.URL)

	srv, httpPort, _, access, agentTok := startServer(t)
	sets, err := allow.ParseSets(nil, nil, []string{okHost})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetAllow(sets)

	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	exp, err := tunnel.ParseExpose("socks://" + listen)
	if err != nil {
		t.Fatal(err)
	}
	startClientExpose(httpPort, agentTok, "allow-srv", []proto.Expose{exp})
	waitClient(t, srv.Hub, "allow-srv")
	waitTCP(t, listen)

	c := socks5Dial(t, listen, okHost, "u", access)
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", okHost)
	body, err := io.ReadAll(c)
	c.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "allow-ok") {
		t.Fatalf("got %q", body)
	}
	if code := socks5ReplyCode(t, listen, denyHost, "u", access); code == 0 {
		t.Fatal("denied target should fail")
	}
}

func TestAllowTargetClient(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "client-ok")
	}))
	defer ok.Close()
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "should-not")
	}))
	defer deny.Close()
	okHost := mustHost(t, ok.URL)
	denyHost := mustHost(t, deny.URL)

	list, err := allow.Parse([]string{okHost})
	if err != nil {
		t.Fatal(err)
	}

	srv, httpPort, _, access, agentTok := startServer(t)
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	exp, err := tunnel.ParseExpose("socks://" + listen)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = client.ConnectOnce(client.Config{
			ID:           "allow-cli",
			Server:       fmt.Sprintf("ws://127.0.0.1:%d", httpPort),
			AgentToken:   agentTok,
			Shell:        "/bin/bash",
			Exposes:      []proto.Expose{exp},
			AllowTargets: list,
		})
	}()
	waitClient(t, srv.Hub, "allow-cli")
	waitTCP(t, listen)

	c := socks5Dial(t, listen, okHost, "u", access)
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", okHost)
	body, err := io.ReadAll(c)
	c.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "client-ok") {
		t.Fatalf("got %q", body)
	}
	if code := socks5ReplyCode(t, listen, denyHost, "u", access); code == 0 {
		t.Fatal("client should deny target")
	}
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}
