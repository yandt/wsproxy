package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"wsproxy/internal/check"
	"wsproxy/internal/config"
	"wsproxy/internal/proto"
	"wsproxy/internal/server"
	"wsproxy/internal/tunnel"
)

func TestStatusListsTunnels(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer hs.Close()
	u, _ := url.Parse(hs.URL)

	srv, httpPort, _, access, agentTok := startServer(t)
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	exp, err := tunnel.ParseExpose("tcp://" + listen + "=" + u.Host)
	if err != nil {
		t.Fatal(err)
	}
	startClientExpose(httpPort, agentTok, "probe1", []proto.Expose{exp})
	waitClient(t, srv.Hub, "probe1")
	waitTCP(t, listen)

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/status", httpPort), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Access-Token", access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st server.StatusReport
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if len(st.Clients) != 1 || st.Clients[0].ID != "probe1" || len(st.Clients[0].Tunnels) != 1 {
		t.Fatalf("%#v", st)
	}
	tun := st.Clients[0].Tunnels[0]
	if !tun.ListenOK || !tun.PeerOK {
		t.Fatalf("tunnel %#v", tun)
	}

	rep := check.Server(config.Server{
		HTTP:        fmt.Sprintf("127.0.0.1:%d", httpPort),
		SSH:         srv.SSHAddr,
		AccessToken: access,
		AgentToken:  agentTok,
	})
	if !rep.OK {
		t.Fatalf("server test %#v", rep)
	}

	crep := check.Client(config.Client{
		Server:     fmt.Sprintf("ws://127.0.0.1:%d", httpPort),
		AgentToken: agentTok,
		ID:         "probe1",
		Expose:     []config.ExposeItem{{Kind: exp.Kind, Listen: exp.Listen, Target: exp.Target}},
	})
	if !crep.OK {
		t.Fatalf("client test %#v", crep)
	}
}

func TestStatusUnauthorized(t *testing.T) {
	_, httpPort, _, _, _ := startServer(t)
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/status", httpPort))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
