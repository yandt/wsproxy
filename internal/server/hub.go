package server

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"wsproxy/internal/allow"
	"wsproxy/internal/auth"
	"wsproxy/internal/proto"
	"wsproxy/internal/tunnel"
)

type session struct {
	id     string
	ch     chan []byte
	closed chan struct{}
	ready  chan error
}

type agentConn struct {
	name     string
	conn     *websocket.Conn
	writeMu  sync.Mutex
	sessMu   sync.Mutex
	sessions map[string]*session
	closers  []io.Closer
}

func newAgent(name string, conn *websocket.Conn) *agentConn {
	return &agentConn{
		name:     name,
		conn:     conn,
		sessions: make(map[string]*session),
	}
}

func (a *agentConn) send(m proto.Msg) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.conn.WriteMessage(websocket.TextMessage, m.Bytes())
}

func (a *agentConn) openShell(id string, cols, rows int) (*session, error) {
	s := a.addSession(id)
	if err := a.send(proto.Msg{T: proto.TypeOpen, ID: id, Cols: cols, Rows: rows}); err != nil {
		a.removeSession(id)
		return nil, err
	}
	return s, nil
}

func (a *agentConn) openTCP(id, target string) (*session, error) {
	s := a.addSession(id)
	if err := a.send(proto.Msg{T: proto.TypeTCPOpen, ID: id, Target: target}); err != nil {
		a.removeSession(id)
		return nil, err
	}
	return s, nil
}

func (a *agentConn) openUDP(id, target string) (*session, error) {
	s := a.addSession(id)
	if err := a.send(proto.Msg{T: proto.TypeUDPOpen, ID: id, Target: target}); err != nil {
		a.removeSession(id)
		return nil, err
	}
	return s, nil
}

func (a *agentConn) addSession(id string) *session {
	s := &session{id: id, ch: make(chan []byte, 64), closed: make(chan struct{}), ready: make(chan error, 1)}
	a.sessMu.Lock()
	a.sessions[id] = s
	a.sessMu.Unlock()
	return s
}

func (a *agentConn) removeSession(id string) {
	a.sessMu.Lock()
	s, ok := a.sessions[id]
	if ok {
		delete(a.sessions, id)
	}
	a.sessMu.Unlock()
	if ok {
		select {
		case <-s.closed:
		default:
			close(s.closed)
		}
	}
}

func (a *agentConn) getSession(id string) *session {
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	return a.sessions[id]
}

func (a *agentConn) closeAll() {
	a.sessMu.Lock()
	ids := make([]string, 0, len(a.sessions))
	for id := range a.sessions {
		ids = append(ids, id)
	}
	a.sessMu.Unlock()
	for _, id := range ids {
		a.removeSession(id)
	}
}

func (a *agentConn) stopListeners() {
	for _, c := range a.closers {
		_ = c.Close()
	}
	a.closers = nil
}

func (a *agentConn) signalReady(id string, err error) {
	s := a.getSession(id)
	if s == nil {
		return
	}
	select {
	case s.ready <- err:
	default:
	}
}

func waitReady(s *session, d time.Duration) error {
	select {
	case err := <-s.ready:
		return err
	case <-s.closed:
		return fmt.Errorf("会话已关闭")
	case <-time.After(d):
		return fmt.Errorf("等待客户端接通超时")
	}
}

func (a *agentConn) readLoop() {
	defer a.closeAll()
	for {
		_, raw, err := a.conn.ReadMessage()
		if err != nil {
			return
		}
		msg, err := proto.Decode(raw)
		if err != nil {
			continue
		}
		s := a.getSession(msg.ID)
		if s == nil {
			continue
		}
		switch msg.T {
		case proto.TypeOK:
			a.signalReady(msg.ID, nil)
		case proto.TypeData:
			payload, err := msg.Payload()
			if err != nil || len(payload) == 0 {
				continue
			}
			select {
			case s.ch <- payload:
			case <-s.closed:
			}
		case proto.TypeErr:
			a.signalReady(msg.ID, fmt.Errorf("%s", msg.Err))
			a.removeSession(msg.ID)
		case proto.TypeClose:
			a.removeSession(msg.ID)
		}
	}
}

type Hub struct {
	mu          sync.Mutex
	agents      map[string]*agentConn
	seq         uint64
	accessToken string
	allow       allow.Sets
}

