package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/ssh"
	"github.com/horyu1234/route-box/internal/tui/i18n"
)

func newModel(t *testing.T, mutate func(*config.Config), opts ...func(*Options)) Model {
	t.Helper()
	cfg := config.Default()
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	app := core.New(core.Options{ConfigPath: filepath.Join(t.TempDir(), "config.json"), Config: cfg})
	o := Options{App: app, Start: func() {}, Shutdown: func() {}, Lang: "en"}
	for _, fn := range opts {
		fn(&o)
	}
	m := New(o)
	t.Cleanup(m.unsub)
	return resize(m, 120, 36)
}

func twoUpstreams(c *config.Config) {
	c.Upstreams = []config.Upstream{
		{Name: "seoul", Mode: config.SSHManaged, Host: "proxy-seoul", Socks: "127.0.0.1:1080", Reconnect: true},
		{Name: "lab", Mode: config.SSHExternal, Socks: "127.0.0.1:9050"},
	}
}

func resize(m Model, w, h int) Model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(Model)
}

func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func typeText(m Model, s string) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return next.(Model)
}

func plain(s string) string { return ansi.Strip(s) }

func assertFits(t *testing.T, m Model, label string) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) > m.height {
		t.Errorf("%s %dx%d: view has %d lines", label, m.width, m.height, len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > m.width {
			t.Errorf("%s %dx%d: line %d is %d cells wide: %q", label, m.width, m.height, i, w, plain(l))
		}
	}
}

func TestOnboardingShownWithoutUpstreams(t *testing.T) {
	m := newModel(t, nil)
	if m.modal != modalOnboard {
		t.Fatalf("modal = %v, want onboarding", m.modal)
	}
	v := plain(m.View())
	for _, want := range []string{"first upstream", "Managed SSH", "External SOCKS"} {
		if !strings.Contains(v, want) {
			t.Errorf("onboarding view missing %q", want)
		}
	}
}

func TestOnboardingExternalCreatesUpstream(t *testing.T) {
	m := newModel(t, nil)
	m = press(t, m, "down", "enter", "enter")
	ups := m.app.Config().Upstreams
	if len(ups) != 1 || ups[0].Mode != config.SSHExternal || ups[0].Socks != config.DefaultSocks {
		t.Fatalf("upstreams = %+v", ups)
	}
	if m.onboard.step != onboardWaiting || m.onboard.name != ups[0].Name {
		t.Fatalf("onboard = %+v", m.onboard)
	}
	next, _ := m.Update(eventMsg{events.UpstreamHealth{Time: time.Now(), Upstream: ups[0].Name, Err: errors.New("connection refused")}})
	m = next.(Model)
	if m.onboard.step != onboardResult || m.onboard.ok || !strings.Contains(plain(m.View()), "connection refused") {
		t.Fatalf("expected failure result, got %+v", m.onboard)
	}
}

func TestOnboardingManagedNamesUpstreamFromHost(t *testing.T) {
	m := newModel(t, nil)
	m = press(t, m, "enter", "enter")
	if m.onboard.form.Err == "" {
		t.Fatal("empty host accepted")
	}
	m = typeText(m, "vpn.example.net")
	m = press(t, m, "enter")
	ups := m.app.Config().Upstreams
	if len(ups) != 1 || ups[0].Name != "vpn" || ups[0].Host != "vpn.example.net" || ups[0].Port != 0 {
		t.Fatalf("upstreams = %+v", ups)
	}
}

