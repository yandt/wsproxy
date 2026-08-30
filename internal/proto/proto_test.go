package proto

import (
	"bytes"
	"testing"
)

func TestEncodeDecodeData(t *testing.T) {
	raw := []byte("hello 命令行")
	msg := EncodeData("s1", raw)
	out, err := Decode(msg.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	got, err := out.Payload()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("got %q want %q", got, raw)
	}
}

func TestDecodeHello(t *testing.T) {
	in := Msg{T: TypeHello, Name: "box1", Exposes: []Expose{{Listen: "9000", Target: "127.0.0.1:80"}}}
	out, err := Decode(in.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if out.T != TypeHello || out.Name != "box1" || len(out.Exposes) != 1 || out.Exposes[0].Target != "127.0.0.1:80" {
		t.Fatalf("unexpected %#v", out)
	}
}

func TestSanitizeName(t *testing.T) {
	if !ValidName("office-pc") || !ValidName("vm_01.local") {
		t.Fatal("valid names rejected")
	}
	if ValidName("bad/name") || ValidName("") || ValidName("has space") {
		t.Fatal("invalid names accepted")
	}
	if got := SanitizeName("My Laptop"); got != "My-Laptop" {
		t.Fatalf("got %q", got)
	}
	if SanitizeName("???") != "client" {
		t.Fatal("expected fallback")
	}
}
