package e2e

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"wsproxy/internal/client"
	"wsproxy/internal/server"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func startServer(t *testing.T) (srv *server.Server, httpPort, sshPort int, access, agentTok string) {
	t.Helper()
	httpPort = freePort(t)
	sshPort = freePort(t)
	access = "VisitTokenTest"
	agentTok = "AgentTokenTest"
	srv = server.New(
		fmt.Sprintf("127.0.0.1:%d", httpPort),
		fmt.Sprintf("127.0.0.1:%d", sshPort),
		agentTok,
		access,
		filepath.Join(t.TempDir(), "host_key"),
	)
	go func() { _ = srv.Run() }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", httpPort))
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return srv, httpPort, sshPort, access, agentTok
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server did not start")
	return
}

func startClient(httpPort int, agentTok, id string) {
	go func() {
		_ = client.ConnectOnce(client.Config{
			ID:         id,
			Server:     fmt.Sprintf("ws://127.0.0.1:%d", httpPort),
			AgentToken: agentTok,
			Shell:      "/bin/bash",
		})
	}()
}

func waitClient(t *testing.T, hub *server.Hub, id string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if hub.Has(id) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("client %s did not connect", id)
}

func sshEcho(t *testing.T, sshPort int, user, password, cmd string) string {
	t.Helper()
	cfg := &gossh.ClientConfig{
		User:            user,
		Auth:            []gossh.AuthMethod{gossh.Password(password)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	}
	c, err := gossh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sshPort), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)
	if _, err := stdin.Write([]byte(cmd + "\n")); err != nil {
		t.Fatal(err)
	}

	seen := make(chan string, 1)
	var out strings.Builder
	var mu sync.Mutex
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				mu.Lock()
				out.Write(buf[:n])
				got := out.String()
				mu.Unlock()
				if strings.Contains(got, "hello-") {
					select {
					case seen <- got:
					default:
					}
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	select {
	case got := <-seen:
		return got
	case <-time.After(5 * time.Second):
		mu.Lock()
		got := out.String()
		mu.Unlock()
		t.Fatalf("did not see echo output: %q", got)
		return ""
	}
}

func loginWeb(t *testing.T, httpPort int, token string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	cli := &http.Client{Jar: jar}
	base := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	resp, err := cli.PostForm(base+"/", url.Values{"token": {token}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login status %d", resp.StatusCode)
	}
	return cli
}

func TestSSHAndToken(t *testing.T) {
	srv, httpPort, sshPort, access, agentTok := startServer(t)
	startClient(httpPort, agentTok, "box1")
	waitClient(t, srv.Hub, "box1")

	bad := fmt.Sprintf("http://127.0.0.1:%d/", httpPort)
	resp, err := http.Get(bad)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "访问 token") {
		t.Fatalf("expected login page, status=%d body=%s", resp.StatusCode, body)
	}

	cli := loginWeb(t, httpPort, access)
	resp, err = cli.Get(fmt.Sprintf("http://127.0.0.1:%d/", httpPort))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "xterm") {
		t.Fatalf("web term status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.HasSuffix(resp.Request.URL.Path, "/c/box1/") && resp.Request.URL.Path != "/c/box1/" {
		t.Fatalf("expected /c/box1/, got %s", resp.Request.URL.Path)
	}
	if strings.Contains(resp.Request.URL.Path, access) {
		t.Fatal("token must not appear in url")
	}

	sshEcho(t, sshPort, "box1", access, "echo hello-from-test")
	sshEcho(t, sshPort, access, access, "echo hello-from-token-user")
}

func TestTwoClients(t *testing.T) {
	srv, httpPort, sshPort, access, agentTok := startServer(t)
	startClient(httpPort, agentTok, "box1")
	startClient(httpPort, agentTok, "box2")
	waitClient(t, srv.Hub, "box1")
	waitClient(t, srv.Hub, "box2")

	cli := loginWeb(t, httpPort, access)
	listURL := fmt.Sprintf("http://127.0.0.1:%d/", httpPort)
	resp, err := cli.Get(listURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)
	if resp.StatusCode != 200 || !strings.Contains(page, "box1") || !strings.Contains(page, "box2") {
		t.Fatalf("list page: status=%d body=%s", resp.StatusCode, page)
	}
	if strings.Contains(page, "xterm") {
		t.Fatal("two clients should show list, not a terminal")
	}
	if strings.Contains(resp.Request.URL.String(), access) {
		t.Fatal("token must not appear in url")
	}

	termURL := fmt.Sprintf("http://127.0.0.1:%d/c/box2/", httpPort)
	resp, err = cli.Get(termURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "xterm") {
		t.Fatalf("box2 term status=%d", resp.StatusCode)
	}

	sshEcho(t, sshPort, "box1", access, "echo hello-box1")
	sshEcho(t, sshPort, "box2", access, "echo hello-box2")

	cfg := &gossh.ClientConfig{
		User:            access,
		Auth:            []gossh.AuthMethod{gossh.Password(access)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	}
	c, err := gossh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sshPort), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	out, err := sess.CombinedOutput("unused")
	text := string(out)
	if err == nil && !strings.Contains(text, "box1") {
		// handleSSH writes then Exit; CombinedOutput may still get the text
	}
	if !strings.Contains(text, "box1") || !strings.Contains(text, "box2") {
		t.Fatalf("expected client list in ssh error, got %q err=%v", text, err)
	}
}
