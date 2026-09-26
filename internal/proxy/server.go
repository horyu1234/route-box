// Package proxy 는 로컬 HTTP 프록시를 구현한다: CONNECT 터널(주 경로)과
// plain-HTTP forwarding. TLS 를 종단하거나 들여다보는 일은 절대 없다.
package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/stats"
)

type Options struct {
	ConnectTimeout time.Duration // SOCKS 핸드셰이크를 포함한 upstream dial
	IdleTimeout    time.Duration // 트래픽 없는 터널/keep-alive 연결의 유휴 허용 시간
	HeaderTimeout  time.Duration // 첫 요청 라인과 헤더를 읽는 시간
	ShutdownGrace  time.Duration // 종료 시 처리 중인 연결에 주는 유예 시간
}

func DefaultOptions() Options {
	return Options{
		ConnectTimeout: 15 * time.Second,
		IdleTimeout:    5 * time.Minute,
		HeaderTimeout:  15 * time.Second,
		ShutdownGrace:  time.Second,
	}
}

type Server struct {
	router    *router.Router
	transport *Transport
	stats     *stats.Stats
	publish   func(events.Event)
	opts      Options
	httpRT    *http.Transport
	nextID    atomic.Uint64
}

// NewServer 는 proxy 를 조립한다. publish 는 nil 이어도 된다.
func NewServer(r *router.Router, t *Transport, st *stats.Stats, publish func(events.Event), opts Options) *Server {
	if publish == nil {
		publish = func(events.Event) {}
	}
	if st == nil {
		st = &stats.Stats{}
	}
	s := &Server{router: r, transport: t, stats: st, publish: publish, opts: opts}
	s.httpRT = &http.Transport{
		Proxy:                 nil,
		DialContext:           s.dialForHTTP,
		DisableKeepAlives:     true,
		DisableCompression:    true,
		ResponseHeaderTimeout: 0,
		ForceAttemptHTTP2:     false,
	}
	return s
}

// ErrAddrInUse 는 다른 프로세스가 그 포트를 점유 중일 때 Listen 이 반환한다.
var ErrAddrInUse = errors.New("address already in use")

// Listen 은 프록시 리스너를 열고, 포트가 이미 쓰이는 중이면 명확한 에러를 낸다.
func Listen(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, fmt.Errorf("listen %s: %w (is another RouteBox or proxy running?)", addr, ErrAddrInUse)
		}
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	return ln, nil
}

// Serve 는 ctx 가 취소될 때까지 연결을 받다가, 이후 accept 를 멈추고
// 처리 중인 연결에는 ShutdownGrace 만큼 마무리 시간을 준 뒤 나머지를 닫고
// 모든 핸들러 goroutine 이 끝날 때까지 기다린다.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	connCtx, cancelConns := context.WithCancel(context.Background())
	defer cancelConns()
	var wg sync.WaitGroup

	stopAccept := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stopAccept()

	var acceptErr error
	var backoff time.Duration
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, os.ErrDeadlineExceeded) {
				backoff = min(max(backoff*2, 5*time.Millisecond), time.Second)
				time.Sleep(backoff)
				continue
			}
			acceptErr = fmt.Errorf("accept: %w", err)
			break
		}
		backoff = 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleConn(connCtx, c)
		}()
	}
	_ = ln.Close()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(s.opts.ShutdownGrace):
		cancelConns()
		<-done
	}
	return acceptErr
}

func (s *Server) handleConn(ctx context.Context, raw net.Conn) {
	s.stats.Open()
	defer s.stats.Close()
	c := &countingConn{Conn: raw, stats: s.stats}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()

	br := bufio.NewReader(c)
	first := true
	for {
		timeout := s.opts.IdleTimeout
		if first {
			timeout = s.opts.HeaderTimeout
		}
		_ = c.SetReadDeadline(time.Now().Add(timeout))
		req, err := http.ReadRequest(br)
		if err != nil {
			if !first || isClosedOrTimeout(err) {
				return
			}
			writeError(c, http.StatusBadRequest, "malformed request")
			return
		}
		_ = c.SetReadDeadline(time.Time{})
		first = false

		if req.Method == http.MethodConnect {
			s.handleConnect(ctx, c, br, req)
			return
		}
		if !s.handleHTTP(ctx, c, req) {
			return
		}
	}
}

func isClosedOrTimeout(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func writeError(w io.Writer, code int, msg string) {
	body := msg + "\n"
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(body), body)
}

func (s *Server) newEvent(method string, host router.Host, port string, d router.Decision) events.ConnectionEvent {
	return events.ConnectionEvent{
		ID:       s.nextID.Add(1),
		Time:     time.Now(),
		Method:   method,
		Host:     host.Name,
		Port:     port,
		Route:    d.Mode,
		Matched:  d.Matched,
		Upstream: d.Upstream,
		State:    events.ConnOpen,
	}
}

// countingConn 은 바이트가 흐를 때마다 전역 RX/TX 카운터를 갱신하고
// 연결별 누적치도 함께 유지한다.
type countingConn struct {
	net.Conn
	stats     *stats.Stats
	read      atomic.Int64
	written   atomic.Int64
	closeOnce sync.Once
	closeErr  error
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.read.Add(int64(n))
		c.stats.AddTX(n)
	}
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.written.Add(int64(n))
		c.stats.AddRX(n)
	}
	return n, err
}

func (c *countingConn) CloseWrite() error {
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *countingConn) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.Conn.Close() })
	return c.closeErr
}
