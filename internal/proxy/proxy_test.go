package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/socks/sockstest"
	"github.com/horyu1234/route-box/internal/stats"
)

type harness struct {
	addr     string
	socks    *sockstest.Server
	resolves *atomic.Int64
	stats    *stats.Stats
	router   *router.Router
	cancel   context.CancelFunc
	done     chan error

	mu     sync.Mutex
	events []events.ConnectionEvent
}

func (h *harness) connEvents() []events.ConnectionEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]events.ConnectionEvent(nil), h.events...)
}

// waitEvent 는 fn 에 매칭되는 이벤트를 폴링한다; 이벤트는 클라이언트가
// 응답을 관측하는 시점과 비동기로 발행된다.
func (h *harness) waitEvent(t *testing.T, fn func(events.ConnectionEvent) bool) events.ConnectionEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range h.connEvents() {
			if fn(e) {
				return e
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("event not observed; got %+v", h.connEvents())
	return events.ConnectionEvent{}
}

type harnessOpt func(*Options)

// newHarness 는 DIRECT dialer 가 조회 횟수를 세면서 항상 실패하는 리졸버를
// 쓰고, SOCKS upstream 은 요청한 이름과 무관하게 모든 요청을 target 으로
// relay 하는 프록시를 띄운다.
func newHarness(t *testing.T, target string, routes []router.Route, opts ...harnessOpt) *harness {
	t.Helper()
	h := &harness{resolves: &atomic.Int64{}, stats: &stats.Stats{}}
	if target != "" {
		srv, err := sockstest.Start(sockstest.DialTo(target))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = srv.Close() })
		h.socks = srv
	}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			h.resolves.Add(1)
			return nil, errors.New("local DNS is disabled in this test")
		},
	}
	socksAddr := ""
	if h.socks != nil {
		socksAddr = h.socks.Addr()
	}
	o := DefaultOptions()
	o.ConnectTimeout = 2 * time.Second
	o.ShutdownGrace = 200 * time.Millisecond
	for _, fn := range opts {
		fn(&o)
	}
	h.router = router.New(withUpstream(routes))
	tr := NewTransport(&net.Dialer{Resolver: resolver}, map[string]string{"up": socksAddr})
	srv := NewServer(h.router, tr, h.stats, func(e events.Event) {
		if ce, ok := e.(events.ConnectionEvent); ok {
			h.mu.Lock()
			h.events = append(h.events, ce)
			h.mu.Unlock()
		}
	}, o)
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.addr = ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Error("server did not shut down")
		}
	})
	return h
}

// withUpstream 은 via 가 비어 있는 proxy route 를 하네스의 업스트림 "up" 으로 보낸다.
func withUpstream(routes []router.Route) []router.Route {
	out := make([]router.Route, len(routes))
	for i, r := range routes {
		if r.Mode == router.ModeProxy && r.Upstream == "" {
			r.Upstream = "up"
		}
		out[i] = r
	}
	return out
}

// echoServer 는 클라이언트가 half-close 할 때까지 echo 하다가 요약 줄을
// 덧붙이고 닫아, 양방향 half-close 를 검증한다.
func echoServer(t *testing.T, network, addr string) string {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Skipf("listen %s %s: %v", network, addr, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				n, _ := io.Copy(c, c)
				_, _ = fmt.Fprintf(c, "|eof after %d", n)
			}()
		}
	}()
	return ln.Addr().String()
}

// connect 는 CONNECT 를 수행하며 payload 를 request 헤더와 같은 write 로
// 보낸다, 브라우저가 TLS ClientHello 를 파이프라이닝하는 것처럼.
func connect(t *testing.T, proxyAddr, target, payload string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic c2VjcmV0\r\n\r\n%s", target, target, payload)
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return c, br, resp
}