func TestAddRouteWithVia(t *testing.T) {
	m := newModel(t, twoUpstreams)
	m = press(t, m, "a")
	if m.modal != modalAddRoute {
		t.Fatalf("modal = %v", m.modal)
	}
	m = typeText(m, "https://WWW.Example.com:443/watch?v=123")
	m = press(t, m, "tab", "right", "enter")
	if m.modal != modalNone {
		t.Fatalf("modal still open: %v (err %q)", m.modal, m.form.Err)
	}
	routes := m.app.Routes()
	if len(routes) != 1 || routes[0] != (router.Route{Domain: "www.example.com", Mode: router.ModeProxy, Upstream: "lab"}) {
		t.Fatalf("routes = %+v", routes)
	}
	v := plain(m.View())
	if !strings.Contains(v, "Route added: www.example.com → lab") || !strings.Contains(v, "→ lab") {
		t.Errorf("toast or via tag missing:\n%s", v)
	}
	if strings.Contains(v, "v=123") {
		t.Error("query string leaked into the UI")
	}
}

func TestAddRouteShowsValidationError(t *testing.T) {
	m := newModel(t, twoUpstreams)
	m = press(t, m, "a")
	m = typeText(m, "not a domain")
	m = press(t, m, "enter")
	if m.modal != modalAddRoute || m.form.Err == "" {
		t.Fatal("invalid input should keep the modal open with an error")
	}
	m = press(t, m, "esc")
	if m.modal != modalNone || len(m.app.Routes()) != 0 {
		t.Fatal("esc should cancel without saving")
	}
}

func TestVCyclesThroughUpstreamsThenDirect(t *testing.T) {
	m := newModel(t, func(c *config.Config) {
		twoUpstreams(c)
		c.Routes = []router.Route{{Domain: "example.com", Mode: router.ModeProxy, Upstream: "seoul"}}
	})
	var got []string
	for range 3 {
		m = press(t, m, "v")
		got = append(got, m.app.Routes()[0].Via())
	}
	if strings.Join(got, ",") != "lab,direct,seoul" {
		t.Fatalf("via sequence = %v", got)
	}
}

func TestDeleteRouteWithConfirmation(t *testing.T) {
	m := newModel(t, func(c *config.Config) {
		twoUpstreams(c)
		c.Routes = []router.Route{{Domain: "example.com", Mode: router.ModeProxy}, {Domain: "example.org", Mode: router.ModeDirect}}
	})
	m = press(t, m, "d")
	if m.modal != modalDeleteRoute || !strings.Contains(plain(m.View()), "Remove example.com?") {
		t.Fatalf("confirmation not shown")
	}
	m = press(t, m, "n")
	if len(m.app.Routes()) != 2 {
		t.Fatal("n removed the route")
	}
	m = press(t, m, "d", "y")
	if r := m.app.Routes(); len(r) != 1 || r[0].Domain != "example.org" {
		t.Fatalf("routes = %+v", r)
	}
}

func TestPresetAsksForViaWithSeveralUpstreams(t *testing.T) {
	m := newModel(t, twoUpstreams)
	m = press(t, m, "p")
	first := m.presetPicker.Selected().Value
	m = press(t, m, "enter")
	if m.modal != modalPresetVia {
		t.Fatalf("modal = %v, want via picker", m.modal)
	}
	m = press(t, m, "down", "enter")
	p, _ := router.FindPreset(first)
	routes := m.app.Routes()
	if len(routes) != len(p.Domains) {
		t.Fatalf("routes = %d, want %d", len(routes), len(p.Domains))
	}
	for _, r := range routes {
		if r.Upstream != "lab" {
			t.Fatalf("preset route %+v not sent via lab", r)
		}
	}
}

func TestPresetPickerGroupsByCategory(t *testing.T) {
	m := newModel(t, twoUpstreams)
	m = press(t, m, "p")
	v := plain(m.View())
	for _, c := range []string{router.CategoryMedia, router.CategoryAI, router.CategoryDev} {
		if !strings.Contains(v, c) {
			t.Errorf("category %q missing from picker", c)
		}
	}
	if m.presetPicker.Selected().Header {
		t.Fatal("cursor landed on a category header")
	}
}

