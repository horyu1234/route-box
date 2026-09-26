package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"

	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/socks"
)

var ErrNoUpstream = errors.New("no SOCKS upstream configured")

// Transport 는 라우팅 결정에 따라 upstream 연결을 연다.
//
// DIRECT 는 Direct 를 통해 OS 리졸버를 쓴다. PROXY 는 호스트명을 그대로
// 결정된 업스트림의 SOCKS 서버에 넘기므로 resolve 는 항상 원격 쪽에서만
// 일어난다 — 로컬에서는 절대 hostname 을 resolve 하지 않는다.
type Transport struct {
	Direct    *net.Dialer
	upstreams atomic.Pointer[map[string]string]
}

// NewTransport 의 upstreams 는 업스트림 이름 → SOCKS 주소 표다.
func NewTransport(direct *net.Dialer, upstreams map[string]string) *Transport {
	if direct == nil {
		direct = &net.Dialer{}
	}
	t := &Transport{Direct: direct}
	t.SetUpstreams(upstreams)
	return t
}

// SetUpstreams 는 표를 통째로 교체한다. 호출자는 넘긴 map 을 이후에 수정하면 안 된다.
func (t *Transport) SetUpstreams(upstreams map[string]string) {
	if upstreams == nil {
		upstreams = map[string]string{}
	}
	t.upstreams.Store(&upstreams)
}

// SocksAddr 는 업스트림 이름의 SOCKS 주소를 돌려준다.
func (t *Transport) SocksAddr(name string) (string, bool) {
	addr, ok := (*t.upstreams.Load())[name]
	return addr, ok && addr != ""
}

// Dial 은 알 수 없는 업스트림을 DIRECT 로 대신하지 않고 실패시킨다.
func (t *Transport) Dial(ctx context.Context, d router.Decision, host router.Host, port string) (net.Conn, error) {
	switch d.Mode {
	case router.ModeProxy:
		addr, ok := t.SocksAddr(d.Upstream)
		if !ok {
			if d.Upstream == "" {
				return nil, ErrNoUpstream
			}
			return nil, fmt.Errorf("%w: %q", ErrNoUpstream, d.Upstream)
		}
		sd := &socks.Dialer{ProxyAddr: addr, Forward: &net.Dialer{KeepAlive: t.Direct.KeepAlive}}
		return sd.DialContext(ctx, host.Name, port)
	case router.ModeDirect:
		conn, err := t.Direct.DialContext(ctx, "tcp", net.JoinHostPort(host.Name, port))
		if err != nil {
			return nil, fmt.Errorf("direct dial: %w", err)
		}
		return conn, nil
	default:
		return nil, fmt.Errorf("unsupported route mode %q", d.Mode)
	}
}