func NewHub(accessToken string) *Hub {
	return &Hub{agents: make(map[string]*agentConn), accessToken: accessToken}
}

func (h *Hub) SetAllow(a allow.Sets) {
	h.allow = a
}

func (h *Hub) AllowsAddr(addr net.Addr) bool {
	return h.allow.IPs.AllowsAddr(addr)
}

func (h *Hub) AllowsSource(remote string) bool {
	return h.allow.IPs.AllowsRemote(remote)
}

func (h *Hub) tokenOK(user, pass string) bool {
	return auth.Equal(pass, h.accessToken) || auth.Equal(user, h.accessToken)
}

func (h *Hub) Attach(name string, conn *websocket.Conn, exposes []proto.Expose) (*agentConn, error) {
	if !proto.ValidName(name) {
		return nil, fmt.Errorf("客户端名字不合法")
	}
	if !h.allow.Clients.AllowsName(name) {
		return nil, fmt.Errorf("客户端 %s 不在白名单", name)
	}

	h.mu.Lock()
	if old, ok := h.agents[name]; ok {
		_ = old.conn.Close()
		old.stopListeners()
		old.closeAll()
		slog.Info("agent replaced", "id", name)
	}
	a := newAgent(name, conn)
	h.agents[name] = a
	h.mu.Unlock()

	a.startExposes(h, exposes)
	slog.Info("agent connected", "id", name)
	return a, nil
}

func (h *Hub) Detach(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for name, a := range h.agents {
		if a.conn == conn {
			a.closeAll()
			a.stopListeners()
			delete(h.agents, name)
			slog.Info("agent disconnected", "id", name)
			return
		}
	}
}

func (h *Hub) AgentOnline() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.agents) > 0
}

func (h *Hub) Has(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.agents[name]
	return ok
}

func (h *Hub) Names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.agents))
	for name := range h.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (h *Hub) Resolve(name string) (*agentConn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if name != "" {
		a, ok := h.agents[name]
		if !ok {
			return nil, fmt.Errorf("客户端 %s 未连接", name)
		}
		return a, nil
	}
	switch len(h.agents) {
	case 0:
		return nil, fmt.Errorf("客户端未连接")
	case 1:
		for _, a := range h.agents {
			return a, nil
		}
	default:
		return nil, fmt.Errorf("有多台客户端，请指定名字：%s", strings.Join(h.namesLocked(), ", "))
	}
	return nil, fmt.Errorf("客户端未连接")
}

func (h *Hub) namesLocked() []string {
	names := make([]string, 0, len(h.agents))
	for name := range h.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (h *Hub) nextID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	return fmt.Sprintf("s%d", h.seq)
}

func (h *Hub) OpenShell(name string, cols, rows int) (*session, *agentConn, error) {
	a, err := h.Resolve(name)
	if err != nil {
		return nil, nil, err
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	s, err := a.openShell(h.nextID(), cols, rows)
	return s, a, err
}

func (h *Hub) pipe(a *agentConn, s *session, in io.Reader, out io.Writer) {
	defer func() {
		_ = a.send(proto.Msg{T: proto.TypeClose, ID: s.id})
		a.removeSession(s.id)
	}()

	go func() {
		defer a.removeSession(s.id)
		buf := make([]byte, 32*1024)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if werr := a.send(proto.EncodeData(s.id, buf[:n])); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-s.closed:
			return
		case payload, ok := <-s.ch:
			if !ok {
				return
			}
			if _, err := out.Write(payload); err != nil {
				return
			}
		}
	}
}

func (h *Hub) Resize(a *agentConn, id string, cols, rows int) {
	if a == nil {
		return
	}
	_ = a.send(proto.Msg{T: proto.TypeResize, ID: id, Cols: cols, Rows: rows})
}

