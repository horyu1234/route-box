package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/horyu1234/route-box/internal/router"
)

func TestLoadMissingReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "config.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
	if cfg.Listen != DefaultListen || len(cfg.Upstreams) != 0 || cfg.UpstreamConfigured() {
		t.Errorf("defaults = %+v", cfg)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Default()
	want.Upstreams = []Upstream{
		{Name: "seoul", Mode: SSHManaged, Host: "proxy-seoul", Port: 2222, IdentityFile: "~/.ssh/id_ed25519", Socks: "127.0.0.1:1080", Reconnect: true},
		{Name: "lab", Mode: SSHExternal, Socks: "127.0.0.1:9050"},
	}
	want.Routes = []router.Route{{Domain: "example.com", Mode: router.ModeProxy, Upstream: "lab"}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Upstreams) != 2 || got.Upstreams[0] != want.Upstreams[0] || got.Upstreams[1] != want.Upstreams[1] ||
		len(got.Routes) != 1 || got.Routes[0] != want.Routes[0] {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config perm = %o, want 600", perm)
	}
}

func TestLoadNormalisesRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{"upstreams":[{"name":"Seoul","host":"proxy-seoul"}],"routes":[{"domain":"HTTPS://WWW.Example.com/x"},{"domain":"www.example.com","mode":"proxy"},{"domain":"example.org","mode":"direct","upstream":"seoul"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []router.Route{{Domain: "www.example.com", Mode: router.ModeProxy, Upstream: "seoul"}, {Domain: "example.org", Mode: router.ModeDirect}}
	if len(cfg.Routes) != len(want) {
		t.Fatalf("routes = %+v, want %+v", cfg.Routes, want)
	}
	for i := range want {
		if cfg.Routes[i] != want[i] {
			t.Errorf("routes[%d] = %+v, want %+v", i, cfg.Routes[i], want[i])
		}
	}
	if cfg.Listen != DefaultListen {
		t.Errorf("listen default not applied: %q", cfg.Listen)
	}
	if u := cfg.Upstreams[0]; u.Name != "seoul" || u.Mode != SSHManaged || u.Socks != DefaultSocks {
		t.Errorf("upstream defaults not applied: %+v", u)
	}
}

func TestLoadInvalidJSONKeepsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := []byte(`{"listen": "127.0.0.1:8080",`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want parse error", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("original file changed: %q, %v", got, err)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{
		`{"listen":"nope"}`,
		`{"routes":[{"domain":"bad domain"}]}`,
		`{"routes":[{"domain":"a.com","mode":"deny"}]}`,
		`{"upstreams":[{"name":"a","mode":"magic","socks":"127.0.0.1:1"}]}`,
		`{"upstreams":[{"name":"a","host":"h","port":70000}]}`,
		`{"upstreams":[{"name":"direct","host":"h"}]}`,
		`{"upstreams":[{"name":"a b","host":"h"}]}`,
		`{"upstreams":[{"name":"a","host":"-oProxyCommand=x"}]}`,
		`{"upstreams":[{"name":"a","host":"h"},{"name":"a","host":"h2","socks":"127.0.0.1:1081"}]}`,
		`{"upstreams":[{"name":"a","host":"h"},{"name":"b","host":"h2"}]}`,
		`{"upstreams":[{"name":"a","host":"h"}],"routes":[{"domain":"example.com","upstream":"missing"}]}`,
		`{"language":"fr"}`,
		`{"upstreams":[{"name":"a","host":"h"}],"fallback":"missing"}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("Load(%s) succeeded, want error", raw)
		}
	}
}

func TestFallbackNormalisesAndDefaultsToDirect(t *testing.T) {
	for raw, want := range map[string]string{
		`{"upstreams":[{"name":"a","host":"h"}]}`:                     "",
		`{"upstreams":[{"name":"a","host":"h"}],"fallback":"DIRECT"}`: "",
		`{"upstreams":[{"name":"a","host":"h"}],"fallback":" A "}`:    "a",
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil || cfg.Fallback != want {
			t.Errorf("Load(%s): fallback %q, %v; want %q", raw, cfg.Fallback, err, want)
		}
	}
}

func TestSaveReplacesAtomicallyAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	for i, host := range []string{"first", "second"} {
		cfg := Default()
		cfg.Listen = "127.0.0.1:" + map[string]string{"first": "8081", "second": "8082"}[host]
		if err := Save(path, cfg); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != "127.0.0.1:8082" {
		t.Errorf("listen = %q, want the second save", got.Listen)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestSaveFailureKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	orig := Default()
	orig.Listen = "127.0.0.1:1111"
	if err := Save(path, orig); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	next := Default()
	next.Listen = "127.0.0.1:2222"
	if err := Save(path, next); err == nil {
		t.Skip("directory is writable despite chmod (running as root?)")
	}
	got, err := Load(path)
	if err != nil || got.Listen != "127.0.0.1:1111" {
		t.Fatalf("original damaged: %+v, %v", got, err)
	}
}

func TestBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	bak, err := Backup(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bak)
	if err != nil || string(data) != "{broken" {
		t.Fatalf("backup = %q, %v", data, err)
	}
}

func TestStoreUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := NewStore(path, Default())

	if _, err := s.Update(func(c *Config) error {
		c.Routes = append(c.Routes, router.Route{Domain: "bad domain"})
		return nil
	}); err == nil {
		t.Fatal("invalid update accepted")
	}
	if len(s.Get().Routes) != 0 {
		t.Fatal("failed update leaked into store")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("failed update was written to disk")
	}

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Update(func(c *Config) error {
				c.Routes = append(c.Routes, router.Route{Domain: "host" + string(rune('a'+i)) + ".com"})
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(s.Get().Routes); n != 20 {
		t.Fatalf("routes = %d, want 20", n)
	}
	onDisk, err := Load(path)
	if err != nil || len(onDisk.Routes) != 20 {
		t.Fatalf("disk routes = %d, %v", len(onDisk.Routes), err)
	}
}

func TestIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true,
		"[::1]:8080":     true,
		"localhost:8080": true,
		"0.0.0.0:8080":   false,
		"[::]:8080":      false,
		"192.168.1.2:80": false,
	} {
		if got := IsLoopback(addr); got != want {
			t.Errorf("IsLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestLegacySingleUpstreamIsMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{"listen":"127.0.0.1:8080","socks":"127.0.0.1:1090","routes":[{"domain":"example.com","mode":"proxy"}],
		"ssh":{"mode":"managed","host":"proxy-seoul","port":22,"reconnect":true}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Upstream{Name: "default", Mode: SSHManaged, Host: "proxy-seoul", Port: 22, Socks: "127.0.0.1:1090", Reconnect: true}
	if len(cfg.Upstreams) != 1 || cfg.Upstreams[0] != want {
		t.Fatalf("upstreams = %+v", cfg.Upstreams)
	}
	if cfg.Routes[0].Upstream != "default" {
		t.Fatalf("route not pinned to migrated upstream: %+v", cfg.Routes[0])
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), `"ssh"`) || strings.Contains(string(data), `"socks": "127.0.0.1:1090",\n  "routes"`) {
		t.Fatalf("legacy fields written back:\n%s", data)
	}
	again, err := Load(path)
	if err != nil || len(again.Upstreams) != 1 || again.Upstreams[0] != want {
		t.Fatalf("reload: %+v %v", again.Upstreams, err)
	}
}

func TestEmptyViaIsPinnedToFirstUpstream(t *testing.T) {
	cfg := Default()
	cfg.Routes = []router.Route{{Domain: "example.com", Mode: router.ModeProxy}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.Routes[0].Upstream != "" {
		t.Fatal("no upstream yet: via must stay empty")
	}
	cfg.Upstreams = []Upstream{{Name: "b", Mode: SSHExternal, Socks: "127.0.0.1:1081"}, {Name: "a", Mode: SSHExternal, Socks: "127.0.0.1:1082"}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.Routes[0].Upstream != "b" {
		t.Fatalf("route via = %q, want first upstream", cfg.Routes[0].Upstream)
	}
	cfg.Upstreams[0], cfg.Upstreams[1] = cfg.Upstreams[1], cfg.Upstreams[0]
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.Routes[0].Upstream != "b" {
		t.Fatal("reordering upstreams silently rerouted a pinned route")
	}
}

func TestMigratedFlagOnlyForLegacyFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(path, []byte(`{"ssh":{"mode":"external"},"socks":"127.0.0.1:1090"}`), 0o600)
	if cfg, err := Load(path); err != nil || !cfg.Migrated {
		t.Fatalf("legacy load: migrated=%v err=%v", cfg.Migrated, err)
	}
	_ = os.WriteFile(path, []byte(`{"upstreams":[{"name":"a","mode":"external","socks":"127.0.0.1:1"}]}`), 0o600)
	if cfg, err := Load(path); err != nil || cfg.Migrated {
		t.Fatalf("current format flagged as migrated: %v %v", cfg.Migrated, err)
	}
}
