package server

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gorilla/websocket"

	"wsproxy/internal/auth"
	"wsproxy/internal/proto"
	"wsproxy/internal/web"
)

const accessCookie = "wsproxy_access"

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	},
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/agent", s.handleAgent)
	mux.HandleFunc("/c/", s.handleClient)
	mux.HandleFunc("/t/", s.handleLegacyToken)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/", s.handleRoot)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" && r.URL.Path != "/agent" && r.URL.Path != "/status" {
			if !s.Hub.AllowsSource(r.RemoteAddr) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Agent-Token")
	if !auth.Equal(token, s.AgentToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	_, raw, err := conn.ReadMessage()
	if err != nil {
		return
	}
	hello, err := proto.Decode(raw)
	if err != nil || hello.T != proto.TypeHello {
		_ = conn.WriteMessage(websocket.TextMessage, proto.Msg{T: proto.TypeErr, Err: "need hello"}.Bytes())
		return
	}
	if !proto.ValidName(hello.Name) {
		_ = conn.WriteMessage(websocket.TextMessage, proto.Msg{T: proto.TypeErr, Err: "need name"}.Bytes())
		return
	}

	a, err := s.Hub.Attach(hello.Name, conn, hello.Exposes)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, proto.Msg{T: proto.TypeErr, Err: err.Error()}.Bytes())
		return
	}
	a.readLoop()
	s.Hub.Detach(conn)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !auth.Equal(r.FormValue("token"), s.AccessToken) {
			s.writeLogin(w, "token 不对")
			return
		}
		s.setAccessCookie(w, r)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !s.accessOK(r) {
		s.writeLogin(w, "")
		return
	}
	s.writeClientList(w, r)
}

func (s *Server) handleClient(w http.ResponseWriter, r *http.Request) {
	if !s.accessOK(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/c/"), "/")
	if rest == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	parts := strings.Split(rest, "/")
	name := parts[0]
	if !proto.ValidName(name) {
		http.NotFound(w, r)
		return
	}
	tail := ""
	if len(parts) >= 2 {
		tail = strings.Join(parts[1:], "/")
	}
	if tail == "ws" {
		s.handleTermWS(w, r, name)
		return
	}
	if tail != "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(web.Index)
}

func (s *Server) handleLegacyToken(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/t/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" || !auth.Equal(parts[0], s.AccessToken) {
		http.NotFound(w, r)
		return
	}
	s.setAccessCookie(w, r)
	next := "/"
	if len(parts) >= 2 && proto.ValidName(parts[1]) {
		next = "/c/" + parts[1] + "/"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: accessCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) writeLogin(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := string(web.Login)
	if errMsg != "" {
		page = strings.Replace(page, "{{ERR}}", `<p class="err">`+html.EscapeString(errMsg)+`</p>`, 1)
		w.WriteHeader(http.StatusUnauthorized)
	} else {
		page = strings.Replace(page, "{{ERR}}", "", 1)
	}
	_, _ = io.WriteString(w, page)
}

func (s *Server) writeClientList(w http.ResponseWriter, r *http.Request) {
	names := s.Hub.Names()
	if len(names) == 1 {
		http.Redirect(w, r, "/c/"+names[0]+"/", http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>wsproxy</title>`)
	b.WriteString(`<style>body{font-family:sans-serif;margin:2rem;background:#0b0d10;color:#e8eaed}a{color:#8ab4f8}li{margin:.4rem 0}</style></head><body>`)
	b.WriteString(`<h1>在线客户端</h1>`)
	if len(names) == 0 {
		b.WriteString(`<p>现在没有客户端连上来。</p>`)
	} else {
		b.WriteString(`<ul>`)
		for _, name := range names {
			href := "/c/" + name + "/"
			fmt.Fprintf(&b, `<li><a href="%s">%s</a></li>`, html.EscapeString(href), html.EscapeString(name))
		}
		b.WriteString(`</ul>`)
	}
	b.WriteString(`<p><a href="/logout">退出</a></p>`)
	b.WriteString(`</body></html>`)
	_, _ = io.WriteString(w, b.String())
}

func (s *Server) handleTermWS(w http.ResponseWriter, r *http.Request, name string) {
	if !s.Hub.Has(name) {
		http.Error(w, "客户端未连接", http.StatusBadGateway)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	sh, agent, err := s.Hub.OpenShell(name, 80, 24)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
		return
	}

	var writeMu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var peek map[string]any
			if json.Unmarshal(raw, &peek) == nil && peek["t"] == "resize" {
				cols, _ := peek["cols"].(float64)
				rows, _ := peek["rows"].(float64)
				s.Hub.Resize(agent, sh.id, int(cols), int(rows))
				continue
			}
			if err := agent.send(proto.EncodeData(sh.id, raw)); err != nil {
				return
			}
		}
	}()

	go func() {
		<-done
		_ = agent.send(proto.Msg{T: proto.TypeClose, ID: sh.id})
		agent.removeSession(sh.id)
	}()

	for {
		select {
		case <-done:
			return
		case <-sh.closed:
			return
		case payload, ok := <-sh.ch:
			if !ok {
				return
			}
			writeMu.Lock()
			err := conn.WriteMessage(websocket.BinaryMessage, payload)
			writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) accessOK(r *http.Request) bool {
	c, err := r.Cookie(accessCookie)
	if err != nil {
		return false
	}
	return auth.Equal(c.Value, s.AccessToken)
}

func (s *Server) setAccessCookie(w http.ResponseWriter, r *http.Request) {
	c := &http.Cookie{
		Name:     accessCookie,
		Value:    s.AccessToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 3600,
	}
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		c.Secure = true
	}
	http.SetCookie(w, c)
}

func (s *Server) startHTTP() error {
	slog.Info("http listen", "addr", s.HTTPAddr)
	return http.ListenAndServe(s.HTTPAddr, s.routes())
}
