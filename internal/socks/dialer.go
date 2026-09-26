// Package socks 는 최소 기능의 SOCKS5 클라이언트다 (RFC 1928, no-auth CONNECT).
//
// hostname 은 항상 ATYP 0x03 으로 보내 name resolution 이 터널 반대편에서
// 일어나게 한다; 이 패키지는 target 에 대해 절대 리졸버를 호출하지 않는다.
package socks

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"
)

const (
	version5     = 0x05
	methodNoAuth = 0x00
	methodNone   = 0xff
	cmdConnect   = 0x01
	atypIPv4     = 0x01
	atypDomain   = 0x03
	atypIPv6     = 0x04
)

// ReplyError 는 SOCKS 서버가 보낸 비정상 응답이다.
type ReplyError struct{ Code byte }

func (e *ReplyError) Error() string {
	msg := map[byte]string{
		0x01: "general SOCKS server failure",
		0x02: "connection not allowed by ruleset",
		0x03: "network unreachable",
		0x04: "host unreachable",
		0x05: "connection refused",
		0x06: "TTL expired",
		0x07: "command not supported",
		0x08: "address type not supported",
	}[e.Code]
	if msg == "" {
		msg = "unknown error"
	}
	return fmt.Sprintf("socks: %s (0x%02x)", msg, e.Code)
}

var ErrProtocol = errors.New("socks: protocol error")

// Dialer 는 ProxyAddr 의 SOCKS5 서버를 거쳐 target 에 연결한다.
type Dialer struct {
	ProxyAddr string
	// Forward 는 SOCKS 서버 자체에 도달하는 데 쓰인다. nil 이면 빈 net.Dialer 를 쓴다.
	Forward *net.Dialer
}

// DialContext 는 host:port 로 터널을 연다. 반환되는 conn 은 SOCKS 서버로의
// 원본 TCP 연결이라 half-close 를 위한 CloseWrite 를 지원한다.
func (d *Dialer) DialContext(ctx context.Context, host, port string) (net.Conn, error) {
	portNum, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNum == 0 {
		return nil, fmt.Errorf("socks: invalid port %q", port)
	}
	req, err := connectRequest(host, uint16(portNum))
	if err != nil {
		return nil, err
	}
	conn, err := d.dialServer(ctx)
	if err != nil {
		return nil, err
	}
	err = withContext(ctx, conn, func() error {
		if err := greet(conn); err != nil {
			return err
		}
		if _, err := conn.Write(req); err != nil {
			return fmt.Errorf("socks: send request: %w", err)
		}
		return readReply(conn)
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// Probe 는 SOCKS5 서버가 no-auth greeting 에 응답하는지 확인한다.
func Probe(ctx context.Context, addr string) error {
	d := &Dialer{ProxyAddr: addr}
	conn, err := d.dialServer(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return withContext(ctx, conn, func() error { return greet(conn) })
}

func (d *Dialer) dialServer(ctx context.Context) (net.Conn, error) {
	fwd := d.Forward
	if fwd == nil {
		fwd = &net.Dialer{}
	}
	conn, err := fwd.DialContext(ctx, "tcp", d.ProxyAddr)
	if err != nil {
		return nil, fmt.Errorf("socks: connect to %s: %w", d.ProxyAddr, err)
	}
	return conn, nil
}

// withContext 는 블로킹 handshake 를 ctx 로 제한하고 이후 deadline 을 지운다.
func withContext(ctx context.Context, conn net.Conn, fn func() error) error {
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	err := fn()
	if !stop() {
		if err == nil {
			err = ctx.Err()
		}
		return fmt.Errorf("socks: handshake interrupted: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return err
}

func greet(conn net.Conn) error {
	if _, err := conn.Write([]byte{version5, 1, methodNoAuth}); err != nil {
		return fmt.Errorf("socks: send greeting: %w", err)
	}
	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return fmt.Errorf("socks: read greeting reply: %w", err)
	}
	if resp[0] != version5 {
		return fmt.Errorf("%w: server version 0x%02x", ErrProtocol, resp[0])
	}
	if resp[1] == methodNone {
		return fmt.Errorf("%w: server requires authentication", ErrProtocol)
	}
	if resp[1] != methodNoAuth {
		return fmt.Errorf("%w: unexpected auth method 0x%02x", ErrProtocol, resp[1])
	}
	return nil
}

func connectRequest(host string, port uint16) ([]byte, error) {
	req := []byte{version5, cmdConnect, 0x00}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Zone() == "" {
		ip = ip.Unmap()
		if ip.Is4() {
			req = append(req, atypIPv4)
		} else {
			req = append(req, atypIPv6)
		}
		req = append(req, ip.AsSlice()...)
	} else {
		if host == "" || len(host) > 255 {
			return nil, fmt.Errorf("socks: invalid host length %d", len(host))
		}
		req = append(req, atypDomain, byte(len(host)))
		req = append(req, host...)
	}
	return binary.BigEndian.AppendUint16(req, port), nil
}

func readReply(conn net.Conn) error {
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return fmt.Errorf("socks: read reply: %w", err)
	}
	if hdr[0] != version5 {
		return fmt.Errorf("%w: reply version 0x%02x", ErrProtocol, hdr[0])
	}
	if hdr[1] != 0x00 {
		return &ReplyError{Code: hdr[1]}
	}
	var addrLen int
	switch hdr[3] {
	case atypIPv4:
		addrLen = 4
	case atypIPv6:
		addrLen = 16
	case atypDomain:
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return fmt.Errorf("socks: read reply: %w", err)
		}
		addrLen = int(l[0])
	default:
		return fmt.Errorf("%w: reply address type 0x%02x", ErrProtocol, hdr[3])
	}
	if _, err := io.CopyN(io.Discard, conn, int64(addrLen+2)); err != nil {
		return fmt.Errorf("socks: read reply: %w", err)
	}
	return nil
}
