package tunnel

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
)

func SOCKS5Target(c net.Conn, tokenOK func(user, pass string) bool) (string, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return "", err
	}
	if hdr[0] != 5 {
		return "", fmt.Errorf("不是 socks5")
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", err
	}
	if !hasMethod(methods, 2) {
		_, _ = c.Write([]byte{5, 0xff})
		return "", fmt.Errorf("需要用户名密码")
	}
	if _, err := c.Write([]byte{5, 2}); err != nil {
		return "", err
	}
	if err := socks5UserPass(c, tokenOK); err != nil {
		return "", err
	}

	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return "", err
	}
	if req[0] != 5 {
		return "", fmt.Errorf("不是 socks5")
	}
	if req[1] != 1 {
		_ = socks5Reply(c, 7)
		return "", fmt.Errorf("只支持 CONNECT")
	}

	host, err := readSOCKSAddr(c, req[3])
	if err != nil {
		_ = socks5Reply(c, 1)
		return "", err
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(c, portBuf); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf)
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func socks5UserPass(c net.Conn, tokenOK func(user, pass string) bool) error {
	ver := make([]byte, 2)
	if _, err := io.ReadFull(c, ver); err != nil {
		return err
	}
	if ver[0] != 1 {
		return fmt.Errorf("不是用户名密码认证")
	}
	user := make([]byte, int(ver[1]))
	if _, err := io.ReadFull(c, user); err != nil {
		return err
	}
	plen := make([]byte, 1)
	if _, err := io.ReadFull(c, plen); err != nil {
		return err
	}
	pass := make([]byte, int(plen[0]))
	if _, err := io.ReadFull(c, pass); err != nil {
		return err
	}
	if tokenOK == nil || !tokenOK(string(user), string(pass)) {
		_, _ = c.Write([]byte{1, 1})
		return fmt.Errorf("认证失败")
	}
	_, err := c.Write([]byte{1, 0})
	return err
}

func hasMethod(methods []byte, want byte) bool {
	for _, m := range methods {
		if m == want {
			return true
		}
	}
	return false
}

func SOCKS5Login(c net.Conn, user, pass string) error {
	if _, err := c.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(c, rep); err != nil {
		return err
	}
	if rep[0] != 5 || rep[1] != 2 {
		return fmt.Errorf("socks 入口不接受用户名密码")
	}
	auth := []byte{1, byte(len(user))}
	auth = append(auth, user...)
	auth = append(auth, byte(len(pass)))
	auth = append(auth, pass...)
	if _, err := c.Write(auth); err != nil {
		return err
	}
	arep := make([]byte, 2)
	if _, err := io.ReadFull(c, arep); err != nil {
		return err
	}
	if arep[1] != 0 {
		return fmt.Errorf("socks 认证失败")
	}
	return nil
}

func SOCKS5OK(c net.Conn) error {
	return socks5Reply(c, 0)
}

func SOCKS5Fail(c net.Conn) error {
	return socks5Reply(c, 1)
}

func socks5Reply(c net.Conn, rep byte) error {
	_, err := c.Write([]byte{5, rep, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}

func readSOCKSAddr(r io.Reader, atyp byte) (string, error) {
	switch atyp {
	case 1:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(r, ip); err != nil {
			return "", err
		}
		return net.IP(ip).String(), nil
	case 4:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(r, ip); err != nil {
			return "", err
		}
		return net.IP(ip).String(), nil
	case 3:
		l := make([]byte, 1)
		if _, err := io.ReadFull(r, l); err != nil {
			return "", err
		}
		host := make([]byte, l[0])
		if _, err := io.ReadFull(r, host); err != nil {
			return "", err
		}
		return string(host), nil
	default:
		return "", fmt.Errorf("不支持的地址类型")
	}
}
