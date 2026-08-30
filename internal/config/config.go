package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"wsproxy/internal/proto"
	"wsproxy/internal/tunnel"
)

type Server struct {
	HTTP         string   `yaml:"http"`
	SSH          string   `yaml:"ssh"`
	AgentToken   string   `yaml:"agent_token"`
	AccessToken  string   `yaml:"access_token"`
	HostKey      string   `yaml:"host_key"`
	AllowIPs     []string `yaml:"allow_ips"`
	AllowClients []string `yaml:"allow_clients"`
	AllowTargets []string `yaml:"allow_targets"`
}

type Client struct {
	Server       string       `yaml:"server"`
	AgentToken   string       `yaml:"agent_token"`
	ID           string       `yaml:"id"`
	Shell        string       `yaml:"shell"`
	Expose       []ExposeItem `yaml:"expose"`
	AllowTargets []string     `yaml:"allow_targets"`
}

type ExposeItem struct {
	Kind   string
	Listen string
	Target string
}

func (e *ExposeItem) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		exp, err := tunnel.ParseExpose(n.Value)
		if err != nil {
			return err
		}
		*e = ExposeItem{Kind: exp.Kind, Listen: exp.Listen, Target: exp.Target}
		return nil
	}

	var aux struct {
		Kind   string `yaml:"kind"`
		Listen string `yaml:"listen"`
		Target string `yaml:"target"`
	}
	if err := n.Decode(&aux); err != nil {
		return err
	}
	spec, err := composeExpose(aux.Kind, aux.Listen, aux.Target)
	if err != nil {
		return err
	}
	exp, err := tunnel.ParseExpose(spec)
	if err != nil {
		return err
	}
	*e = ExposeItem{Kind: exp.Kind, Listen: exp.Listen, Target: exp.Target}
	return nil
}

func composeExpose(kind, listen, target string) (string, error) {
	if listen == "" {
		return "", fmt.Errorf("expose 缺少 listen")
	}
	if kind == "" {
		kind = proto.KindTCP
	}
	switch kind {
	case proto.KindTCP, proto.KindUDP:
		if target == "" {
			return "", fmt.Errorf("%s 隧道需要 target", kind)
		}
		return kind + "://" + listen + "=" + target, nil
	case proto.KindSOCKS, "socks5", proto.KindHTTP:
		if target != "" {
			return "", fmt.Errorf("%s 隧道不要写 target", kind)
		}
		return kind + "://" + listen, nil
	default:
		return "", fmt.Errorf("不支持的隧道类型 %q", kind)
	}
}

func (c Client) Exposes() []proto.Expose {
	out := make([]proto.Expose, 0, len(c.Expose))
	for _, e := range c.Expose {
		out = append(out, proto.Expose{Kind: e.Kind, Listen: e.Listen, Target: e.Target})
	}
	return out
}

type file struct {
	ServerNode yaml.Node `yaml:"server"`
	ClientNode yaml.Node `yaml:"client"`
	HTTP       string    `yaml:"http"`
	SSH        string    `yaml:"ssh"`
	AgentToken string    `yaml:"agent_token"`
	AccessTok  string    `yaml:"access_token"`
	HostKey    string    `yaml:"host_key"`
	ID           string       `yaml:"id"`
	Shell        string       `yaml:"shell"`
	Expose       []ExposeItem `yaml:"expose"`
	AllowIPs     []string     `yaml:"allow_ips"`
	AllowClients []string     `yaml:"allow_clients"`
	AllowTargets []string     `yaml:"allow_targets"`
}

func loadFile(path string) (file, error) {
	var f file
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, fmt.Errorf("读配置 %s: %w", path, err)
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	return f, nil
}

func LoadServer(path string) (Server, error) {
	f, err := loadFile(path)
	if err != nil {
		return Server{}, err
	}
	if f.ServerNode.Kind == yaml.MappingNode {
		var s Server
		if err := f.ServerNode.Decode(&s); err != nil {
			return Server{}, fmt.Errorf("解析 server: %w", err)
		}
		return s, nil
	}
	return Server{
		HTTP:         f.HTTP,
		SSH:          f.SSH,
		AgentToken:   f.AgentToken,
		AccessToken:  f.AccessTok,
		HostKey:      f.HostKey,
		AllowIPs:     f.AllowIPs,
		AllowClients: f.AllowClients,
		AllowTargets: f.AllowTargets,
	}, nil
}

func LoadClient(path string) (Client, error) {
	f, err := loadFile(path)
	if err != nil {
		return Client{}, err
	}
	if f.ClientNode.Kind == yaml.MappingNode {
		var c Client
		if err := f.ClientNode.Decode(&c); err != nil {
			return Client{}, fmt.Errorf("解析 client: %w", err)
		}
		return c, nil
	}
	serverURL := ""
	if f.ServerNode.Kind == yaml.ScalarNode {
		serverURL = f.ServerNode.Value
	}
	return Client{
		Server:       serverURL,
		AgentToken:   f.AgentToken,
		ID:           f.ID,
		Shell:        f.Shell,
		Expose:       f.Expose,
		AllowTargets: f.AllowTargets,
	}, nil
}

func MergeServer(file Server, set map[string]string) Server {
	out := Server{
		HTTP:         ":8080",
		SSH:          ":2222",
		HostKey:      "ssh_host_key",
		AgentToken:   file.AgentToken,
		AccessToken:  file.AccessToken,
		AllowIPs:     append([]string{}, file.AllowIPs...),
		AllowClients: append([]string{}, file.AllowClients...),
		AllowTargets: append([]string{}, file.AllowTargets...),
	}
	if file.HTTP != "" {
		out.HTTP = file.HTTP
	}
	if file.SSH != "" {
		out.SSH = file.SSH
	}
	if file.HostKey != "" {
		out.HostKey = file.HostKey
	}
	if v, ok := set["http"]; ok {
		out.HTTP = v
	}
	if v, ok := set["ssh"]; ok {
		out.SSH = v
	}
	if v, ok := set["agent-token"]; ok {
		out.AgentToken = v
	}
	if v, ok := set["access-token"]; ok {
		out.AccessToken = v
	}
	if v, ok := set["host-key"]; ok {
		out.HostKey = v
	}
	return out
}

func MergeClient(file Client, set map[string]string, extraExpose []proto.Expose, extraTargets []string) Client {
	out := file
	out.AllowTargets = append([]string{}, file.AllowTargets...)
	if v, ok := set["server"]; ok {
		out.Server = v
	}
	if v, ok := set["agent-token"]; ok {
		out.AgentToken = v
	}
	if v, ok := set["id"]; ok {
		out.ID = v
	}
	if v, ok := set["shell"]; ok {
		out.Shell = v
	}
	for _, e := range extraExpose {
		out.Expose = append(out.Expose, ExposeItem{Kind: e.Kind, Listen: e.Listen, Target: e.Target})
	}
	out.AllowTargets = append(out.AllowTargets, extraTargets...)
	return out
}
