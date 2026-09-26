package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	info := func(v string) *debug.BuildInfo { return &debug.BuildInfo{Main: debug.Module{Version: v}} }
	for _, c := range []struct {
		ldflags string
		bi      *debug.BuildInfo
		ok      bool
		want    string
	}{
		{"dev", info("v0.1.1"), true, "v0.1.1"},                                             // go install …@v0.1.1
		{"dev", info("v0.0.0-20260926-303cf9ed13dc"), true, "v0.0.0-20260926-303cf9ed13dc"}, // …@main
		{"v0.1.1-2-gabc", info("v0.1.1"), true, "v0.1.1-2-gabc"},                            // make build 가 우선
		{"dev", info("(devel)"), true, "dev"},
		{"dev", info(""), true, "dev"},
		{"dev", nil, false, "dev"},
	} {
		if got := resolveVersion(c.ldflags, c.bi, c.ok); got != c.want {
			t.Errorf("resolveVersion(%q, %v) = %q, want %q", c.ldflags, c.bi, got, c.want)
		}
	}
}