func readAll(t *testing.T, c net.Conn, br *bufio.Reader) string {
	t.Helper()
	if err := c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestConnectProxyPassesHostnameToSocksWithoutLocalDNS(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	h := newHarness(t, target, []router.Route{
		{Domain: "example.com", Mode: router.ModeProxy},
		{Domain: "routebox-test.invalid", Mode: router.ModeProxy},
	})

	for _, tc := range []struct{ target, host string }{
		{"www.example.com:443", "www.example.com"},
		{"WWW.Example.COM.:443", "www.example.com"},
		// 로컬에서는 어디서도 resolve 되지 않는 이름: resolution 이 원격에서
		// 일어나야만 성공한다.
		{"deep.routebox-test.invalid:443", "deep.routebox-test.invalid"},
	} {
		c, br, resp := connect(t, h.addr, tc.target, "hello")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", tc.target, resp.StatusCode)
		}
		if got := readAll(t, c, br); got != "hello|eof after 5" {
			t.Fatalf("%s: tunnel data = %q", tc.target, got)
		}
		reqs := h.socks.Requests()
		last := reqs[len(reqs)-1]
		if last.Atyp != 0x03 || last.Host != tc.host || last.Port != 443 {
			t.Fatalf("%s: SOCKS saw %+v, want domain %q:443", tc.target, last, tc.host)
		}
	}
	if n := h.resolves.Load(); n != 0 {
		t.Fatalf("local resolver was called %d times for PROXY routes", n)
	}
	e := h.waitEvent(t, func(e events.ConnectionEvent) bool {
		return e.Host == "www.example.com" && e.State == events.ConnClosed
	})
	if e.Route != router.ModeProxy || e.Port != "443" || e.Matched != "example.com" {
		t.Fatalf("event = %+v", e)
	}
}

// "*." route 는 서브도메인만 업스트림으로 보내고, 도메인 자체는 다른 route 를 따른다.
func TestWildcardRouteProxiesSubdomainsOnly(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	h := newHarness(t, target, []router.Route{{Domain: "*.routebox-test.invalid", Mode: router.ModeProxy}})

	c, br, resp := connect(t, h.addr, "www.routebox-test.invalid:443", "w")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	readAll(t, c, br)
	if r := h.socks.Requests(); len(r) != 1 || r[0].Atyp != 0x03 || r[0].Host != "www.routebox-test.invalid" {
		t.Fatalf("socks requests = %+v", r)
	}
	// 도메인 자체는 매칭되지 않아 기본 DIRECT 로 가고, 로컬에서 resolve 되지 않으니 실패한다.
	_, _, resp = connect(t, h.addr, "routebox-test.invalid:443", "")
	if resp.StatusCode != http.StatusBadGateway || len(h.socks.Requests()) != 1 {
		t.Fatalf("apex: status %d, socks %+v", resp.StatusCode, h.socks.Requests())
	}
	if hits := h.stats.Hits(); len(hits) != 1 || hits["*.routebox-test.invalid"] != 1 || h.stats.Unmatched() != 1 {
		t.Fatalf("hits = %v, unmatched %d", hits, h.stats.Unmatched())
	}
}

// fallback 업스트림으로 가는 연결도 proxy route 와 똑같이 이름을 SOCKS 에 넘기고,
// 업스트림이 죽으면 DIRECT 로 새지 않고 502 로 끝나야 한다.
func TestFallbackUpstreamPassesHostnameAndFailsClosed(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	h := newHarness(t, target, []router.Route{{Domain: "example.com", Mode: router.ModeDirect}})
	h.router.SetFallback("up")

	c, br, resp := connect(t, h.addr, "unmatched.routebox-test.invalid:443", "hi")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := readAll(t, c, br); got != "hi|eof after 2" {
		t.Fatalf("tunnel data = %q", got)
	}
	if r := h.socks.Requests(); len(r) != 1 || r[0].Atyp != 0x03 || r[0].Host != "unmatched.routebox-test.invalid" {
		t.Fatalf("socks requests = %+v", r)
	}
	e := h.waitEvent(t, func(e events.ConnectionEvent) bool { return e.Host == "unmatched.routebox-test.invalid" })
	if e.Route != router.ModeProxy || e.Upstream != "up" || e.Matched != "" {
		t.Fatalf("event = %+v", e)
	}
	if h.stats.Unmatched() != 1 {
		t.Fatalf("unmatched = %d", h.stats.Unmatched())
	}

	_ = h.socks.Close()
	_, _, resp = connect(t, h.addr, "other.routebox-test.invalid:443", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status with fallback upstream down = %d, want 502", resp.StatusCode)
	}
	if n := h.resolves.Load(); n != 0 {
		t.Fatalf("local resolver was called %d times for fallback traffic", n)
	}
}

