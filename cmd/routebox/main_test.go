package main

import (
	"os"
	"path/filepath"
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

func TestServiceExeKeepsHomebrewLinkPath(t *testing.T) {
	dir := t.TempDir()
	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	cellar := filepath.Join(real, "Cellar", "routebox", "0.3.0", "bin", "routebox")
	write(cellar)
	link(cellar, filepath.Join(real, "brew", "bin", "routebox"))
	if got, err := serviceExe(filepath.Join(real, "brew", "bin", "routebox")); err != nil || got != filepath.Join(real, "brew", "bin", "routebox") {
		t.Errorf("homebrew: %q %v, want the version-independent link", got, err)
	}

	plain := filepath.Join(real, "go", "bin", "routebox")
	write(plain)
	link(plain, filepath.Join(real, "local", "bin", "routebox"))
	if got, err := serviceExe(filepath.Join(real, "local", "bin", "routebox")); err != nil || got != plain {
		t.Errorf("other symlink: %q %v, want it resolved to %q", got, err, plain)
	}
}
