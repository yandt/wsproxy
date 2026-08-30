package client

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"wsproxy/internal/allow"
	"wsproxy/internal/proto"
	"wsproxy/internal/tunnel"
)

type Config struct {
	ID           string
	Server       string
	AgentToken   string
	Shell        string
	Exposes      []proto.Expose
	AllowTargets *allow.List
}

func DefaultID() string {
	h, err := os.Hostname()
	if err != nil {
		return "client"
	}
	return proto.SanitizeName(h)
}

func ParseExpose(s string) (proto.Expose, error) {
	return tunnel.ParseExpose(s)
}

func prepare(cfg Config) (Config, error) {
	if cfg.ID == "" {
		cfg.ID = DefaultID()
	}
	if !proto.ValidName(cfg.ID) {
		return cfg, fmt.Errorf("客户端名字不合法，只能用字母、数字、点、下划线和短横线")
	}
	if cfg.Shell == "" {
		cfg.Shell = os.Getenv("SHELL")
	}
	if cfg.Shell == "" {
		cfg.Shell = "/bin/bash"
	}
	return cfg, nil
}

func ConnectOnce(cfg Config) error {
	cfg, err := prepare(cfg)
	if err != nil {
		return err
	}
	return dialOnce(cfg)
}

func Run(cfg Config) error {
	var err error
	cfg, err = prepare(cfg)
	if err != nil {
		return err
	}
	for {
		if err := dialOnce(cfg); err != nil {
			slog.Error("agent disconnected", "err", err)
		}
		time.Sleep(2 * time.Second)
		slog.Info("reconnecting")
	}
}

type agent struct {
	cfg     Config
	conn    *websocket.Conn
	writeMu sync.Mutex
	sessMu  sync.Mutex
	sess    map[string]io.Closer
}

func (a *agent) send(m proto.Msg) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.conn.WriteMessage(websocket.TextMessage, m.Bytes())
}

func (a *agent) setSession(id string, c io.Closer) {
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	a.sess[id] = c
}

func (a *agent) takeSession(id string) io.Closer {
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	c := a.sess[id]
	delete(a.sess, id)
	return c
}

func (a *agent) getSession(id string) io.Closer {
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	return a.sess[id]
}

func (a *agent) closeAll() {
	a.sessMu.Lock()
	for id, c := range a.sess {
		_ = c.Close()
		delete(a.sess, id)
	}
	a.sessMu.Unlock()
}

func dialOnce(cfg Config) error {
	u, err := url.Parse(cfg.Server)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return fmt.Errorf("server 应为 ws:// 或 wss://")
	}
	u.Path = "/agent"
	u.RawQuery = ""

	hdr := http.Header{}
	hdr.Set("X-Agent-Token", cfg.AgentToken)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), hdr)
	if err != nil {
		return err
	}
	defer conn.Close()

	a := &agent{cfg: cfg, conn: conn, sess: make(map[string]io.Closer)}
	defer a.closeAll()

	if err := a.send(proto.Msg{T: proto.TypeHello, Name: cfg.ID, Exposes: cfg.Exposes}); err != nil {
		return err
	}
	slog.Info("connected", "id", cfg.ID, "server", u.String())
	return a.loop()
}

func (a *agent) loop() error {
	for {
		_, raw, err := a.conn.ReadMessage()
		if err != nil {
			return err
		}
		msg, err := proto.Decode(raw)
		if err != nil {
			continue
		}
		switch msg.T {
		case proto.TypeOpen:
			go a.openShell(msg)
		case proto.TypeTCPOpen:
			go a.openTCP(msg)
		case proto.TypeUDPOpen:
			go a.openUDP(msg)
		case proto.TypeData:
			a.onData(msg)
		case proto.TypeResize:
			a.onResize(msg)
		case proto.TypeClose:
			if c := a.takeSession(msg.ID); c != nil {
				_ = c.Close()
			}
		}
	}
}

