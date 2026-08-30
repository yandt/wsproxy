package tunnel

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
)

func TestSOCKS5Target(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- ""
			return
		}
		defer c.Close()
		target, err := SOCKS5Target(c, func(user, pass string) bool { return pass == "secret" })
		if err != nil {
			done <- "err:" + err.Error()
			return
		}
		_ = SOCKS5OK(c)
		done <- target
	}()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte{5, 1, 2})
	rep := make([]byte, 2)
	if _, err := io.ReadFull(c, rep); err != nil {
		t.Fatal(err)
	}
	if rep[1] != 2 {
		t.Fatalf("method %d", rep[1])
	}
	auth := []byte{1, 1, 'u', 6}
	auth = append(auth, []byte("secret")...)
	_, _ = c.Write(auth)
	arep := make([]byte, 2)
	if _, err := io.ReadFull(c, arep); err != nil {
		t.Fatal(err)
	}
	req := []byte{5, 1, 0, 3, byte(len("example.com"))}
	req = append(req, []byte("example.com")...)
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], 443)
	req = append(req, port[:]...)
	_, _ = c.Write(req)

	got := <-done
	if got != "example.com:443" {
		t.Fatalf("got %q", got)
	}
}

func TestSOCKS5RejectsNoAuth(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	errCh := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer c.Close()
		_, err = SOCKS5Target(c, func(string, string) bool { return true })
		errCh <- err
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte{5, 1, 0})
	if err := <-errCh; err == nil {
		t.Fatal("expected reject")
	}
}
