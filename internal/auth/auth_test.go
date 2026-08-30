package auth

import "testing"

func TestEqual(t *testing.T) {
	if !Equal("token-a", "token-a") {
		t.Fatal("same token should match")
	}
	if Equal("token-a", "token-b") {
		t.Fatal("different token should not match")
	}
	if Equal("short", "much-longer-token") {
		t.Fatal("different length should not match")
	}
}
