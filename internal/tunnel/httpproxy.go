package tunnel

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

type HTTPRequest struct {
	Target   string
	Connect  bool
	Buffered io.Reader
	WriteOK  func() error
}

func HTTPProxyRequest(c net.Conn, tokenOK func(user, pass string) bool) (*HTTPRequest, error) {
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return nil, err
	}
	user, pass, ok := parseProxyBasic(req.Header.Get("Proxy-Authorization"))
	if !ok || tokenOK == nil || !tokenOK(user, pass) {
		_, _ = c.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"wsproxy\"\r\nContent-Length: 0\r\n\r\n"))
		return nil, fmt.Errorf("代理需要认证")
	}
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")

	if req.Method == http.MethodConnect {
		target := req.Host
		if !hasPort(target) {
			target += ":443"
		}
		return &HTTPRequest{
			Target:   target,
			Connect:  true,
			Buffered: br,
			WriteOK: func() error {
				_, err := c.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
				return err
			},
		}, nil
	}

	target := req.Host
	if target == "" && req.URL != nil {
		target = req.URL.Host
	}
	if target == "" {
		return nil, fmt.Errorf("缺少 Host")
	}
	if !hasPort(target) {
		target += ":80"
	}

	req.RequestURI = ""
	if req.URL != nil {
		req.URL.Scheme = ""
		req.URL.Host = ""
	}
	var buf bytes.Buffer
	if err := req.Write(&buf); err != nil {
		return nil, err
	}
	return &HTTPRequest{
		Target:   target,
		Connect:  false,
		Buffered: io.MultiReader(&buf, br),
	}, nil
}

func parseProxyBasic(h string) (string, string, bool) {
	const p = "Basic "
	if !strings.HasPrefix(h, p) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len(p):]))
	if err != nil {
		return "", "", false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	return user, pass, ok
}

func HTTPProxyLogin(c net.Conn, user, pass string) error {
	token := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	req := "GET / HTTP/1.0\r\nProxy-Authorization: Basic " + token + "\r\n\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		return err
	}
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		if err == io.EOF {
			return nil
		}
		return err
	}
	if strings.Contains(line, "407") {
		return fmt.Errorf("http 代理认证失败")
	}
	return nil
}

func hasPort(host string) bool {
	if strings.HasPrefix(host, "[") {
		return strings.Contains(host, "]:")
	}
	return strings.Count(host, ":") == 1
}

func PrefixConn(c net.Conn, r io.Reader) net.Conn {
	return &prefixConn{Conn: c, r: r}
}

type prefixConn struct {
	net.Conn
	r io.Reader
}

func (c *prefixConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}
