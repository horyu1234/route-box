package core

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/socks/sockstest"
	"github.com/horyu1234/route-box/internal/ssh"
	"github.com/horyu1234/route-box/internal/ssh/sshtest"
)

// 테스트 바이너리가 가짜 ssh 역할도 한다: -D 주소에서 SOCKS 인사만 받고 SIGTERM 까지 기다린다.
func TestMain(m *testing.M) {
	if os.Getenv("ROUTEBOX_FAKE_SSH") == "ok" {
		os.Exit(fakeSSH(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeSSH(args []string) int {
	i := slices.Index(args, "-D")
	ln, err := net.Listen("tcp", args[i+1])
	if err != nil {
		return 255
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 3)
			if _, err := io.ReadFull(c, buf); err == nil {
				_, _ = c.Write([]byte{5, 0})
			}
			_ = c.Close()
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	<-sig
	return 0
}

func newApp(t *testing.T, mutate func(*config.Config)) *App {
	t.Helper()
	cfg := config.Default()
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	return New(Options{
		ConfigPath:     filepath.Join(t.TempDir(), "config.json"),
		Config:         cfg,
		ListenOverride: "127.0.0.1:0",
		HealthPeriod:   50 * time.Millisecond,
		SSHBin:         os.Args[0],
		SSHTiming:      ssh.Timing{ProbeInterval: 20 * time.Millisecond, ReadyTimeout: 5 * time.Second, BackoffMin: 50 * time.Millisecond, BackoffMax: 200 * time.Millisecond},
	})
}

func external(name, socks string) config.Upstream {
	return config.Upstream{Name: name, Mode: config.SSHExternal, Socks: socks}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func TestRouteMutationsPersistAndApply(t *testing.T) {
	a := newApp(t, func(c *config.Config) {
		c.Upstreams = []config.Upstream{external("seoul", "127.0.0.1:1080"), external("tokyo", "127.0.0.1:1081")}
	})
	r, err := a.AddRoute("https://WWW.Example.com:443/watch?v=1", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Domain != "www.example.com" || r.Mode != router.ModeProxy || r.Upstream != "seoul" {
		t.Fatalf("route = %+v, want pinned to the first upstream", r)
	}
	if _, err := a.AddRoute("www.example.com", ""); !errors.Is(err, ErrRouteExists) {
		t.Fatalf("duplicate add: %v", err)
	}
	if _, err := a.AddRoute("example.org", "nowhere"); !errors.Is(err, config.ErrUnknownUpstream) {
		t.Fatalf("unknown via accepted: %v", err)
	}
	host, _ := router.ParseHost("m.www.example.com")
	if d := a.router.Decide(host); d.Mode != router.ModeProxy || d.Upstream != "seoul" {
		t.Fatalf("router not updated: %+v", d)
	}

	if _, err := a.SetRouteVia("www.example.com", "tokyo"); err != nil {
		t.Fatal(err)
	}
	if d := a.router.Decide(host); d.Upstream != "tokyo" {
		t.Fatalf("via change not applied: %+v", d)
	}
	if _, err := a.UpdateRoute("www.example.com", "example.com", "direct"); err != nil {
		t.Fatal(err)
	}
	if d := a.router.Decide(host); d.Mode != router.ModeDirect || d.Matched != "example.com" {
		t.Fatalf("after update: %+v", d)
	}

	disk, err := config.Load(a.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.Routes) != 1 || disk.Routes[0] != (router.Route{Domain: "example.com", Mode: router.ModeDirect}) {
		t.Fatalf("disk routes = %+v", disk.Routes)
	}
	if _, err := a.RemoveRoute("EXAMPLE.com."); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RemoveRoute("example.com"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("second remove: %v", err)
	}
}

func TestRouteHitsFollowTheRouteList(t *testing.T) {
	a := newApp(t, func(c *config.Config) {
		c.Upstreams = []config.Upstream{external("seoul", "127.0.0.1:1080"), external("tokyo", "127.0.0.1:1081")}
	})
	for _, d := range []string{"example.com", "example.org"} {
		if _, err := a.AddRoute(d, ""); err != nil {
			t.Fatal(err)
		}
	}
	a.stats.Hit("example.com")
	a.stats.Hit("example.com")
	a.stats.Hit("example.org")
	a.stats.Hit("example.net") // 설정에 없는 route 는 Status 에 나오지 않는다

	if _, err := a.SetRouteVia("example.com", "tokyo"); err != nil {
		t.Fatal(err)
	}
	if got := a.Status().RouteHits; len(got) != 2 || got["example.com"] != 2 || got["example.org"] != 1 {
		t.Fatalf("hits after via change = %v", got)
	}
	if _, err := a.RemoveRoute("example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddRoute("example.com", ""); err != nil {
		t.Fatal(err)
	}
	if got := a.Status().RouteHits; len(got) != 1 || got["example.org"] != 1 {
		t.Fatalf("re-added route kept its old hits: %v", got)
	}
}

func TestFallbackFollowsUpstreamAndBlocksRemoval(t *testing.T) {
	a := newApp(t, func(c *config.Config) {
		c.Upstreams = []config.Upstream{external("seoul", "127.0.0.1:1080"), external("tokyo", "127.0.0.1:1081")}
	})
	host, _ := router.ParseHost("unmatched.example.net")
	if d := a.router.Decide(host); d.Mode != router.ModeDirect || a.Status().Fallback != "direct" {
		t.Fatalf("default fallback = %+v / %q, want direct", d, a.Status().Fallback)
	}
	for _, bad := range []string{"", "nowhere"} {
		if _, err := a.SetFallback(bad); err == nil {
			t.Fatalf("SetFallback(%q) accepted", bad)
		}
	}
	if via, err := a.SetFallback("Tokyo"); err != nil || via != "tokyo" {
		t.Fatalf("set: %q %v", via, err)
	}
	if d := a.router.Decide(host); d.Mode != router.ModeProxy || d.Upstream != "tokyo" || d.Matched != "" {
		t.Fatalf("decide = %+v", d)
	}
	if err := a.RemoveUpstream("tokyo"); !errors.Is(err, ErrUpstreamInUse) {
		t.Fatalf("removed the fallback upstream: %v", err)
	}
	u := external("osaka", "127.0.0.1:1081")
	if _, err := a.UpdateUpstream("tokyo", u); err != nil {
		t.Fatal(err)
	}
	if d := a.router.Decide(host); d.Upstream != "osaka" || a.Status().Fallback != "osaka" {
		t.Fatalf("fallback did not follow rename: %+v", d)
	}
	disk, err := config.Load(a.ConfigPath())
	if err != nil || disk.Fallback != "osaka" {
		t.Fatalf("disk fallback = %q, %v", disk.Fallback, err)
	}
	if via, err := a.SetFallback("direct"); err != nil || via != "direct" {
		t.Fatalf("back to direct: %q %v", via, err)
	}
	if d := a.router.Decide(host); d.Mode != router.ModeDirect {
		t.Fatalf("decide = %+v", d)
	}
	if err := a.RemoveUpstream("osaka"); err != nil {
		t.Fatal(err)
	}
}

func TestFallbackFromConfigAppliesAtStart(t *testing.T) {
	a := newApp(t, func(c *config.Config) {
		c.Upstreams = []config.Upstream{external("seoul", "127.0.0.1:1080")}
		c.Fallback = "seoul"
	})
	host, _ := router.ParseHost("unmatched.example.net")
	if d := a.router.Decide(host); d.Mode != router.ModeProxy || d.Upstream != "seoul" {
		t.Fatalf("decide = %+v", d)
	}
}

func TestRoutesAddedBeforeAnyUpstreamArePinnedLater(t *testing.T) {
	a := newApp(t, nil)
	if r, err := a.AddRoute("example.com", ""); err != nil || r.Upstream != "" {
		t.Fatalf("route = %+v, %v", r, err)
	}
	if _, err := a.AddUpstream(external("lab", "127.0.0.1:9050")); err != nil {
		t.Fatal(err)
	}
	if r := a.Routes()[0]; r.Upstream != "lab" {
		t.Fatalf("route not pinned to new upstream: %+v", r)
	}
	host, _ := router.ParseHost("example.com")
	if d := a.router.Decide(host); d.Upstream != "lab" {
		t.Fatalf("router still sees the unpinned route: %+v", d)
	}
}

func TestUpstreamLifecycle(t *testing.T) {
	a := newApp(t, func(c *config.Config) {
		c.Upstreams = []config.Upstream{external("seoul", "127.0.0.1:1080")}
		c.Routes = []router.Route{{Domain: "example.com", Mode: router.ModeProxy, Upstream: "seoul"}}
	})
	if _, err := a.AddUpstream(external("seoul", "127.0.0.1:1081")); !errors.Is(err, config.ErrDuplicateUpstream) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := a.AddUpstream(external("tokyo", "127.0.0.1:1080")); !errors.Is(err, config.ErrSocksInUse) {
		t.Fatalf("shared SOCKS address: %v", err)
	}
	if _, err := a.AddUpstream(external("direct", "127.0.0.1:1082")); err == nil {
		t.Fatal("reserved name accepted")
	}
	if err := a.RemoveUpstream("seoul"); !errors.Is(err, ErrUpstreamInUse) {
		t.Fatalf("remove in-use upstream: %v", err)
	}
	if _, err := a.UpdateUpstream("seoul", external("busan", "127.0.0.1:1080")); err != nil {
		t.Fatal(err)
	}
	if r := a.Routes()[0]; r.Upstream != "busan" {
		t.Fatalf("rename did not cascade: %+v", r)
	}
	if _, err := a.SetRouteVia("example.com", "direct"); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveUpstream("busan"); err != nil {
		t.Fatal(err)
	}
	if len(a.Upstreams()) != 0 {
		t.Fatal("upstream not removed")
	}
}

func TestAddPresetSkipsExisting(t *testing.T) {
	p := router.Presets()[len(router.Presets())-1]
	a := newApp(t, func(c *config.Config) {
		c.Upstreams = []config.Upstream{external("seoul", "127.0.0.1:1080"), external("tokyo", "127.0.0.1:1081")}
		c.Routes = []router.Route{{Domain: p.Domains[0], Mode: router.ModeDirect}}
	})
	added, err := a.AddPreset(p.Name, "tokyo")
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != len(p.Domains)-1 {
		t.Fatalf("added %d, want %d", len(added), len(p.Domains)-1)
	}
	for _, r := range added {
		if r.Upstream != "tokyo" {
			t.Fatalf("preset route %+v not sent via tokyo", r)
		}
	}
	if a.Routes()[0].Mode != router.ModeDirect {
		t.Fatal("preset overwrote an existing route")
	}
	if again, err := a.AddPreset(p.Name, "tokyo"); err != nil || len(again) != 0 {
		t.Fatalf("second preset add: %v, %v", again, err)
	}
}

func TestSocksOverrideAppliesToFirstUpstreamOnly(t *testing.T) {
	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{external("seoul", "127.0.0.1:1080"), external("tokyo", "127.0.0.1:1081")}
	path := filepath.Join(t.TempDir(), "config.json")
	a := New(Options{ConfigPath: path, Config: cfg, SocksOverride: "127.0.0.1:9999"})
	if got, _ := a.transport.SocksAddr("seoul"); got != "127.0.0.1:9999" {
		t.Fatalf("seoul socks = %q", got)
	}
	if got, _ := a.transport.SocksAddr("tokyo"); got != "127.0.0.1:1081" {
		t.Fatalf("tokyo socks = %q", got)
	}
	if _, err := a.AddRoute("example.com", ""); err != nil {
		t.Fatal(err)
	}
	disk, _ := config.Load(path)
	if disk.Upstreams[0].Socks != "127.0.0.1:1080" {
		t.Fatalf("override leaked into config: %q", disk.Upstreams[0].Socks)
	}
}

func TestSuggestions(t *testing.T) {
	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{external("proxy-seoul", "127.0.0.1:1080"), external("b", "127.0.0.1:1081")}
	if got := SuggestSocks(cfg, ""); got != "127.0.0.1:1082" {
		t.Errorf("SuggestSocks = %q", got)
	}
	if got := SuggestSocks(cfg, "proxy-seoul"); got != "127.0.0.1:1080" {
		t.Errorf("SuggestSocks while editing = %q", got)
	}
	for host, want := range map[string]string{
		"proxy-seoul":        "proxy-seoul-2",
		"vpn.example.com":    "vpn",
		"me@Tokyo.Example":   "tokyo",
		"10.0.0.5":           "10-0-0-5",
		"direct":             "upstream",
		"":                   "upstream",
		"__weird__host name": "weird__host-name",
	} {
		if got := SuggestName(cfg, host); got != want {
			t.Errorf("SuggestName(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestBadge(t *testing.T) {
	ok := UpstreamStatus{Configured: true, Mode: config.SSHExternal, Health: Health{Checked: time.Now(), Reachable: true}}
	bad := ok
	bad.Health.Reachable = false
	connecting := UpstreamStatus{Configured: true, Mode: config.SSHManaged, SSH: ssh.Status{State: ssh.StateReconnecting}}
	running := ProxyStatus{Running: true}
	for _, tc := range []struct {
		st   Status
		want Badge
	}{
		{Status{Proxy: running, Upstreams: []UpstreamStatus{ok, ok}}, BadgeConnected},
		{Status{Proxy: running, Upstreams: []UpstreamStatus{ok, bad}}, BadgeDegraded},
		{Status{Proxy: running, Upstreams: []UpstreamStatus{ok, connecting}}, BadgeReconnecting},
		{Status{Proxy: running}, BadgeDisconnected},
		{Status{Upstreams: []UpstreamStatus{ok}}, BadgeDisconnected},
	} {
		if got := badge(tc.st); got != tc.want {
			t.Errorf("badge = %s, want %s", got, tc.want)
		}
	}
}

func echo(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
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
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func startSocks(t *testing.T, target string) *sockstest.Server {
	t.Helper()
	srv, err := sockstest.Start(sockstest.DialTo(target))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func runApp(t *testing.T, a *App) (<-chan events.Event, string, func()) {
	t.Helper()
	sub, cancelSub := a.Subscribe(1024)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var addr string
	waitFor(t, sub, func(e events.Event) bool {
		ps, ok := e.(events.ProxyStarted)
		addr = ps.Addr
		return ok
	})
	stop := func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
		cancelSub()
	}
	return sub, addr, stop
}

func tunnel(t *testing.T, proxyAddr, host string) int {
	t.Helper()
	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT %s:443 HTTP/1.1\r\n\r\n", host)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func hostsSeen(s *sockstest.Server) string {
	var out []string
	for _, r := range s.Requests() {
		out = append(out, r.Host)
	}
	return strings.Join(out, ",")
}

func TestRunRoutesEachServiceThroughItsUpstream(t *testing.T) {
	target := echo(t)
	seoul, tokyo := startSocks(t, target), startSocks(t, target)
	a := newApp(t, func(c *config.Config) {
		c.Upstreams = []config.Upstream{external("seoul", seoul.Addr()), external("tokyo", tokyo.Addr())}
	})
	sub, proxyAddr, stop := runApp(t, a)
	defer stop()

	reachable := map[string]bool{}
	waitFor(t, sub, func(e events.Event) bool {
		if h, ok := e.(events.UpstreamHealth); ok && h.Reachable {
			reachable[h.Upstream] = true
		}
		return reachable["seoul"] && reachable["tokyo"]
	})
	if b := a.Status().Badge; b != BadgeConnected {
		t.Fatalf("badge = %s", b)
	}

	mustAdd := func(domain, via string) {
		if _, err := a.AddRoute(domain, via); err != nil {
			t.Fatal(err)
		}
	}
	mustAdd("example.com", "seoul")
	mustAdd("example.org", "tokyo")
	for _, h := range []string{"www.example.com", "api.example.org", "example.net"} {
		if code := tunnel(t, proxyAddr, h); code != http.StatusOK && h != "example.net" {
			t.Fatalf("%s: %d", h, code)
		}
	}
	if got := hostsSeen(seoul); got != "www.example.com" {
		t.Fatalf("seoul saw %q", got)
	}
	if got := hostsSeen(tokyo); got != "api.example.org" {
		t.Fatalf("tokyo saw %q", got)
	}

	if _, err := a.SetRouteVia("example.org", "seoul"); err != nil {
		t.Fatal(err)
	}
	tunnel(t, proxyAddr, "api.example.org")
	if got := hostsSeen(seoul); got != "www.example.com,api.example.org" {
		t.Fatalf("live via change not applied, seoul saw %q", got)
	}

	var ev events.ConnectionEvent
	for _, e := range a.RecentConnections() {
		if e.Host == "www.example.com" {
			ev = e
		}
	}
	if ev.Upstream != "seoul" || ev.Route != router.ModeProxy {
		t.Fatalf("connection log entry = %+v", ev)
	}
	st := a.Status()
	if u, _ := st.Upstream("seoul"); u.Routes != 2 {
		t.Fatalf("seoul routes = %d", u.Routes)
	}
}

func TestManagedUpstreamsStartAndStopAtRuntime(t *testing.T) {
	t.Setenv("ROUTEBOX_FAKE_SSH", "ok")
	a := newApp(t, nil)
	sub, _, stop := runApp(t, a)
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()

	for _, name := range []string{"alpha", "beta"} {
		if _, err := a.AddUpstream(config.Upstream{Name: name, Mode: config.SSHManaged, Host: name + "-host", Socks: freeAddr(t), Reconnect: true}); err != nil {
			t.Fatal(err)
		}
	}
	pids := map[string]int{}
	waitFor(t, sub, func(e events.Event) bool {
		if s, ok := e.(events.SSHStateChanged); ok && s.Status.State == ssh.StateConnected {
			pids[s.Upstream] = s.Status.PID
		}
		return pids["alpha"] > 0 && pids["beta"] > 0
	})
	if pids["alpha"] == pids["beta"] {
		t.Fatal("upstreams share one ssh process")
	}

	if err := a.RemoveUpstream("alpha"); err != nil {
		t.Fatal(err)
	}
	waitReaped(t, pids["alpha"])
	if err := syscall.Kill(pids["beta"], 0); err != nil {
		t.Fatalf("removing alpha stopped beta: %v", err)
	}

	stop()
	stopped = true
	waitReaped(t, pids["beta"])
}

func waitReaped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		// 좀비도 signal 0 은 받으므로 ESRCH 만이 회수됐다는 증거다.
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d not reaped", pid)
}

func TestRunFailsWhenPortBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	a := New(Options{ConfigPath: filepath.Join(t.TempDir(), "c.json"), Config: config.Default(), ListenOverride: ln.Addr().String()})
	if err := a.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded on a busy port")
	}
}

func TestWarningsForNonLoopback(t *testing.T) {
	a := New(Options{ConfigPath: filepath.Join(t.TempDir(), "c.json"), Config: config.Default(), ListenOverride: "0.0.0.0:8080"})
	if len(a.Warnings()) != 1 {
		t.Fatal("expected open-proxy warning")
	}
	if len(newApp(t, nil).Warnings()) != 0 {
		t.Fatal("unexpected warning for loopback")
	}
}

func waitFor(t *testing.T, ch <-chan events.Event, fn func(events.Event) bool) events.Event {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatal("event stream closed")
			}
			if fn(e) {
				return e
			}
		case <-timeout:
			t.Fatal("timed out waiting for event")
		}
	}
}

func TestSocksOverrideStaysWithItsUpstream(t *testing.T) {
	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{external("seoul", "127.0.0.1:1080"), external("tokyo", "127.0.0.1:1081")}
	a := New(Options{ConfigPath: filepath.Join(t.TempDir(), "config.json"), Config: cfg, SocksOverride: "127.0.0.1:9999"})
	if err := a.RemoveUpstream("seoul"); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.transport.SocksAddr("tokyo"); got != "127.0.0.1:1081" {
		t.Fatalf("override moved to tokyo after seoul was removed: %q", got)
	}
}

func TestMigratedConfigIsAnnounced(t *testing.T) {
	cfg := config.Default()
	cfg.Migrated = true
	a := New(Options{ConfigPath: filepath.Join(t.TempDir(), "config.json"), Config: cfg, ListenOverride: "127.0.0.1:0"})
	sub, _, stop := runApp(t, a)
	defer stop()
	waitFor(t, sub, func(e events.Event) bool {
		n, ok := e.(events.Notice)
		return ok && n.Message == MigratedNotice
	})
}

func TestHostKeyTrustRequiresTheScannedFingerprint(t *testing.T) {
	fake := sshtest.New(t, sshtest.Unknown)
	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{
		{Name: "seoul", Mode: config.SSHManaged, Host: "fake.example.net", Socks: "127.0.0.1:1080"},
		{Name: "lab", Mode: config.SSHExternal, Socks: "127.0.0.1:9050"},
	}
	app := New(Options{ConfigPath: filepath.Join(t.TempDir(), "config.json"), Config: cfg, SSHBin: fake.Bin})

	if _, err := app.ScanHostKey(context.Background(), "lab"); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("external upstream: %v", err)
	}
	if err := app.TrustHostKey("seoul", sshtest.Fingerprint); !errors.Is(err, ErrNoPendingKey) {
		t.Fatalf("trust before scan: %v", err)
	}
	k, err := app.ScanHostKey(context.Background(), "seoul")
	if err != nil || k.Fingerprint() != sshtest.Fingerprint {
		t.Fatalf("scan: %+v %v", k, err)
	}
	if err := app.TrustHostKey("seoul", "ssh-ed25519 SHA256:somethingelse"); !errors.Is(err, ErrNoPendingKey) {
		t.Fatalf("trust with another fingerprint: %v", err)
	}
	if err := app.TrustHostKey("seoul", sshtest.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(fake.KnownHosts); !strings.Contains(string(b), sshtest.Key) {
		t.Fatalf("known_hosts = %q", b)
	}
	if err := app.TrustHostKey("seoul", sshtest.Fingerprint); !errors.Is(err, ErrNoPendingKey) {
		t.Fatal("a scanned key must be trusted at most once")
	}
}