// Positive control: 카운팅 리졸버가 실제로 DIRECT 경로에 걸려 있다는 것을
// 증명한다, 그래야 위의 0 이라는 카운트가 "hook 이 안 걸렸다"가 아니라
// "resolve 되지 않았다"를 의미한다.
func TestDirectHostnameUsesLocalResolver(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	h := newHarness(t, target, []router.Route{{Domain: "example.com", Mode: router.ModeProxy}})
	_, _, resp := connect(t, h.addr, "direct.routebox.test:443", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (resolver is rigged to fail)", resp.StatusCode)
	}
	if h.resolves.Load() == 0 {
		t.Fatal("DIRECT hostname did not go through the local resolver")
	}
	if len(h.socks.Requests()) != 0 {
		t.Fatal("DIRECT route reached the SOCKS server")
	}
	if h.stats.Snapshot().Failed != 1 {
		t.Fatalf("failed = %d", h.stats.Snapshot().Failed)
	}
}

func TestConnectDirectRelayAndHalfClose(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	h := newHarness(t, target, nil)
	c, br, resp := connect(t, h.addr, target, "ping")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := readAll(t, c, br); got != "ping|eof after 4" {
		t.Fatalf("got %q", got)
	}
	if len(h.socks.Requests()) != 0 {
		t.Fatal("DIRECT route used SOCKS")
	}
	e := h.waitEvent(t, func(e events.ConnectionEvent) bool { return e.State == events.ConnClosed })
	if e.Route != router.ModeDirect || e.BytesOut != 4 || e.BytesIn != int64(len("ping|eof after 4")) {
		t.Fatalf("event = %+v", e)
	}
	st := h.stats.Snapshot()
	if st.Direct != 1 || st.Proxied != 0 || st.TX == 0 || st.RX == 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestConnectIPv6Literal(t *testing.T) {
	target := echoServer(t, "tcp6", "[::1]:0")
	h := newHarness(t, target, nil)
	c, br, resp := connect(t, h.addr, target, "v6")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := readAll(t, c, br); got != "v6|eof after 2" {
		t.Fatalf("got %q", got)
	}
	if h.resolves.Load() != 0 {
		t.Fatal("IP literal triggered a DNS lookup")
	}
}

func TestConnectHostWithoutPortDefaultsTo443(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	h := newHarness(t, target, []router.Route{{Domain: "noport.example", Mode: router.ModeProxy}})
	_, _, resp := connect(t, h.addr, "noport.example", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if r := h.socks.Requests(); len(r) != 1 || r[0].HostPort() != "noport.example:443" {
		t.Fatalf("requests = %+v", r)
	}
}

func TestProxyRouteFailsClosedWhenSocksIsDown(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	h := newHarness(t, target, []router.Route{{Domain: "example.com", Mode: router.ModeProxy}})
	_ = h.socks.Close()
	_, _, resp := connect(t, h.addr, "www.example.com:443", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if h.resolves.Load() != 0 {
		t.Fatal("fell back to local resolution")
	}
	e := h.waitEvent(t, func(e events.ConnectionEvent) bool { return e.State == events.ConnFailed })
	if e.Error == nil || e.Route != router.ModeProxy {
		t.Fatalf("event = %+v", e)
	}
	// 실패한 연결도 매칭된 route 의 hit 이다; 서브도메인은 그 route 로 센다.
	if hits := h.stats.Hits(); len(hits) != 1 || hits["example.com"] != 1 {
		t.Fatalf("hits = %v", hits)
	}
}

func TestHitsKeyedByConfiguredRouteDomain(t *testing.T) {
	target := echoServer(t, "tcp6", "[::1]:0")
	_, port, _ := net.SplitHostPort(target)
	v6, err := router.NewRoute("[::1]", "direct")
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, target, []router.Route{v6})
	for range 2 {
		c, br, resp := connect(t, h.addr, "[0:0::1]:"+port, "x")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		readAll(t, c, br)
	}
	if hits := h.stats.Hits(); len(hits) != 1 || hits[v6.Domain] != 2 {
		t.Fatalf("hits = %v, want %s: 2", hits, v6.Domain)
	}
}

func TestRouteChangeAppliesToNextConnection(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(target)
	h := newHarness(t, target, nil)
	hostport := "127.0.0.1:" + port

	c, br, _ := connect(t, h.addr, hostport, "a")
	readAll(t, c, br)
	if len(h.socks.Requests()) != 0 {
		t.Fatal("expected DIRECT before route added")
	}
	h.router.SetRoutes(withUpstream([]router.Route{{Domain: "127.0.0.1", Mode: router.ModeProxy}}))
	c, br, _ = connect(t, h.addr, hostport, "b")
	readAll(t, c, br)
	if r := h.socks.Requests(); len(r) != 1 || r[0].Atyp != 0x01 {
		t.Fatalf("expected PROXY after route added, got %+v", r)
	}
}

func TestMalformedRequests(t *testing.T) {
	h := newHarness(t, "", nil)
	for _, raw := range []string{
		"GARBAGE\r\n\r\n",
		"CONNECT bad host:443 HTTP/1.1\r\n\r\n",
		"CONNECT example.com:99999 HTTP/1.1\r\n\r\n",
		"CONNECT [::1 HTTP/1.1\r\n\r\n",
		"GET /relative HTTP/1.1\r\nHost: x\r\n\r\n",
	} {
		c, err := net.Dial("tcp", h.addr)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = c.Write([]byte(raw))
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", raw, resp.StatusCode)
		}
		_ = c.Close()
	}
}

func TestClientAbruptDisconnect(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	h := newHarness(t, target, nil)
	c, _, _ := connect(t, h.addr, target, "x")
	_ = c.(*net.TCPConn).SetLinger(0)
	_ = c.Close()
	h.waitEvent(t, func(e events.ConnectionEvent) bool { return e.State == events.ConnClosed })
	waitActive(t, h.stats, 0)
}

func TestUpstreamAbruptDisconnect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.(*net.TCPConn).SetLinger(0)
			_ = c.Close()
		}
	}()
	h := newHarness(t, "", nil)
	c, br, resp := connect(t, h.addr, ln.Addr().String(), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	_, _ = io.ReadAll(br)
	_ = c.Close()
	h.waitEvent(t, func(e events.ConnectionEvent) bool { return e.State == events.ConnClosed })
	waitActive(t, h.stats, 0)
}