func TestUpstreamManagerAddEditDelete(t *testing.T) {
	m := newModel(t, twoUpstreams)
	m = press(t, m, "s")
	if m.modal != modalUpstreams || !strings.Contains(plain(m.View()), "proxy-seoul") {
		t.Fatalf("manager not shown: %v", m.modal)
	}
	// 새 업스트림: 이름 비움, 호스트만 입력 → 이름과 SOCKS 포트가 자동으로 정해진다.
	m = press(t, m, "a", "tab", "tab")
	m = typeText(m, "tokyo.example.net")
	m = press(t, m, "enter")
	if m.modal != modalUpstreams {
		t.Fatalf("form did not close: %q", m.form.Err)
	}
	u, ok := m.app.Config().Upstream("tokyo")
	if !ok || u.Socks != "127.0.0.1:1081" || u.Host != "tokyo.example.net" {
		t.Fatalf("new upstream = %+v %v", u, ok)
	}

	m.upCursor = 0
	m = press(t, m, "e", "tab")
	m.form.Fields[1].Input.SetValue("busan")
	m = press(t, m, "enter")
	if _, ok := m.app.Config().Upstream("busan"); !ok {
		t.Fatalf("rename failed: %q", m.form.Err)
	}

	m.upCursor = 0
	m = press(t, m, "d", "y")
	if _, ok := m.app.Config().Upstream("busan"); ok {
		t.Fatal("unused upstream not deleted")
	}
}

func TestUpstreamInUseCannotBeDeleted(t *testing.T) {
	m := newModel(t, func(c *config.Config) {
		twoUpstreams(c)
		c.Routes = []router.Route{{Domain: "example.com", Mode: router.ModeProxy, Upstream: "seoul"}}
	})
	m = press(t, m, "s", "d", "y")
	if _, ok := m.app.Config().Upstream("seoul"); !ok {
		t.Fatal("upstream in use was deleted")
	}
	if !strings.Contains(plain(m.View()), "move them with v first") {
		t.Fatal("no explanation shown")
	}
}

func TestLanguageToggleTranslatesAndPersists(t *testing.T) {
	m := newModel(t, twoUpstreams)
	if !strings.Contains(plain(m.View()), "ROUTES") {
		t.Fatal("expected English first")
	}
	m = press(t, m, "L")
	v := plain(m.View())
	if !strings.Contains(v, "라우트") || !strings.Contains(v, "언어: 한국어") {
		t.Fatalf("Korean not applied:\n%s", v)
	}
	if m.app.Config().Language != "ko" {
		t.Fatal("language not saved")
	}
}

