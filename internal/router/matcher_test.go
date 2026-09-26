package router

import (
	"errors"
	"strings"
	"testing"
)

func mustHost(t *testing.T, s string) Host {
	t.Helper()
	h, err := ParseHost(s)
	if err != nil {
		t.Fatalf("ParseHost(%q): %v", s, err)
	}
	return h
}

func TestMatcherSuffix(t *testing.T) {
	m := NewMatcher([]Route{{Domain: "example.com", Mode: ModeProxy}})
	tests := []struct {
		host string
		want bool
	}{
		{"example.com", true},
		{"www.example.com", true},
		{"abc.www.example.com", true},
		{"mail.example.com", true},
		{"Example.COM.", true},
		{"notexample.com", false},
		{"example.com.attacker.net", false},
		{"com", false},
		{"example.co", false},
	}
	for _, tt := range tests {
		_, ok := m.Match(mustHost(t, tt.host))
		if ok != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.host, ok, tt.want)
		}
	}
}

func TestMatcherMostSpecificWins(t *testing.T) {
	m := NewMatcher([]Route{
		{Domain: "example.com", Mode: ModeProxy},
		{Domain: "intranet.example.com", Mode: ModeDirect},
	})
	if r, _ := m.Match(mustHost(t, "a.intranet.example.com")); r.Mode != ModeDirect || r.Domain != "intranet.example.com" {
		t.Errorf("got %+v, want intranet.example.com direct", r)
	}
	if r, _ := m.Match(mustHost(t, "www.example.com")); r.Mode != ModeProxy {
		t.Errorf("got %+v, want proxy", r)
	}
}

func TestWildcardMatchesSubdomainsOnly(t *testing.T) {
	m := NewMatcher([]Route{{Domain: "*.example.com", Mode: ModeProxy, Upstream: "seoul"}})
	for host, want := range map[string]bool{
		"www.example.com":          true,
		"a.b.example.com":          true,
		"WWW.Example.COM.":         true,
		"example.com":              false,
		"notexample.com":           false,
		"example.com.attacker.net": false,
		"com":                      false,
	} {
		r, ok := m.Match(mustHost(t, host))
		if ok != want || (ok && r.Domain != "*.example.com") {
			t.Errorf("Match(%q) = %+v, %v; want %v", host, r, ok, want)
		}
	}
}

func TestWildcardAndApexSplitTraffic(t *testing.T) {
	m := NewMatcher([]Route{
		{Domain: "example.com", Mode: ModeDirect},
		{Domain: "*.example.com", Mode: ModeProxy, Upstream: "seoul"},
		{Domain: "intranet.example.com", Mode: ModeDirect},
		{Domain: "*.intranet.example.com", Mode: ModeProxy, Upstream: "lab"},
	})
	for host, want := range map[string]string{
		"example.com":            "example.com",
		"www.example.com":        "*.example.com",
		"intranet.example.com":   "intranet.example.com",
		"a.intranet.example.com": "*.intranet.example.com",
	} {
		if r, ok := m.Match(mustHost(t, host)); !ok || r.Domain != want {
			t.Errorf("Match(%q) = %+v, want %s", host, r, want)
		}
	}
}

func TestMatcherIPLiteralsAreExact(t *testing.T) {
	m := NewMatcher([]Route{
		{Domain: "10.0.0.1", Mode: ModeProxy},
		{Domain: "2001:db8::1", Mode: ModeProxy},
	})
	if _, ok := m.Match(mustHost(t, "10.0.0.1")); !ok {
		t.Error("10.0.0.1 should match exactly")
	}
	if _, ok := m.Match(mustHost(t, "192.10.0.0.1.nip.io")); ok {
		t.Error("domain must not match an IP route by suffix")
	}
	if _, ok := m.Match(mustHost(t, "10.0.0.2")); ok {
		t.Error("10.0.0.2 must not match")
	}
	if _, ok := m.Match(mustHost(t, "[2001:DB8:0::1]")); !ok {
		t.Error("IPv6 literal should match regardless of notation")
	}
}