func TestIdleTimeoutClosesTunnel(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	h := newHarness(t, target, nil, func(o *Options) { o.IdleTimeout = 200 * time.Millisecond })
	_, br, resp := connect(t, h.addr, target, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	start := time.Now()
	if _, err := io.ReadAll(br); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("idle tunnel closed after %v", d)
	}
}

func TestGracefulShutdownClosesTunnels(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	h := newHarness(t, target, nil)
	_, br, _ := connect(t, h.addr, target, "")
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
		h.done <- nil
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return")
	}
	if _, err := io.ReadAll(br); err != nil {
		t.Fatalf("tunnel not closed cleanly: %v", err)
	}
	if _, err := net.DialTimeout("tcp", h.addr, 200*time.Millisecond); err == nil {
		t.Fatal("listener still accepting after shutdown")
	}
	waitActive(t, h.stats, 0)
}

func TestListenPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := Listen(ln.Addr().String()); !errors.Is(err, ErrAddrInUse) {
		t.Fatalf("err = %v, want ErrAddrInUse", err)
	}
}

func TestPlainHTTPDirectAndProxy(t *testing.T) {
	var gotAuth atomic.Value
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Proxy-Authorization") + "|" + r.Header.Get("Proxy-Connection"))
		_, _ = fmt.Fprintf(w, "host=%s path=%s", r.Host, r.URL.RequestURI())
	}))
	defer web.Close()
	webAddr := strings.TrimPrefix(web.URL, "http://")
	h := newHarness(t, webAddr, []router.Route{{Domain: "web.proxied", Mode: router.ModeProxy}})

	proxyURL, _ := url.Parse("http://" + h.addr)
	client := &http.Client{Transport: &http.Transport{
		Proxy:              http.ProxyURL(proxyURL),
		ProxyConnectHeader: http.Header{},
	}, Timeout: 5 * time.Second}

	for _, tc := range []struct {
		url, wantBody string
		mode          router.Mode
	}{
		{web.URL + "/a?token=secret", "host=" + webAddr + " path=/a?token=secret", router.ModeDirect},
		{"http://web.proxied/b?q=1", "host=web.proxied path=/b?q=1", router.ModeProxy},
	} {
		req, _ := http.NewRequest(http.MethodGet, tc.url, nil)
		req.Header.Set("Proxy-Authorization", "Basic c2VjcmV0")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != tc.wantBody {
			t.Fatalf("%s: %d %q", tc.url, resp.StatusCode, body)
		}
		if a := gotAuth.Load().(string); a != "|" {
			t.Fatalf("hop-by-hop headers forwarded: %q", a)
		}
		h.waitEvent(t, func(e events.ConnectionEvent) bool { return e.Method == "HTTP" && e.Route == tc.mode })
	}
	if r := h.socks.Requests(); len(r) != 1 || r[0].HostPort() != "web.proxied:80" || r[0].Atyp != 0x03 {
		t.Fatalf("socks requests = %+v", r)
	}
	if h.resolves.Load() != 0 {
		t.Fatal("plain HTTP PROXY route used the local resolver")
	}
	// 매칭되지 않은 기본 DIRECT 는 세지 않고, dialForHTTP 가 다시 Decide 해도 두 번 세지 않는다.
	if hits := h.stats.Hits(); len(hits) != 1 || hits["web.proxied"] != 1 {
		t.Fatalf("hits = %v", hits)
	}
	for _, e := range h.connEvents() {
		if strings.Contains(fmt.Sprintf("%+v", e), "secret") || strings.Contains(e.Host, "?") {
			t.Fatalf("event leaks request details: %+v", e)
		}
	}
}

