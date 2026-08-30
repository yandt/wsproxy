package server

import (
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"time"

	"wsproxy/internal/auth"
	"wsproxy/internal/proto"
	"wsproxy/internal/tunnel"
)

type TunnelStatus struct {
	Kind      string `json:"kind"`
	Listen    string `json:"listen"`
	Target    string `json:"target,omitempty"`
	ListenOK  bool   `json:"listen_ok"`
	PeerOK    bool   `json:"peer_ok"`
	ListenErr string `json:"listen_err,omitempty"`
	PeerErr   string `json:"peer_err,omitempty"`
	Note      string `json:"note,omitempty"`
}

type ClientStatus struct {
	ID      string         `json:"id"`
	Online  bool           `json:"online"`
	Tunnels []TunnelStatus `json:"tunnels"`
}

type StatusReport struct {
	OK      bool           `json:"ok"`
	HTTP    string         `json:"http"`
	SSH     string         `json:"ssh"`
	Clients []ClientStatus `json:"clients"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.statusAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rep := s.Diagnose(3 * time.Second)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if !rep.OK {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(rep)
}

func (s *Server) statusAuth(r *http.Request) bool {
	if s.accessOK(r) {
		return true
	}
	if auth.Equal(r.Header.Get("X-Access-Token"), s.AccessToken) {
		return true
	}
	return auth.Equal(r.Header.Get("X-Agent-Token"), s.AgentToken)
}

func (s *Server) Diagnose(timeout time.Duration) StatusReport {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	rep := StatusReport{
		HTTP: s.HTTPAddr,
		SSH:  s.SSHAddr,
	}
	clients := s.Hub.diagnose(timeout, s.AccessToken)
	rep.Clients = clients
	rep.OK = true
	if len(clients) == 0 {
		rep.OK = false
	}
	for _, c := range clients {
		if !c.Online {
			rep.OK = false
		}
		for _, t := range c.Tunnels {
			if !t.ListenOK || !t.PeerOK {
				rep.OK = false
			}
		}
	}
	return rep
}

func (h *Hub) diagnose(timeout time.Duration, accessToken string) []ClientStatus {
	h.mu.Lock()
	agents := make([]*agentConn, 0, len(h.agents))
	for _, a := range h.agents {
		agents = append(agents, a)
	}
	h.mu.Unlock()
	sort.Slice(agents, func(i, j int) bool { return agents[i].name < agents[j].name })

	out := make([]ClientStatus, 0, len(agents))
	for _, a := range agents {
		cs := ClientStatus{ID: a.name, Online: true}
		for _, exp := range a.exposes {
			cs.Tunnels = append(cs.Tunnels, h.probeTunnel(a, exp, timeout, accessToken))
		}
		out = append(out, cs)
	}
	return out
}

func (h *Hub) probeTunnel(a *agentConn, exp proto.Expose, timeout time.Duration, accessToken string) TunnelStatus {
	kind := exp.Kind
	if kind == "" {
		kind = proto.KindTCP
	}
	st := TunnelStatus{Kind: kind, Listen: exp.Listen, Target: exp.Target}
	addr := localDialAddr(exp.Listen)

	switch kind {
	case proto.KindUDP:
		if err := udpListenBusy(exp.Listen); err != nil {
			st.ListenOK = true
		} else {
			st.ListenErr = "入口没在听"
		}
		if err := h.probeThrough(a, proto.KindUDP, exp.Target, timeout); err != nil {
			st.PeerErr = err.Error()
		} else {
			st.PeerOK = true
		}
	case proto.KindSOCKS:
		if err := tcpDial(addr, timeout); err != nil {
			st.ListenErr = err.Error()
		} else {
			st.ListenOK = true
		}
		if err := socksEntry(addr, accessToken, timeout); err != nil {
			st.PeerErr = err.Error()
		} else {
			st.PeerOK = true
		}
		st.Note = "动态目标，对端表示入口认证通过"
	case proto.KindHTTP:
		if err := tcpDial(addr, timeout); err != nil {
			st.ListenErr = err.Error()
		} else {
			st.ListenOK = true
		}
		if err := httpEntry(addr, accessToken, timeout); err != nil {
			st.PeerErr = err.Error()
		} else {
			st.PeerOK = true
		}
		st.Note = "动态目标，对端表示入口认证通过"
	default:
		if err := tcpDial(addr, timeout); err != nil {
			st.ListenErr = err.Error()
		} else {
			st.ListenOK = true
		}
		if err := h.probeThrough(a, proto.KindTCP, exp.Target, timeout); err != nil {
			st.PeerErr = err.Error()
		} else {
			st.PeerOK = true
		}
	}
	return st
}

func (h *Hub) probeThrough(a *agentConn, kind, target string, timeout time.Duration) error {
	if target == "" {
		return nil
	}
	var (
		s   *session
		err error
	)
	id := h.nextID()
	if kind == proto.KindUDP {
		s, err = a.openUDP(id, target)
	} else {
		s, err = a.openTCP(id, target)
	}
	if err != nil {
		return err
	}
	defer func() {
		_ = a.send(proto.Msg{T: proto.TypeClose, ID: s.id})
		a.removeSession(s.id)
	}()
	return waitReady(s, timeout)
}

func tcpDial(addr string, timeout time.Duration) error {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	_ = c.Close()
	return nil
}

func socksEntry(addr, token string, timeout time.Duration) error {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	return tunnel.SOCKS5Login(c, "u", token)
}

func httpEntry(addr, token string, timeout time.Duration) error {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	return tunnel.HTTPProxyLogin(c, "u", token)
}

func udpListenBusy(addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	_ = pc.Close()
	return nil
}

func localDialAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
