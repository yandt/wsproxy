package tunnel

import (
	"testing"

	"wsproxy/internal/proto"
)

func TestParseExpose(t *testing.T) {
	got, err := ParseExpose("9000=127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != proto.KindTCP || got.Listen != "127.0.0.1:9000" || got.Target != "127.0.0.1:80" {
		t.Fatalf("%#v", got)
	}

	got, err = ParseExpose("udp://5353=1.1.1.1:53")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != proto.KindUDP || got.Listen != "127.0.0.1:5353" {
		t.Fatalf("%#v", got)
	}

	got, err = ParseExpose("socks://1080")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != proto.KindSOCKS || got.Listen != "127.0.0.1:1080" || got.Target != "" {
		t.Fatalf("%#v", got)
	}

	got, err = ParseExpose("tcp://0.0.0.0:9000=127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != "0.0.0.0:9000" {
		t.Fatalf("explicit public bind %#v", got)
	}

	got, err = ParseExpose("http://127.0.0.1:3128")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != proto.KindHTTP || got.Listen != "127.0.0.1:3128" {
		t.Fatalf("%#v", got)
	}

	if _, err := ParseExpose("socks://1080=1.2.3.4:80"); err == nil {
		t.Fatal("socks should not take target")
	}
	if _, err := ParseExpose("bad"); err == nil {
		t.Fatal("expected error")
	}
}
