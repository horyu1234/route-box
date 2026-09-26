package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
)

type decisionKey struct{}

// hopHeaders 는 connection 범위 헤더라서 전달하면 안 된다 (RFC 9110 §7.6.1).
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func removeHopHeaders(h http.Header) {
	for _, v := range h.Values("Connection") {
		for f := range strings.SplitSeq(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				h.Del(f)
			}
		}
	}
	for _, k := range hopHeaders {
		h.Del(k)
	}
}

// handleHTTP 는 absolute-form 의 plain HTTP 요청 하나를 전달하고 클라이언트
// 연결을 재사용할 수 있는지 보고한다.
func (s *Server) handleHTTP(ctx context.Context, c *countingConn, req *http.Request) bool {
	if req.URL.Host == "" || !strings.EqualFold(req.URL.Scheme, "http") {
		writeError(c, http.StatusBadRequest, "RouteBox is a proxy: send absolute http:// URLs or use CONNECT")
		return false
	}
	host, port, err := router.SplitHostPort(req.URL.Host)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid host")
		return false
	}
	if port == "" {
		port = "80"
	}
	decision := s.router.Decide(host)
	ev := s.newEvent("HTTP", host, port, decision)
	s.stats.Attempt(decision.Mode == router.ModeProxy)
	s.stats.Hit(decision.Matched)
	readBefore, writtenBefore := c.read.Load(), c.written.Load()

	clientClose := req.Close
	out := req.Clone(context.WithValue(ctx, decisionKey{}, decision))
	out.RequestURI = ""
	out.URL.Host = net.JoinHostPort(host.Name, port)
	out.Host = req.Host
	out.Close = false
	removeHopHeaders(out.Header)

	resp, err := s.httpRT.RoundTrip(out)
	if err != nil {
		s.stats.Fail()
		code := http.StatusBadGateway
		if isTimeout(err) {
			code = http.StatusGatewayTimeout
		}
		writeError(c, code, "RouteBox: upstream request failed")
		ev.State, ev.Error, ev.Duration = events.ConnFailed, err, time.Since(ev.Time)
		s.publish(ev)
		return false
	}
	defer resp.Body.Close()

	removeHopHeaders(resp.Header)
	chunked := len(resp.TransferEncoding) > 0 && resp.TransferEncoding[0] == "chunked"
	keepAlive := !clientClose && !(resp.ContentLength < 0 && !chunked && req.Method != http.MethodHead)
	resp.Close = !keepAlive
	writeErr := resp.Write(c)

	ev.State = events.ConnClosed
	ev.BytesOut = c.read.Load() - readBefore
	ev.BytesIn = c.written.Load() - writtenBefore
	ev.Duration = time.Since(ev.Time)
	if writeErr != nil {
		ev.Error = fmt.Errorf("write response: %w", writeErr)
	}
	s.publish(ev)
	return keepAlive && writeErr == nil
}

func (s *Server) dialForHTTP(ctx context.Context, _, addr string) (net.Conn, error) {
	host, port, err := router.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	d, ok := ctx.Value(decisionKey{}).(router.Decision)
	if !ok {
		d = s.router.Decide(host)
	}
	dialCtx, cancel := context.WithTimeout(ctx, s.opts.ConnectTimeout)
	defer cancel()
	return s.transport.Dial(dialCtx, d, host, port)
}