func TestRouterDecide(t *testing.T) {
	r := New([]Route{{Domain: "example.com", Mode: ModeProxy}})
	if d := r.Decide(mustHost(t, "www.example.com")); d.Mode != ModeProxy || d.Matched != "example.com" {
		t.Errorf("www.example.com: got %+v", d)
	}
	if d := r.Decide(mustHost(t, "github.com")); d.Mode != ModeDirect || d.Matched != "" {
		t.Errorf("github.com: got %+v", d)
	}
	r.SetRoutes(nil)
	if d := r.Decide(mustHost(t, "www.example.com")); d.Mode != ModeDirect {
		t.Errorf("after clearing routes: got %+v", d)
	}
}

func TestPresetsAreWellFormed(t *testing.T) {
	names := map[string]bool{}
	categories := map[string]bool{CategoryMedia: true, CategoryAI: true, CategoryDev: true, CategoryUtility: true}
	for _, p := range Presets() {
		if names[p.Name] {
			t.Errorf("duplicate preset name %q", p.Name)
		}
		names[p.Name] = true
		if err := ValidateUpstreamName(p.Name); err != nil {
			t.Errorf("preset name %q: %v", p.Name, err)
		}
		if !categories[p.Category] || p.Title == "" || len(p.Domains) == 0 {
			t.Errorf("preset %q incomplete: %+v", p.Name, p)
		}
		seen := map[string]bool{}
		for _, d := range p.Domains {
			n, err := NormalizeRouteInput(d)
			if err != nil || n != d {
				t.Errorf("preset %q domain %q not canonical (%q, %v)", p.Name, d, n, err)
			}
			if seen[d] {
				t.Errorf("preset %q repeats %q", p.Name, d)
			}
			seen[d] = true
		}
		routes, err := p.Routes("up1")
		if err != nil || len(routes) != len(p.Domains) || routes[0].Upstream != "up1" {
			t.Errorf("preset %q routes: %+v %v", p.Name, routes, err)
		}
	}
	first := Presets()[0]
	if p, err := FindPreset(strings.ToUpper(first.Name)); err != nil || p.Name != first.Name {
		t.Errorf("FindPreset is not case-insensitive: %v", err)
	}
	if _, err := FindPreset("nope"); !errors.Is(err, ErrUnknownPreset) {
		t.Errorf("unknown preset: %v", err)
	}
}

func TestParseVia(t *testing.T) {
	for _, tt := range []struct {
		in   string
		mode Mode
		up   string
		ok   bool
	}{
		{"", ModeProxy, "", true},
		{"direct", ModeDirect, "", true},
		{"DIRECT", ModeDirect, "", true},
		{"seoul", ModeProxy, "seoul", true},
		{"tokyo-2", ModeProxy, "tokyo-2", true},
		{"bad name", "", "", false},
		{"-x", "", "", false},
		{"proxy.example.com", "", "", false},
	} {
		m, up, err := ParseVia(tt.in)
		if (err == nil) != tt.ok || m != tt.mode || up != tt.up {
			t.Errorf("ParseVia(%q) = %q %q %v", tt.in, m, up, err)
		}
	}
	if err := ValidateUpstreamName("direct"); err == nil {
		t.Error("direct must be reserved")
	}
}

func TestDecideCarriesUpstream(t *testing.T) {
	r := New([]Route{
		{Domain: "example.com", Mode: ModeProxy, Upstream: "seoul"},
		{Domain: "example.org", Mode: ModeProxy, Upstream: "tokyo"},
	})
	if d := r.Decide(mustHost(t, "www.example.com")); d.Upstream != "seoul" {
		t.Errorf("got %+v", d)
	}
	if d := r.Decide(mustHost(t, "example.org")); d.Upstream != "tokyo" {
		t.Errorf("got %+v", d)
	}
}