type ptyFile struct {
	file *os.File
	cmd  *exec.Cmd
}

func (p *ptyFile) Close() error {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	if p.file != nil {
		return p.file.Close()
	}
	return nil
}

func (a *agent) openShell(msg proto.Msg) {
	cmd := exec.Command(a.cfg.Shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := pty.Start(cmd)
	if err != nil {
		_ = a.send(proto.Msg{T: proto.TypeErr, ID: msg.ID, Err: err.Error()})
		return
	}
	if msg.Cols > 0 && msg.Rows > 0 {
		_ = pty.Setsize(f, &pty.Winsize{Rows: uint16(msg.Rows), Cols: uint16(msg.Cols)})
	}
	a.setSession(msg.ID, &ptyFile{file: f, cmd: cmd})
	_ = a.send(proto.Msg{T: proto.TypeOK, ID: msg.ID})

	buf := make([]byte, 32*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if werr := a.send(proto.EncodeData(msg.ID, buf[:n])); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	if c := a.takeSession(msg.ID); c != nil {
		_ = c.Close()
	}
	_ = a.send(proto.Msg{T: proto.TypeClose, ID: msg.ID})
}

func (a *agent) openUDP(msg proto.Msg) {
	if !a.cfg.AllowTargets.AllowsHostPort(msg.Target) {
		_ = a.send(proto.Msg{T: proto.TypeErr, ID: msg.ID, Err: "目标不在白名单: " + msg.Target})
		return
	}
	c, err := net.DialTimeout("udp", msg.Target, 10*time.Second)
	if err != nil {
		_ = a.send(proto.Msg{T: proto.TypeErr, ID: msg.ID, Err: err.Error()})
		return
	}
	a.setSession(msg.ID, c)
	_ = a.send(proto.Msg{T: proto.TypeOK, ID: msg.ID})

	buf := make([]byte, 65535)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			if werr := a.send(proto.EncodeData(msg.ID, buf[:n])); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	if old := a.takeSession(msg.ID); old != nil {
		_ = old.Close()
	}
	_ = a.send(proto.Msg{T: proto.TypeClose, ID: msg.ID})
}

func (a *agent) openTCP(msg proto.Msg) {
	if !a.cfg.AllowTargets.AllowsHostPort(msg.Target) {
		_ = a.send(proto.Msg{T: proto.TypeErr, ID: msg.ID, Err: "目标不在白名单: " + msg.Target})
		return
	}
	c, err := net.DialTimeout("tcp", msg.Target, 10*time.Second)
	if err != nil {
		_ = a.send(proto.Msg{T: proto.TypeErr, ID: msg.ID, Err: err.Error()})
		return
	}
	a.setSession(msg.ID, c)
	_ = a.send(proto.Msg{T: proto.TypeOK, ID: msg.ID})

	buf := make([]byte, 32*1024)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			if werr := a.send(proto.EncodeData(msg.ID, buf[:n])); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	if old := a.takeSession(msg.ID); old != nil {
		_ = old.Close()
	}
	_ = a.send(proto.Msg{T: proto.TypeClose, ID: msg.ID})
}

func (a *agent) onData(msg proto.Msg) {
	payload, err := msg.Payload()
	if err != nil || len(payload) == 0 {
		return
	}
	c := a.getSession(msg.ID)
	switch t := c.(type) {
	case *ptyFile:
		_, _ = t.file.Write(payload)
	case net.Conn:
		_, _ = t.Write(payload)
	}
}

func (a *agent) onResize(msg proto.Msg) {
	c := a.getSession(msg.ID)
	p, ok := c.(*ptyFile)
	if !ok || p.file == nil || msg.Cols <= 0 || msg.Rows <= 0 {
		return
	}
	_ = pty.Setsize(p.file, &pty.Winsize{Rows: uint16(msg.Rows), Cols: uint16(msg.Cols)})
}
