package check

import "testing"

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