func TestConcurrentTunnels(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	h := newHarness(t, target, []router.Route{{Domain: "example.com", Mode: router.ModeProxy}})
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dest := target
			if i%2 == 0 {
				dest = "s" + strconv.Itoa(i) + ".example.com:443"
			}
			c, err := net.Dial("tcp", h.addr)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			payload := strings.Repeat("x", 1000+i)
			fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\n\r\n%s", dest, payload)
			br := bufio.NewReader(c)
			resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
			if err != nil || resp.StatusCode != 200 {
				t.Errorf("connect %s: %v", dest, err)
				return
			}
			_ = c.(*net.TCPConn).CloseWrite()
			b, _ := io.ReadAll(br)
			if want := payload + "|eof after " + strconv.Itoa(len(payload)); string(b) != want {
				t.Errorf("%s: got %d bytes", dest, len(b))
			}
		}()
	}
	wg.Wait()
	st := h.stats.Snapshot()
	if st.Proxied != 15 || st.Direct != 15 {
		t.Fatalf("stats = %+v", st)
	}
	waitActive(t, h.stats, 0)
}

func waitActive(t *testing.T, st *stats.Stats, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st.Snapshot().Active == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("active = %d, want %d", st.Snapshot().Active, want)
}

func TestRoutesUseTheirOwnUpstream(t *testing.T) {
	target := echoServer(t, "tcp", "127.0.0.1:0")
	seoul, err := sockstest.Start(sockstest.DialTo(target))
	if err != nil {
		t.Fatal(err)
	}
	defer seoul.Close()
	tokyo, err := sockstest.Start(sockstest.DialTo(target))
	if err != nil {
		t.Fatal(err)
	}
	defer tokyo.Close()

	var mu sync.Mutex
	var evs []events.ConnectionEvent
	r := router.New([]router.Route{
		{Domain: "example.com", Mode: router.ModeProxy, Upstream: "seoul"},
		{Domain: "example.org", Mode: router.ModeProxy, Upstream: "tokyo"},
		{Domain: "example.net", Mode: router.ModeProxy, Upstream: "gone"},
	})
	tr := NewTransport(nil, map[string]string{"seoul": seoul.Addr(), "tokyo": tokyo.Addr()})
	o := DefaultOptions()
	o.ShutdownGrace = 100 * time.Millisecond
	srv := NewServer(r, tr, &stats.Stats{}, func(e events.Event) {
		if ce, ok := e.(events.ConnectionEvent); ok {
			mu.Lock()
			evs = append(evs, ce)
			mu.Unlock()
		}
	}, o)
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }()

	for _, host := range []string{"a.example.com", "b.example.org", "example.com"} {
		c, br, resp := connect(t, ln.Addr().String(), host+":443", "x")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", host, resp.StatusCode)
		}
		readAll(t, c, br)
	}
	_, _, resp := connect(t, ln.Addr().String(), "example.net:443", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("unknown upstream: status %d, want 502", resp.StatusCode)
	}

	hosts := func(s *sockstest.Server) string {
		var out []string
		for _, r := range s.Requests() {
			out = append(out, r.Host)
		}
		return strings.Join(out, ",")
	}
	if got := hosts(seoul); got != "a.example.com,example.com" {
		t.Fatalf("seoul saw %v", got)
	}
	if got := hosts(tokyo); got != "b.example.org" {
		t.Fatalf("tokyo saw %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	byHost := map[string]string{}
	for _, e := range evs {
		byHost[e.Host] = e.Upstream
	}
	if byHost["a.example.com"] != "seoul" || byHost["b.example.org"] != "tokyo" {
		t.Fatalf("events carry wrong upstream: %v", byHost)
	}
}
