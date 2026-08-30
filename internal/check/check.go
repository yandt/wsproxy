package check

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"wsproxy/internal/client"
	"wsproxy/internal/config"
	"wsproxy/internal/proto"
	"wsproxy/internal/server"
)

type Line struct {
	Name    string
	OK      bool
	Detail  string
	Indent  int
}

type Report struct {
	Title string
	Lines []Line
	OK    bool
}

func (r *Report) add(indent int, name string, ok bool, detail string) {
	r.Lines = append(r.Lines, Line{Name: name, OK: ok, Detail: detail, Indent: indent})
	if !ok {
		r.OK = false
	}
}

func Print(w io.Writer, r Report) {
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprintln(w, r.Title)
	for _, ln := range r.Lines {
		mark := "不通"
		if ln.OK {
			mark = "通"
		}
		pad := strings.Repeat("  ", ln.Indent)
		if ln.Detail != "" {
			fmt.Fprintf(w, "%s- %s  %s  %s\n", pad, ln.Name, mark, ln.Detail)
		} else {
			fmt.Fprintf(w, "%s- %s  %s\n", pad, ln.Name, mark)
		}
	}
	if r.OK {
		fmt.Fprintln(w, "结果: 全部通过")
	} else {
		fmt.Fprintln(w, "结果: 有不通的项")
	}
}

func Server(cfg config.Server) Report {
	r := Report{Title: "服务端连通性", OK: true}
	httpBase := localHTTP(cfg.HTTP)
	sshAddr := localDialAddr(cfg.SSH)

	healthOK, healthDetail := getHealth(httpBase)
	r.add(0, "网页/接口", healthOK, healthDetail)

	sshOK, sshDetail := tcpOK(sshAddr)
	r.add(0, "SSH 入口", sshOK, sshDetail)

	st, err := fetchStatus(httpBase, cfg.AccessToken, cfg.AgentToken)
	if err != nil {
		r.add(0, "隧道总览", false, err.Error())
		return r
	}
	addClientTunnels(&r, st, "")
	return r
}

func Client(cfg config.Client) Report {
	r := Report{Title: "客户端连通性", OK: true}
	base := publicHTTP(cfg.Server)
	id := cfg.ID
	if id == "" {
		id = client.DefaultID()
	}

	healthOK, healthDetail := getHealth(base)
	r.add(0, "连主服务器", healthOK, healthDetail)

	st, err := fetchStatus(base, "", cfg.AgentToken)
	if err != nil {
		r.add(0, "问服务器状态", false, err.Error())
	} else {
		online := false
		for _, c := range st.Clients {
			if c.ID == id {
				online = true
				break
			}
		}
		if online {
			r.add(0, "本机已上线", true, id)
		} else {
			names := clientNames(st)
			if names == "" {
				r.add(0, "本机已上线", false, id+" 不在线（服务器上还没有客户端）")
			} else {
				r.add(0, "本机已上线", false, id+" 不在线，现在在线: "+names)
			}
		}
		addClientTunnels(&r, st, id)
	}

	exposes := cfg.Exposes()
	if len(exposes) == 0 {
		r.add(0, "本机隧道目标", true, "没有配置隧道")
	} else {
		r.add(0, "本机隧道目标", true, fmt.Sprintf("共 %d 条", len(exposes)))
		for _, exp := range exposes {
			addLocalTarget(&r, exp)
		}
	}
	return r
}

func addClientTunnels(r *Report, st server.StatusReport, onlyID string) {
	if len(st.Clients) == 0 {
		r.add(0, "在线客户端", false, "没有客户端连上来")
		return
	}
	for _, c := range st.Clients {
		if onlyID != "" && c.ID != onlyID {
			continue
		}
		r.add(0, "客户端 "+c.ID, c.Online, "")
		if len(c.Tunnels) == 0 {
			r.add(1, "隧道", true, "没有隧道")
			continue
		}
		for _, t := range c.Tunnels {
			name := t.Kind + "  " + t.Listen
			if t.Target != "" {
				name += " → " + t.Target
			}
			ok := t.ListenOK && t.PeerOK
			detail := "入口 "
			if t.ListenOK {
				detail += "通"
			} else if t.ListenErr != "" {
				detail += "不通（" + t.ListenErr + "）"
			} else {
				detail += "不通"
			}
			detail += "  对端 "
			if t.PeerOK {
				detail += "通"
			} else if t.PeerErr != "" {
				detail += "不通（" + t.PeerErr + "）"
			} else {
				detail += "不通"
			}
			if t.Note != "" {
				detail += "  " + t.Note
			}
			r.add(1, name, ok, detail)
		}
	}
}

func addLocalTarget(r *Report, exp proto.Expose) {
	kind := exp.Kind
	if kind == "" {
		kind = proto.KindTCP
	}
	switch kind {
	case proto.KindSOCKS, proto.KindHTTP:
		r.add(1, kind+" 入口在服务器上", true, "本机无需预开目标")
	case proto.KindUDP:
		if exp.Target == "" {
			r.add(1, "udp 目标", false, "没写目标")
			return
		}
		c, err := net.DialTimeout("udp", exp.Target, 2*time.Second)
		if err != nil {
			r.add(1, "udp "+exp.Target, false, err.Error())
			return
		}
		_ = c.Close()
		r.add(1, "udp "+exp.Target, true, "能写出（UDP 不保证对面有人听）")
	default:
		if exp.Target == "" {
			r.add(1, "tcp 目标", false, "没写目标")
			return
		}
		ok, detail := tcpOK(exp.Target)
		r.add(1, "tcp "+exp.Target, ok, detail)
	}
}

func getHealth(base string) (bool, string) {
	cli := &http.Client{Timeout: 3 * time.Second}
	resp, err := cli.Get(strings.TrimRight(base, "/") + "/health")
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return true, strings.TrimSpace(string(body))
}

func fetchStatus(base, access, agent string) (server.StatusReport, error) {
	var zero server.StatusReport
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/status", nil)
	if err != nil {
		return zero, err
	}
	if access != "" {
		req.Header.Set("X-Access-Token", access)
	}
	if agent != "" {
		req.Header.Set("X-Agent-Token", agent)
	}
	cli := &http.Client{Timeout: 15 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return zero, fmt.Errorf("状态接口口令不对")
	}
	var st server.StatusReport
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return zero, fmt.Errorf("读状态失败: %w", err)
	}
	return st, nil
}

func tcpOK(addr string) (bool, string) {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false, err.Error()
	}
	_ = c.Close()
	return true, addr
}

func clientNames(st server.StatusReport) string {
	var names []string
	for _, c := range st.Clients {
		names = append(names, c.ID)
	}
	return strings.Join(names, ", ")
}

func localHTTP(addr string) string {
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			return "http://127.0.0.1" + addr
		}
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func localDialAddr(addr string) string {
	if addr == "" {
		addr = ":2222"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			return "127.0.0.1" + addr
		}
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func publicHTTP(serverURL string) string {
	s := strings.TrimSpace(serverURL)
	s = strings.TrimPrefix(s, "wss://")
	s = strings.TrimPrefix(s, "ws://")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if strings.HasPrefix(serverURL, "wss://") || strings.HasPrefix(serverURL, "https://") {
		return "https://" + s
	}
	return "http://" + s
}

func DefaultConfigs() (serverPath, clientPath string) {
	return "/etc/wsproxy/server.yaml", "/etc/wsproxy/client.yaml"
}
