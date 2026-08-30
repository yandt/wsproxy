package check

import (
	"strings"
	"testing"

	"wsproxy/internal/config"
)

func TestClientEmptyServerPrints(t *testing.T) {
	var buf strings.Builder
	rep := ClientTo(&buf, config.Client{})
	out := buf.String()
	if !strings.Contains(out, "连主服务器") || !strings.Contains(out, "不通") {
		t.Fatalf("want visible server check, got %q", out)
	}
	if rep.OK {
		t.Fatal("empty server should fail")
	}
}

func TestLocalAndPublicHTTP(t *testing.T) {
	if got := localHTTP(":8080"); got != "http://127.0.0.1:8080" {
		t.Fatal(got)
	}
	if got := localHTTP("0.0.0.0:8080"); got != "http://127.0.0.1:8080" {
		t.Fatal(got)
	}
	if got := publicHTTP("ws://10.0.0.1:8080"); got != "http://10.0.0.1:8080" {
		t.Fatal(got)
	}
	if got := publicHTTP("wss://ex.com"); got != "https://ex.com" {
		t.Fatal(got)
	}
}
