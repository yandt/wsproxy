package check

import (
	"bufio"
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
	r.addTo(nil, indent, name, ok, detail)
}

func (r *Report) addTo(w io.Writer, indent int, name string, ok bool, detail string) {
	r.Lines = append(r.Lines, Line{Name: name, OK: ok, Detail: detail, Indent: indent})
	if !ok {
		r.OK = false
	}
	if w != nil {
		printLine(w, indent, name, ok, detail)
	}
}

func flush(w io.Writer) {
	if f, ok := w.(*os.File); ok {
		_ = f.Sync()
	}
}

func printLine(w io.Writer, indent int, name string, ok bool, detail string) {
	mark := "不通"
	if ok {
		mark = "通"
	}
	pad := strings.Repeat("  ", indent)
	if detail != "" {
		fmt.Fprintf(w, "%s- %s  %s  %s\n", pad, name, mark, detail)
	} else {
		fmt.Fprintf(w, "%s- %s  %s\n", pad, name, mark)
	}
	flush(w)
}

func Print(w io.Writer, r Report) {
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprintln(w, r.Title)
	flush(w)
	for _, ln := range r.Lines {
		printLine(w, ln.Indent, ln.Name, ln.OK, ln.Detail)
	}
	if r.OK {
		fmt.Fprintln(w, "结果: 全部通过")
	} else {
		fmt.Fprintln(w, "结果: 有不通的项")
	}
}

func Server(cfg config.Server) Report {
	return ServerTo(os.Stdout, cfg)
}

func ServerTo(w io.Writer, cfg config.Server) Report {
	if w == nil {
		w = os.Stdout
	}
	r := Report{Title: "服务端连通性", OK: true}
	fmt.Fprintln(w, r.Title)
	flush(w)
	httpBase := localHTTP(cfg.HTTP)
	sshAddr := localDialAddr(cfg.SSH)

	fmt.Fprintf(w, "正在测网页/接口 %s …\n", httpBase+"/health")
	flush(w)
	healthOK, healthDetail := getHealth(httpBase)
	r.addTo(w, 0, "网页/接口", healthOK, healthDetail)

	fmt.Fprintf(w, "正在测 SSH 入口 %s …\n", sshAddr)
	flush(w)
	sshOK, sshDetail := tcpOK(sshAddr)
	r.addTo(w, 0, "SSH 入口", sshOK, sshDetail)

	fmt.Fprintln(w, "正在问各条隧道 …")
	flush(w)
	st, err := fetchStatus(httpBase, cfg.AccessToken, cfg.AgentToken, true)
	if err != nil {
		r.addTo(w, 0, "隧道总览", false, err.Error())
		finish(w, r.OK)
		return r
	}
	addClientTunnels(&r, w, st, "")
	finish(w, r.OK)
	return r
}

func Client(cfg config.Client) Report {
	return ClientTo(os.Stdout, cfg)
}

