package client

import (
	"testing"

	"wsproxy/internal/proto"
)

func TestParseExpose(t *testing.T) {
	got, err := ParseExpose("9000=127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != "127.0.0.1:9000" || got.Target != "127.0.0.1:80" || got.Kind != proto.KindTCP {
		t.Fatalf("%#v", got)
	}
	if _, err := ParseExpose("socks://1080"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseExpose("bad"); err == nil {
		t.Fatal("expected error")
	}
}
