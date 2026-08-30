package tunnel

import (
	"fmt"
	"strings"

	"wsproxy/internal/proto"
)

func ParseExpose(s string) (proto.Expose, error) {
	s = strings.TrimSpace(s)
	kind := proto.KindTCP
	rest := s
	if i := strings.Index(s, "://"); i >= 0 {
		kind = strings.ToLower(s[:i])
		rest = s[i+3:]
	}
	if kind == "socks5" {
		kind = proto.KindSOCKS
	}

	switch kind {
	case proto.KindTCP, proto.KindUDP:
		listen, target, ok := strings.Cut(rest, "=")
		if !ok || listen == "" || target == "" {
			return proto.Expose{}, fmt.Errorf("格式应为 %s://端口=目标，例如 %s://9000=127.0.0.1:80", kind, kind)
		}
		if _, _, err := splitHostPortLoose(target); err != nil {
			return proto.Expose{}, fmt.Errorf("目标地址不合法: %s", target)
		}
		return proto.Expose{Kind: kind, Listen: normalizeListen(listen), Target: target}, nil

	case proto.KindSOCKS, proto.KindHTTP:
		if strings.Contains(rest, "=") {
			return proto.Expose{}, fmt.Errorf("%s 隧道不写目标，目标由连上来的人指定，例如 %s://1080", kind, kind)
		}
		if rest == "" {
			return proto.Expose{}, fmt.Errorf("格式应为 %s://端口，例如 %s://1080", kind, kind)
		}
		return proto.Expose{Kind: kind, Listen: normalizeListen(rest)}, nil

	default:
		return proto.Expose{}, fmt.Errorf("不支持的隧道类型 %q，可用 tcp、udp、socks、http", kind)
	}
}

func normalizeListen(listen string) string {
	if !strings.Contains(listen, ":") {
		return "127.0.0.1:" + listen
	}
	return listen
}

func splitHostPortLoose(addr string) (string, string, error) {
	host, port, err := splitLastColon(addr)
	if err != nil || host == "" || port == "" {
		return "", "", fmt.Errorf("need host:port")
	}
	return host, port, nil
}

func splitLastColon(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i <= 0 || i == len(addr)-1 {
		return "", "", fmt.Errorf("need host:port")
	}
	return addr[:i], addr[i+1:], nil
}