func TestConfigErrorRequiresChoiceBeforeStart(t *testing.T) {
	started, backedUp := false, false
	m := newModel(t, twoUpstreams, func(o *Options) {
		o.LoadErr = errors.New("parse config: unexpected end of JSON input")
		o.Start = func() { started = true }
		o.Backup = func() (string, error) { backedUp = true; return "/x/config.json.bak", nil }
	})
	if m.modal != modalConfigError || m.started {
		t.Fatal("expected config error prompt before starting")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = next.(Model)
	if cmd != nil {
		cmd()
	}
	if !backedUp || !started || !m.started {
		t.Fatalf("backedUp=%v started=%v", backedUp, started)
	}
}

func TestQuitWaitsForShutdown(t *testing.T) {
	shutdown := false
	m := newModel(t, twoUpstreams, func(o *Options) { o.Shutdown = func() { shutdown = true } })
	m = press(t, m, "q")
	if !shutdown || !m.quitting || !strings.Contains(plain(m.View()), "Shutting down") {
		t.Fatal("q did not start graceful shutdown")
	}
	_, cmd := m.Update(eventsClosedMsg{})
	if cmd == nil {
		t.Fatal("no quit after event stream closed")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected tea.Quit")
	}
}

func TestSSHEventsProduceToasts(t *testing.T) {
	m := newModel(t, twoUpstreams)
	next, _ := m.Update(eventMsg{events.SSHStateChanged{Upstream: "seoul", Status: ssh.Status{State: ssh.StateConnected}}})
	m = next.(Model)
	if !strings.Contains(plain(m.View()), "seoul: SSH tunnel connected") {
		t.Error("connected toast missing")
	}
	next, _ = m.Update(eventMsg{events.SSHStateChanged{Upstream: "seoul", Status: ssh.Status{State: ssh.StateReconnecting, Err: "Connection refused"}}})
	m = next.(Model)
	if !strings.Contains(plain(m.View()), "seoul: SSH tunnel failed: Connection refused") {
		t.Error("failure toast missing")
	}
}

func TestResponsiveLayoutsFitTerminalInBothLanguages(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.KO} {
		m := newModel(t, func(c *config.Config) {
			twoUpstreams(c)
			for i, n := range []string{"c", "d", "e", "f"} {
				c.Upstreams = append(c.Upstreams, config.Upstream{Name: "extra-" + n, Mode: config.SSHExternal, Socks: "127.0.0.1:" + string(rune('2'+i)) + "000"})
			}
			c.Listen = "0.0.0.0:8080"
			c.Routes = []router.Route{
				{Domain: "example.com", Mode: router.ModeProxy, Upstream: "seoul"},
				{Domain: "example.org", Mode: router.ModeDirect},
				{Domain: "a-very-long-subdomain-name.for-testing-truncation.example.net", Mode: router.ModeProxy, Upstream: "lab"},
			}
		}, func(o *Options) { o.Lang = string(lang) })
		m.conns = []events.ConnectionEvent{
			{Time: time.Now(), Host: "cdn.example.com", Port: "443", Route: router.ModeProxy, Upstream: "seoul", State: events.ConnClosed, BytesIn: 1 << 20},
			{Time: time.Now(), Host: "example.org", Port: "443", Route: router.ModeDirect, State: events.ConnFailed, Error: errors.New("dial: connection refused")},
		}
		for _, size := range [][2]int{{160, 48}, {120, 36}, {96, 24}, {80, 24}, {60, 18}, {50, 14}} {
			mm := resize(m, size[0], size[1])
			assertFits(t, mm, string(lang))
			if v := plain(mm.View()); !strings.Contains(v, "[q]") || !strings.Contains(v, "[?]") {
				t.Errorf("%s %dx%d: help bar lost help/quit", lang, size[0], size[1])
			}
			for _, key := range []string{"s", "?", "a", "p", "e"} {
				assertFits(t, press(t, mm, key), string(lang)+" modal "+key)
			}
		}
	}

	m := newModel(t, func(c *config.Config) {
		twoUpstreams(c)
		c.Routes = []router.Route{{Domain: "example.com", Mode: router.ModeProxy, Upstream: "seoul"}}
	})
	wide := plain(resize(m, 120, 36).View())
	for _, want := range []string{"RouteBox", "ROUTES", "LIVE CONNECTIONS", "example.com", "→ seoul", "seoul", "lab", "SOCKS 127.0.0.1:9050", "[a] add"} {
		if !strings.Contains(wide, want) {
			t.Errorf("wide view missing %q", want)
		}
	}
	narrowM := resize(m, 80, 24)
	narrow := plain(narrowM.View())
	if !strings.Contains(narrow, "ROUTES") || strings.Contains(narrow, "LIVE CONNECTIONS") {
		t.Errorf("narrow view should prioritise routes:\n%s", narrow)
	}
	logs := plain(press(t, narrowM, "l").View())
	if strings.Contains(logs, "ROUTES") || !strings.Contains(logs, "LIVE CONNECTIONS") {
		t.Errorf("l should switch the narrow view to the log:\n%s", logs)
	}
}

func TestTooSmall(t *testing.T) {
	m := resize(newModel(t, twoUpstreams), 40, 10)
	if v := plain(m.View()); !strings.Contains(v, "Terminal too small") || !strings.Contains(v, "Minimum recommended size") {
		t.Fatalf("view = %q", v)
	}
}
