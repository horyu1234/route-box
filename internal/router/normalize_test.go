package router

import (
	"errors"
	"testing"
)

func TestNormalizeRouteInput(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"example.com", "example.com"},
		{"https://WWW.Example.com:443/watch?v=123", "www.example.com"},
		{"http://example.com/path/to?q=1#frag", "example.com"},
		{"example.com:8080", "example.com"},
		{"EXAMPLE.COM", "example.com"},
		{"example.com.", "example.com"},
		{"  example.com  ", "example.com"},
		{"*.example.com", "*.example.com"},
		{"https://*.Example.COM.:443/x", "*.example.com"},
		{".example.com", "example.com"},
		{"https://user:pass@example.com/", "example.com"},
		{"192.168.0.1", "192.168.0.1"},
		{"http://192.168.0.1:8080/x", "192.168.0.1"},
		{"[2001:DB8::1]:443", "2001:db8::1"},
		{"2001:db8::1", "2001:db8::1"},
		{"https://[::1]/", "::1"},
		{"localhost", "localhost"},
		{"_dmarc.example.com", "_dmarc.example.com"},
	}
	for _, tt := range tests {
		got, err := NormalizeRouteInput(tt.in)
		if err != nil {
			t.Errorf("NormalizeRouteInput(%q) error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("NormalizeRouteInput(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeRouteInputRejectsMalformed(t *testing.T) {
	for _, in := range []string{
		"", "   ", "https://", "exa mple.com", "-bad.com", "bad-.com", "a..b",
		"example.com:0", "example.com:99999", "example.com:abc", "[::1", "[::1]x",
		"ex!ample.com", "한국.kr", "1:2:3", "example.com:",
		"*.", "*", "*example.com", "*.*.example.com", "api.*.example.com", "*.10.0.0.1", "*.[::1]",
	} {
		if got, err := NormalizeRouteInput(in); err == nil {
			t.Errorf("NormalizeRouteInput(%q) = %q, want error", in, got)
		} else if !errors.Is(err, ErrInvalidHost) {
			t.Errorf("NormalizeRouteInput(%q) error %v does not wrap ErrInvalidHost", in, err)
		}
	}
}

func TestSplitHostPort(t *testing.T) {
	tests := []struct {
		in       string
		host     string
		kind     HostKind
		port     string
		wantFail bool
	}{
		{in: "www.example.com:443", host: "www.example.com", kind: KindDomain, port: "443"},
		{in: "WWW.Example.COM.:443", host: "www.example.com", kind: KindDomain, port: "443"},
		{in: "example.com", host: "example.com", kind: KindDomain},
		{in: "10.0.0.1:80", host: "10.0.0.1", kind: KindIPv4, port: "80"},
		{in: "[::1]:443", host: "::1", kind: KindIPv6, port: "443"},
		{in: "[::ffff:10.0.0.1]:443", host: "10.0.0.1", kind: KindIPv4, port: "443"},
		{in: "::1", host: "::1", kind: KindIPv6},
		{in: "[fe80::1%en0]:22", host: "fe80::1%en0", kind: KindIPv6, port: "22"},
		{in: ":443", wantFail: true},
		{in: "host:65536", wantFail: true},
		{in: "host:0443", wantFail: true},
		{in: "[::1]:", wantFail: true},
	}
	for _, tt := range tests {
		h, port, err := SplitHostPort(tt.in)
		if tt.wantFail {
			if err == nil {
				t.Errorf("SplitHostPort(%q) = %+v %q, want error", tt.in, h, port)
			}
			continue
		}
		if err != nil {
			t.Errorf("SplitHostPort(%q) error: %v", tt.in, err)
			continue
		}
		if h.Name != tt.host || h.Kind != tt.kind || port != tt.port {
			t.Errorf("SplitHostPort(%q) = (%q, %v, %q), want (%q, %v, %q)",
				tt.in, h.Name, h.Kind, port, tt.host, tt.kind, tt.port)
		}
	}
}
