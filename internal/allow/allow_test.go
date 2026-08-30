package allow

import (
	"net"
	"testing"
)

func TestEmptyAllowsAll(t *testing.T) {
	l, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !l.AllowsIP(net.ParseIP("8.8.8.8")) || !l.AllowsHostPort("evil.com:443") || !l.AllowsName("any") {
		t.Fatal("empty list should allow all")
	}
}

func TestIPAndCIDR(t *testing.T) {
	l, err := Parse([]string{"127.0.0.1", "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	if !l.AllowsIP(net.ParseIP("127.0.0.1")) || !l.AllowsIP(net.ParseIP("10.1.2.3")) {
		t.Fatal("expected allow")
	}
	if l.AllowsIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("expected deny")
	}
	if !l.AllowsRemote("127.0.0.1:9") {
		t.Fatal("remote")
	}
}

func TestTargets(t *testing.T) {
	l, err := Parse([]string{
		"127.0.0.1:80",
		"10.0.0.0/8",
		"example.com:443",
		"*.internal.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !l.AllowsHostPort("127.0.0.1:80") {
		t.Fatal("ip:port")
	}
	if l.AllowsHostPort("127.0.0.1:22") {
		t.Fatal("wrong port")
	}
	if !l.AllowsHostPort("10.1.2.3:9999") {
		t.Fatal("cidr any port")
	}
	if !l.AllowsHostPort("example.com:443") || l.AllowsHostPort("example.com:80") {
		t.Fatal("hostname port")
	}
	if !l.AllowsHostPort("a.internal.test:1") || l.AllowsHostPort("internal.test.evil:1") {
		t.Fatal("wildcard")
	}
}

func TestNames(t *testing.T) {
	l, err := Parse([]string{"office", "home"})
	if err != nil {
		t.Fatal(err)
	}
	if !l.AllowsName("office") || l.AllowsName("other") {
		t.Fatal("name")
	}
}

func TestStar(t *testing.T) {
	l, err := Parse([]string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if !l.AllowsIP(net.ParseIP("1.1.1.1")) || !l.AllowsHostPort("x:1") {
		t.Fatal("star")
	}
}

func TestBadCIDR(t *testing.T) {
	if _, err := Parse([]string{"10.0.0.0/99"}); err == nil {
		t.Fatal("expected error")
	}
}
