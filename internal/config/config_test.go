package config

import (
	"os"
	"path/filepath"
	"testing"

	"wsproxy/internal/proto"
)

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFlatServer(t *testing.T) {
	p := write(t, "s.yaml", `
http: ":9090"
ssh: ":2200"
agent_token: tun
access_token: visit
host_key: /tmp/key
`)
	s, err := LoadServer(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.HTTP != ":9090" || s.SSH != ":2200" || s.AgentToken != "tun" || s.AccessToken != "visit" || s.HostKey != "/tmp/key" {
		t.Fatalf("%#v", s)
	}
}

func TestLoadNestedAndClientExpose(t *testing.T) {
	p := write(t, "all.yaml", `
server:
  http: ":8080"
  agent_token: tun
  access_token: visit
client:
  server: ws://127.0.0.1:8080
  agent_token: tun
  id: office
  expose:
    - 9000=127.0.0.1:80
    - kind: socks
      listen: 127.0.0.1:1080
    - kind: udp
      listen: ":5353"
      target: 127.0.0.1:53
`)
	s, err := LoadServer(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.HTTP != ":8080" || s.AgentToken != "tun" {
		t.Fatalf("server %#v", s)
	}
	c, err := LoadClient(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "ws://127.0.0.1:8080" || c.ID != "office" || len(c.Expose) != 3 {
		t.Fatalf("client %#v", c)
	}
	ex := c.Exposes()
	if ex[0].Kind != proto.KindTCP || ex[0].Target != "127.0.0.1:80" {
		t.Fatalf("tcp %#v", ex[0])
	}
	if ex[1].Kind != proto.KindSOCKS || ex[1].Listen != "127.0.0.1:1080" {
		t.Fatalf("socks %#v", ex[1])
	}
	if ex[2].Kind != proto.KindUDP || ex[2].Target != "127.0.0.1:53" {
		t.Fatalf("udp %#v", ex[2])
	}
}

func TestLoadFlatClient(t *testing.T) {
	p := write(t, "c.yaml", `
server: ws://example:8080
agent_token: tun
id: home
expose:
  - socks://127.0.0.1:1080
`)
	c, err := LoadClient(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "ws://example:8080" || c.ID != "home" || len(c.Expose) != 1 || c.Expose[0].Kind != proto.KindSOCKS {
		t.Fatalf("%#v", c)
	}
}

func TestMergeFlagsOverrideFile(t *testing.T) {
	s := MergeServer(Server{HTTP: ":1", AgentToken: "a", AccessToken: "b"}, map[string]string{
		"http":         ":2",
		"access-token": "c",
	})
	if s.HTTP != ":2" || s.AgentToken != "a" || s.AccessToken != "c" || s.SSH != ":2222" {
		t.Fatalf("%#v", s)
	}

	c := MergeClient(Client{Server: "ws://old", ID: "a", AllowTargets: []string{"10.0.0.0/8"}}, map[string]string{"id": "b"}, []proto.Expose{
		{Kind: proto.KindTCP, Listen: "0.0.0.0:9", Target: "127.0.0.1:80"},
	}, []string{"127.0.0.1:80"})
	if c.Server != "ws://old" || c.ID != "b" || len(c.Expose) != 1 {
		t.Fatalf("%#v", c)
	}
	if len(c.AllowTargets) != 2 || c.AllowTargets[1] != "127.0.0.1:80" {
		t.Fatalf("allow %#v", c.AllowTargets)
	}
}

func TestLoadAllowLists(t *testing.T) {
	p := write(t, "allow.yaml", `
server:
  http: ":8080"
  agent_token: tun
  access_token: visit
  allow_ips:
    - 127.0.0.1
    - 10.0.0.0/8
  allow_clients: [office]
  allow_targets:
    - 127.0.0.1:80
client:
  server: ws://127.0.0.1:8080
  agent_token: tun
  allow_targets:
    - 10.0.0.0/8
`)
	s, err := LoadServer(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.AllowIPs) != 2 || s.AllowClients[0] != "office" || s.AllowTargets[0] != "127.0.0.1:80" {
		t.Fatalf("server %#v", s)
	}
	c, err := LoadClient(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.AllowTargets) != 1 || c.AllowTargets[0] != "10.0.0.0/8" {
		t.Fatalf("client %#v", c)
	}
}