func ClientTo(w io.Writer, cfg config.Client) Report {
	if w == nil {
		w = os.Stdout
	}
	r := Report{Title: "客户端连通性", OK: true}
	fmt.Fprintln(w, r.Title)
	flush(w)
	base := publicHTTP(cfg.Server)
	id := cfg.ID
	if id == "" {
		id = client.DefaultID()
	}

	if strings.TrimSpace(cfg.Server) == "" || base == "http://" || base == "https://" {
		r.addTo(w, 0, "连主服务器", false, "没有服务器地址，请加 --server ws://IP:端口")
		finish(w, r.OK)
		return r
	}

	fmt.Fprintf(w, "正在测能不能连上服务器 %s …\n", cfg.Server)
	flush(w)
	healthOK, healthDetail := getHealth(base)
	if healthOK {
		r.addTo(w, 0, "连主服务器", true, cfg.Server+"  →  "+healthDetail)
	} else {
		r.addTo(w, 0, "连主服务器", false, cfg.Server+"  "+healthDetail)
		r.addTo(w, 0, "本机已上线", false, "服务器网页口都连不上，先检查地址、端口和防火墙")
		exposes := cfg.Exposes()
		if len(exposes) == 0 {
			r.addTo(w, 0, "本机隧道目标", true, "没有配置隧道")
		} else {
			r.addTo(w, 0, "本机隧道目标", true, fmt.Sprintf("共 %d 条", len(exposes)))
			for _, exp := range exposes {
				addLocalTarget(&r, w, exp)
			}
		}
		finish(w, r.OK)
		return r
	}

	fmt.Fprintln(w, "正在问服务器：这台客户端上线了没有 …")
	flush(w)
	quick, err := fetchStatus(base, "", cfg.AgentToken, false)
	if err != nil {
		r.addTo(w, 0, "问服务器状态", false, err.Error())
	} else {
		online := false
		for _, c := range quick.Clients {
			if c.ID == id {
				online = true
				break
			}
		}
		if online {
			r.addTo(w, 0, "本机已上线", true, id)
		} else {
			names := clientNames(quick)
			if names == "" {
				r.addTo(w, 0, "本机已上线", false, id+" 不在线。请先启动客户端: wsproxy client --config /etc/wsproxy/client.yaml")
			} else {
				r.addTo(w, 0, "本机已上线", false, id+" 不在线，现在在线: "+names)
			}
		}
	}

	fmt.Fprintln(w, "正在测各条隧道 …")
	flush(w)
	st, err := fetchStatus(base, "", cfg.AgentToken, true)
	if err != nil {
		r.addTo(w, 0, "隧道总览", false, err.Error())
	} else {
		addClientTunnels(&r, w, st, id)
	}

	exposes := cfg.Exposes()
	if len(exposes) == 0 {
		r.addTo(w, 0, "本机隧道目标", true, "没有配置隧道")
	} else {
		r.addTo(w, 0, "本机隧道目标", true, fmt.Sprintf("共 %d 条", len(exposes)))
		for _, exp := range exposes {
			addLocalTarget(&r, w, exp)
		}
	}
	finish(w, r.OK)
	return r
}

func finish(w io.Writer, ok bool) {
	if ok {
		fmt.Fprintln(w, "结果: 全部通过")
	} else {
		fmt.Fprintln(w, "结果: 有不通的项")
	}
	flush(w)
}

func Prompt(question string) string {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		f = os.Stdin
	} else {
		defer f.Close()
	}
	fmt.Fprint(f, question)
	line, _ := bufio.NewReader(f).ReadString('\n')
	return strings.TrimSpace(line)
}

func addClientTunnels(r *Report, w io.Writer, st server.StatusReport, onlyID string) {
	if len(st.Clients) == 0 {
		r.addTo(w, 0, "在线客户端", false, "没有客户端连上来")
		return
	}
	for _, c := range st.Clients {
		if onlyID != "" && c.ID != onlyID {
			continue
		}
		r.addTo(w, 0, "客户端 "+c.ID, c.Online, "")
		if len(c.Tunnels) == 0 {
			r.addTo(w, 1, "隧道", true, "没有隧道")
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
			r.addTo(w, 1, name, ok, detail)
		}
	}
}

func addLocalTarget(r *Report, w io.Writer, exp proto.Expose) {
	kind := exp.Kind
	if kind == "" {
		kind = proto.KindTCP
	}
	switch kind {
	case proto.KindSOCKS, proto.KindHTTP:
		r.addTo(w, 1, kind+" 入口在服务器上", true, "本机无需预开目标")
	case proto.KindUDP:
		if exp.Target == "" {
			r.addTo(w, 1, "udp 目标", false, "没写目标")
			return
		}
		c, err := net.DialTimeout("udp", exp.Target, 2*time.Second)
		if err != nil {
			r.addTo(w, 1, "udp "+exp.Target, false, err.Error())
			return
		}
		_ = c.Close()
		r.addTo(w, 1, "udp "+exp.Target, true, "能写出（UDP 不保证对面有人听）")
	default:
		if exp.Target == "" {
			r.addTo(w, 1, "tcp 目标", false, "没写目标")
			return
		}
		ok, detail := tcpOK(exp.Target)
		r.addTo(w, 1, "tcp "+exp.Target, ok, detail)
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

func fetchStatus(base, access, agent string, probe bool) (server.StatusReport, error) {
	var zero server.StatusReport
	u := strings.TrimRight(base, "/") + "/status"
	if !probe {
		u += "?probe=0"
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return zero, err
	}
	if access != "" {
		req.Header.Set("X-Access-Token", access)
	}
	if agent != "" {
		req.Header.Set("X-Agent-Token", agent)
	}
	cli := &http.Client{Timeout: 8 * time.Second}
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
