package proto

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
)

const (
	TypeHello    = "hello"
	TypeOpen     = "open"
	TypeTCPOpen  = "tcp-open"
	TypeUDPOpen  = "udp-open"
	TypeData     = "data"
	TypeResize   = "resize"
	TypeClose    = "close"
	TypeOK       = "ok"
	TypeErr      = "err"

	KindTCP   = "tcp"
	KindUDP   = "udp"
	KindSOCKS = "socks"
	KindHTTP  = "http"
)

type Expose struct {
	Kind   string `json:"kind,omitempty"`
	Listen string `json:"listen"`
	Target string `json:"target,omitempty"`
}

type Msg struct {
	T       string   `json:"t"`
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name,omitempty"`
	D       string   `json:"d,omitempty"`
	Cols    int      `json:"cols,omitempty"`
	Rows    int      `json:"rows,omitempty"`
	Target  string   `json:"target,omitempty"`
	Err     string   `json:"err,omitempty"`
	Exposes []Expose `json:"exposes,omitempty"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func ValidName(s string) bool {
	return nameRe.MatchString(s)
}

func SanitizeName(s string) string {
	s = strings.TrimSpace(s)
	if ValidName(s) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 64 {
		out = out[:64]
	}
	if !ValidName(out) {
		return "client"
	}
	return out
}

func (m Msg) Bytes() []byte {
	b, err := json.Marshal(m)
	if err != nil {
		return []byte(`{"t":"err","err":"encode"}`)
	}
	return b
}

func Decode(raw []byte) (Msg, error) {
	var m Msg
	err := json.Unmarshal(raw, &m)
	return m, err
}

func EncodeData(id string, payload []byte) Msg {
	return Msg{T: TypeData, ID: id, D: base64.StdEncoding.EncodeToString(payload)}
}

func (m Msg) Payload() ([]byte, error) {
	if m.D == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(m.D)
}
