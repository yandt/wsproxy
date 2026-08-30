package e2e

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"wsproxy/internal/client"
	"wsproxy/internal/proto"
	"wsproxy/internal/tunnel"
)

func startClientExpose(httpPort int, agentTok, id string, exposes []proto.Expose) {
	go func() {
		_ = client.ConnectOnce(client.Config{
			ID:         id,
			Server:     fmt.Sprintf("ws://127.0.0.1:%d", httpPort),
			AgentToken: agentTok,
			Shell:      "/bin/bash",
			Exposes:    exposes,
		})
	}()
}

func waitTCP(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("not listening %s", addr)
}

func socks5Dial(t *testing.T, proxy, dest, user, pass string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxy, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(c, rep); err != nil {
		t.Fatal(err)
	}
	if rep[1] != 2 {
		c.Close()
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
		c.Close()
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
	var pb [2]byte
	binary.BigEndian.PutUint16(pb[:], port)
	req = append(req, pb[:]...)
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 10)
	if _, err := io.ReadFull(c, head); err != nil {
		t.Fatal(err)
	}
	if head[1] != 0 {
		c.Close()
		t.Fatalf("socks reply %d", head[1])
	}
	return c
}

func TestTCPExpose(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "tcp-ok")
	}))
	defer hs.Close()
	u, _ := url.Parse(hs.URL)

	srv, httpPort, _, _, agentTok := startServer(t)
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	exp, err := tunnel.ParseExpose("tcp://" + listen + "=" + u.Host)
	if err != nil {
		t.Fatal(err)
	}
	startClientExpose(httpPort, agentTok, "tun1", []proto.Expose{exp})
	waitClient(t, srv.Hub, "tun1")
	waitTCP(t, listen)

	resp, err := http.Get("http://" + listen)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "tcp-ok" {
		t.Fatalf("got %q", body)
	}
}

func TestSOCKSExpose(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "socks-ok")
	}))
	defer hs.Close()
	u, _ := url.Parse(hs.URL)

	srv, httpPort, _, access, agentTok := startServer(t)
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	exp, err := tunnel.ParseExpose("socks://" + listen)
	if err != nil {
		t.Fatal(err)
	}
	startClientExpose(httpPort, agentTok, "tun2", []proto.Expose{exp})
	waitClient(t, srv.Hub, "tun2")
	waitTCP(t, listen)

	c := socks5Dial(t, listen, u.Host, "u", access)
	defer c.Close()
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", u.Host)
	body, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "socks-ok") {
		t.Fatalf("got %q", body)
	}
}

func TestHTTPProxyExpose(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "http-ok")
	}))
	defer hs.Close()

	srv, httpPort, _, access, agentTok := startServer(t)
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	exp, err := tunnel.ParseExpose("http://" + listen)
	if err != nil {
		t.Fatal(err)
	}
	startClientExpose(httpPort, agentTok, "tun3", []proto.Expose{exp})
	waitClient(t, srv.Hub, "tun3")
	waitTCP(t, listen)

	tr := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: listen, User: url.UserPassword("u", access)})}
	cli := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := cli.Get(hs.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "http-ok" {
		t.Fatalf("got %q", body)
	}
}

func TestUDPExpose(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(append([]byte("pong-"), buf[:n]...), addr)
		}
	}()

	srv, httpPort, _, _, agentTok := startServer(t)
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	exp, err := tunnel.ParseExpose("udp://" + listen + "=" + pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	startClientExpose(httpPort, agentTok, "tun4", []proto.Expose{exp})
	waitClient(t, srv.Hub, "tun4")

	c, err := net.Dial("udp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	deadline := time.Now().Add(3 * time.Second)
	var buf [64]byte
	var n int
	for time.Now().Before(deadline) {
		_ = c.SetDeadline(time.Now().Add(300 * time.Millisecond))
		_, _ = c.Write([]byte("hi"))
		n, err = c.Read(buf[:])
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "pong-hi" {
		t.Fatalf("got %q", buf[:n])
	}
}
