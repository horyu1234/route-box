package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/router"
)

// shortDir 는 소켓 경로를 unix 소켓 길이 제한 아래로 유지한다.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestControlRoundTrip(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "routebox.sock")
	app := core.New(core.Options{ConfigPath: filepath.Join(dir, "config.json"), Config: config.Default()})

	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sock)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket perm: %v %v", info.Mode(), err)
	}
	if _, err := Listen(sock); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Listen: %v, want ErrAlreadyRunning", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, app) }()

	c := NewClient(sock)
	for _, u := range []config.Upstream{
		{Name: "seoul", Mode: config.SSHExternal, Socks: "127.0.0.1:1080"},
		{Name: "tokyo", Mode: config.SSHExternal, Socks: "127.0.0.1:1081"},
	} {
		if _, err := c.AddUpstream(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.AddUpstream(ctx, config.Upstream{Name: "osaka", Mode: config.SSHExternal, Socks: "127.0.0.1:1081"}); !errors.Is(err, config.ErrSocksInUse) {
		t.Fatalf("shared socks: %v", err)
	}
	r, err := c.AddRoute(ctx, "https://Mail.Example.com/x?y=1", "")
	if err != nil || r.Domain != "mail.example.com" || r.Upstream != "seoul" {
		t.Fatalf("add: %+v %v", r, err)
	}
	if _, err := c.AddRoute(ctx, "mail.example.com", ""); !errors.Is(err, core.ErrRouteExists) {
		t.Fatalf("dup add: %v", err)
	}
	if _, err := c.AddRoute(ctx, "bad domain", ""); !errors.Is(err, router.ErrInvalidHost) {
		t.Fatalf("invalid domain: %v", err)
	}
	if _, err := c.AddRoute(ctx, "example.org", "nowhere"); !errors.Is(err, config.ErrUnknownUpstream) {
		t.Fatalf("unknown via: %v", err)
	}
	if r, err := c.SetRouteVia(ctx, "MAIL.example.com", "tokyo"); err != nil || r.Upstream != "tokyo" {
		t.Fatalf("set via: %+v %v", r, err)
	}
	if err := c.RemoveUpstream(ctx, "tokyo"); !errors.Is(err, core.ErrUpstreamInUse) {
		t.Fatalf("remove in-use upstream: %v", err)
	}
	preset := router.Presets()[len(router.Presets())-1]
	added, err := c.AddPreset(ctx, preset.Name, "direct")
	if err != nil || len(added) != len(preset.Domains) || added[0].Mode != router.ModeDirect {
		t.Fatalf("preset: %v %v", added, err)
	}
	if _, err := c.AddPreset(ctx, "nope", ""); !errors.Is(err, router.ErrUnknownPreset) {
		t.Fatalf("unknown preset: %v", err)
	}
	ups, err := c.Upstreams(ctx)
	if err != nil || len(ups) != 2 {
		t.Fatalf("upstreams: %v %v", ups, err)
	}
	infos, err := c.SSH(ctx, "")
	if err != nil || len(infos) != 2 {
		t.Fatalf("ssh info: %v %v", infos, err)
	}
	if _, err := c.SSH(ctx, "missing"); !errors.Is(err, core.ErrUpstreamMissing) {
		t.Fatalf("ssh info for missing upstream: %v", err)
	}
	routes, err := c.Routes(ctx)
	if err != nil || len(routes) != len(app.Routes()) {
		t.Fatalf("routes: %v %v", routes, err)
	}
	if _, err := c.RemoveRoute(ctx, "mail.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RemoveRoute(ctx, "mail.example.com"); !errors.Is(err, core.ErrRouteNotFound) {
		t.Fatalf("second remove: %v", err)
	}
	if via, err := c.SetFallback(ctx, "SEOUL"); err != nil || via != "seoul" {
		t.Fatalf("set fallback: %q %v", via, err)
	}
	if _, err := c.SetFallback(ctx, "nowhere"); !errors.Is(err, config.ErrUnknownUpstream) {
		t.Fatalf("unknown fallback: %v", err)
	}
	st, err := c.Status(ctx)
	if err != nil || st.Routes != len(app.Routes()) || st.Badge != core.BadgeDisconnected || st.Fallback != "seoul" {
		t.Fatalf("status: %+v %v", st, err)
	}
	if err := c.RestartSSH(ctx, ""); !errors.Is(err, core.ErrNotRunning) {
		t.Fatalf("restart while proxy is not running: %v", err)
	}

	disk, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil || len(disk.Routes) != len(app.Routes()) {
		t.Fatalf("control changes not persisted: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("socket not removed on shutdown")
	}
	if _, err := NewClient(sock).Status(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("client after shutdown: %v", err)
	}
}

func TestListenReplacesStaleSocket(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "routebox.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(sock)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	_ = ln.Close()
}