func (a *agentConn) startExposes(h *Hub, exposes []proto.Expose) {
	for _, exp := range exposes {
		kind := exp.Kind
		if kind == "" {
			kind = proto.KindTCP
		}
		slog.Info("expose listening", "id", a.name, "kind", kind, "listen", exp.Listen, "target", exp.Target)
		switch kind {
		case proto.KindTCP:
			h.listenTCP(a, exp.Listen, func(c net.Conn) {
				h.bridgeTCP(a, c, exp.Target, nil)
			})
		case proto.KindSOCKS:
			h.listenTCP(a, exp.Listen, func(c net.Conn) {
				target, err := tunnel.SOCKS5Target(c, h.tokenOK)
				if err != nil {
					return
				}
				if err := h.bridgeTCP(a, c, target, func() error { return tunnel.SOCKS5OK(c) }); err != nil {
					_ = tunnel.SOCKS5Fail(c)
				}
			})
		case proto.KindHTTP:
			h.listenTCP(a, exp.Listen, func(c net.Conn) {
				req, err := tunnel.HTTPProxyRequest(c, h.tokenOK)
				if err != nil {
					return
				}
				var in io.Reader = c
				if req.Buffered != nil {
					in = tunnel.PrefixConn(c, req.Buffered)
				}
				if err := h.bridgeTCP(a, &rwc{r: in, w: c, c: c}, req.Target, req.WriteOK); err != nil {
					_, _ = c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
				}
			})
		case proto.KindUDP:
			h.listenUDP(a, exp.Listen, exp.Target)
		default:
			slog.Error("unknown expose kind", "kind", kind)
		}
	}
}

type rwc struct {
	r io.Reader
	w io.Writer
	c io.Closer
}

func (c *rwc) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *rwc) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *rwc) Close() error                { return c.c.Close() }

func (h *Hub) listenTCP(a *agentConn, addr string, handle func(net.Conn)) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("listen expose failed", "id", a.name, "addr", addr, "err", err)
		return
	}
	a.closers = append(a.closers, ln)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				if !h.allow.IPs.AllowsAddr(conn.RemoteAddr()) {
					return
				}
				handle(conn)
			}(c)
		}
	}()
}

func (h *Hub) bridgeTCP(a *agentConn, c io.ReadWriteCloser, target string, afterOK func() error) error {
	if !h.allow.Targets.AllowsHostPort(target) {
		return fmt.Errorf("目标不在白名单: %s", target)
	}
	s, err := a.openTCP(h.nextID(), target)
	if err != nil {
		return err
	}
	if err := waitReady(s, 10*time.Second); err != nil {
		a.removeSession(s.id)
		return err
	}
	if afterOK != nil {
		if err := afterOK(); err != nil {
			a.removeSession(s.id)
			return err
		}
	}
	h.pipe(a, s, c, c)
	return nil
}

func (h *Hub) listenUDP(a *agentConn, addr, target string) {
	if !h.allow.Targets.AllowsHostPort(target) {
		slog.Error("udp target not allowed", "id", a.name, "target", target)
		return
	}
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		slog.Error("listen udp failed", "id", a.name, "addr", addr, "err", err)
		return
	}
	a.closers = append(a.closers, pc)
	go h.serveUDP(a, pc, target)
}

func (h *Hub) serveUDP(a *agentConn, pc net.PacketConn, target string) {
	type flow struct {
		sess *session
		addr net.Addr
		seen time.Time
	}
	var mu sync.Mutex
	flows := map[string]*flow{}
	done := make(chan struct{})
	defer close(done)

	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				mu.Lock()
				for k, f := range flows {
					if time.Since(f.seen) > 60*time.Second {
						a.removeSession(f.sess.id)
						delete(flows, k)
					}
				}
				mu.Unlock()
			}
		}
	}()

	buf := make([]byte, 65535)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		if !h.allow.IPs.AllowsAddr(addr) {
			continue
		}
		key := addr.String()
		pkt := append([]byte(nil), buf[:n]...)

		mu.Lock()
		f := flows[key]
		mu.Unlock()
		if f == nil {
			s, err := a.openUDP(h.nextID(), target)
			if err != nil {
				continue
			}
			if err := waitReady(s, 10*time.Second); err != nil {
				a.removeSession(s.id)
				continue
			}
			f = &flow{sess: s, addr: addr, seen: time.Now()}
			mu.Lock()
			if existing := flows[key]; existing != nil {
				mu.Unlock()
				a.removeSession(s.id)
				f = existing
			} else {
				flows[key] = f
				mu.Unlock()
				go func(fl *flow) {
					for {
						select {
						case <-fl.sess.closed:
							return
						case payload, ok := <-fl.sess.ch:
							if !ok {
								return
							}
							_, _ = pc.WriteTo(payload, fl.addr)
						}
					}
				}(f)
			}
		}
		mu.Lock()
		f.seen = time.Now()
		sess := f.sess
		mu.Unlock()
		if err := a.send(proto.EncodeData(sess.id, pkt)); err != nil {
			return
		}
	}
}
