package proxy

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
)

// handleConnect 는 "CONNECT host:port" 를 처리한다. 터널은 불투명한 바이트를
// 그대로 나른다: TLS, SNI, 인증서가 변형 없이 통과한다.
func (s *Server) handleConnect(ctx context.Context, c *countingConn, br *bufio.Reader, req *http.Request) {
	authority := req.RequestURI
	if authority == "" {
		authority = req.Host
	}
	host, port, err := router.SplitHostPort(authority)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid CONNECT target")
		return
	}
	if port == "" {
		port = "443"
	}
	decision := s.router.Decide(host)
	ev := s.newEvent(http.MethodConnect, host, port, decision)
	s.stats.Attempt(decision.Mode == router.ModeProxy)
	s.stats.Hit(decision.Matched)

	dialCtx, cancel := context.WithTimeout(ctx, s.opts.ConnectTimeout)
	upstream, err := s.transport.Dial(dialCtx, decision, host, port)
	cancel()
	if err != nil {
		s.stats.Fail()
		code := http.StatusBadGateway
		if isTimeout(err) {
			code = http.StatusGatewayTimeout
		}
		writeError(c, code, "RouteBox: upstream connection failed")
		ev.State, ev.Error, ev.Duration = events.ConnFailed, err, time.Since(ev.Time)
		s.publish(ev)
		return
	}

	if _, err := c.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = upstream.Close()
		return
	}
	s.publish(ev)

	// br 은 클라이언트가 CONNECT 헤더 직후에 보낸 바이트(예: TLS ClientHello)를
	// 이미 담고 있을 수 있다; br 에서부터 relay 해야 그 바이트들도 전달된다.
	up, down := relay(ctx, c, br, upstream, s.opts.IdleTimeout)
	ev.State, ev.BytesOut, ev.BytesIn, ev.Duration = events.ConnClosed, up, down, time.Since(ev.Time)
	ev.Time = time.Now()
	s.publish(ev)
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
