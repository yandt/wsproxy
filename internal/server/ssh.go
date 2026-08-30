package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"log/slog"
	"os"
	"path/filepath"

	glssh "github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"

	"wsproxy/internal/auth"
)

func (s *Server) startSSH() error {
	signer, err := loadOrCreateHostKey(s.HostKeyPath)
	if err != nil {
		return err
	}

	srv := &glssh.Server{
		Addr: s.SSHAddr,
		PasswordHandler: func(ctx glssh.Context, password string) bool {
			if !s.Hub.AllowsAddr(ctx.RemoteAddr()) {
				return false
			}
			return auth.Equal(password, s.AccessToken)
		},
		Handler: s.handleSSH,
	}
	srv.AddHostKey(signer)
	slog.Info("ssh listen", "addr", s.SSHAddr)
	return srv.ListenAndServe()
}

func (s *Server) handleSSH(sess glssh.Session) {
	name := sess.User()
	if auth.Equal(name, s.AccessToken) {
		name = ""
	}

	if !s.Hub.AgentOnline() {
		_, _ = sess.Write([]byte("客户端未连接\n"))
		_ = sess.Exit(1)
		return
	}

	cols, rows := 80, 24
	ptyReq, winCh, isPty := sess.Pty()
	if isPty {
		if ptyReq.Window.Width > 0 {
			cols = ptyReq.Window.Width
		}
		if ptyReq.Window.Height > 0 {
			rows = ptyReq.Window.Height
		}
	}

	sh, agent, err := s.Hub.OpenShell(name, cols, rows)
	if err != nil {
		_, _ = sess.Write([]byte(err.Error() + "\n"))
		_ = sess.Exit(1)
		return
	}

	if isPty {
		go func() {
			for win := range winCh {
				s.Hub.Resize(agent, sh.id, win.Width, win.Height)
			}
		}()
	}

	s.Hub.pipe(agent, sh, sess, sess)
	_ = sess.Exit(0)
}

func loadOrCreateHostKey(path string) (gossh.Signer, error) {
	if path == "" {
		path = "ssh_host_key"
	}
	if raw, err := os.ReadFile(path); err == nil {
		return gossh.ParsePrivateKey(raw)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(block)
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, err
	}
	return gossh.ParsePrivateKey(pemBytes)
}
