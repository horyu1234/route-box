package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func testSpec() Spec {
	return Spec{
		Exe:        "/Applications/Route Box/routebox",
		ConfigPath: "/Users/me/Library/Application Support/routebox/config.json",
		LogPath:    "/Users/me/Library/Logs/RouteBox/routebox.log",
		ErrPath:    "/Users/me/Library/Logs/RouteBox/stderr.log",
		PATH:       "/usr/bin:/bin:/opt/<odd>&dir",
	}
}

func TestPlistRunsHeadlessWithExplicitConfig(t *testing.T) {
	p := Plist(testSpec())
	for _, want := range []string{
		"<string>" + Label + "</string>",
		"<string>/Applications/Route Box/routebox</string>\n\t\t<string>--no-tui</string>\n\t\t<string>--config</string>\n\t\t<string>/Users/me/Library/Application Support/routebox/config.json</string>\n\t\t<string>--log-file</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>SuccessfulExit</key>\n\t\t<false/>",
		"<string>/usr/bin:/bin:/opt/&lt;odd&gt;&amp;dir</string>",
		"<key>StandardErrorPath</key>\n\t<string>/Users/me/Library/Logs/RouteBox/stderr.log</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
	if runtime.GOOS != "darwin" {
		return
	}
	if _, err := exec.LookPath("plutil"); err != nil {
		return
	}
	path := filepath.Join(t.TempDir(), "x.plist")
	if err := os.WriteFile(path, []byte(p), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil -lint: %v\n%s", err, out)
	}
}

func TestSystemdUnitQuotesArgumentsAndUsesJournal(t *testing.T) {
	s := testSpec()
	s.Exe = `/home/me/bin/route box`
	s.ConfigPath = `/home/me/.config/routebox/100%$HOME"x".json`
	u := SystemdUnit(s)
	want := `ExecStart="/home/me/bin/route box" "--no-tui" "--config" "/home/me/.config/routebox/100%%$$HOME\"x\".json"` + "\n"
	if !strings.Contains(u, want) {
		t.Errorf("ExecStart:\n%s\nwant %s", u, want)
	}
	for _, want := range []string{"Restart=on-failure", "WantedBy=default.target", `Environment="PATH=/usr/bin:/bin:/opt/<odd>&dir"`} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q", want)
		}
	}
	if strings.Contains(u, "--log-file") {
		t.Error("systemd unit must log to journald, not a file")
	}
}

type call []string

func fakeManager(t *testing.T, goos string) (*Manager, *[]call) {
	t.Helper()
	var calls []call
	m := &Manager{OS: goos, Home: t.TempDir(), UID: 501, Run: func(name string, args ...string) (string, error) {
		calls = append(calls, append(call{name}, args...))
		return "", nil
	}}
	return m, &calls
}

// fakeLaunchd 는 서비스가 loaded 인 동안 print 가 성공하고, bootout 뒤에도
// linger 번 더 성공하다가(정리 중) 실패하는 launchctl 이다.
func fakeLaunchd(t *testing.T, loaded bool, linger int) (*Manager, *[]call) {
	t.Helper()
	var calls []call
	m := &Manager{OS: "darwin", Home: t.TempDir(), UID: 501}
	m.Run = func(name string, args ...string) (string, error) {
		calls = append(calls, append(call{name}, args...))
		switch args[0] {
		case "print":
			if loaded {
				return "", nil
			}
			if linger > 0 {
				linger--
				return "", nil
			}
			return "", errors.New("not found")
		case "bootout":
			loaded = false
		case "bootstrap":
			if linger > 0 {
				return "", errors.New("Bootstrap failed: 5: Input/output error")
			}
			loaded = true
		}
		return "", nil
	}
	return m, &calls
}

func TestLaunchdReinstallWaitsForTeardownBeforeBootstrap(t *testing.T) {
	m, calls := fakeLaunchd(t, true, 2)
	if err := m.Install(m.Spec("/usr/local/bin/routebox", "/cfg/config.json", "")); err != nil {
		t.Fatal(err)
	}
	svc := "gui/501/" + Label
	want := []call{
		{"launchctl", "print", svc},
		{"launchctl", "bootout", svc},
		{"launchctl", "print", svc},
		{"launchctl", "print", svc},
		{"launchctl", "print", svc},
		{"launchctl", "bootstrap", "gui/501", m.DefinitionPath()},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls =\n%v\nwant\n%v", *calls, want)
	}
}

func TestLaunchdInstallWritesPlistAndBootstraps(t *testing.T) {
	m, calls := fakeLaunchd(t, false, 0)
	s := m.Spec("/usr/local/bin/routebox", "/cfg/config.json", "/usr/bin:/bin")
	if err := m.Install(s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(m.DefinitionPath())
	if err != nil || !strings.Contains(string(b), m.LogPath()) {
		t.Fatalf("plist not written with log path: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(m.LogPath())); err != nil {
		t.Errorf("log dir not created: %v", err)
	}
	want := []call{
		{"launchctl", "print", "gui/501/" + Label},
		{"launchctl", "bootstrap", "gui/501", m.DefinitionPath()},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}

	*calls = nil
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if m.Installed() {
		t.Error("plist still present after uninstall")
	}
	if !reflect.DeepEqual(*calls, []call{{"launchctl", "print", "gui/501/" + Label}, {"launchctl", "bootout", "gui/501/" + Label}, {"launchctl", "print", "gui/501/" + Label}}) {
		t.Fatalf("uninstall calls = %v", *calls)
	}
}

func TestSystemdInstallEnablesAndRestarts(t *testing.T) {
	m, calls := fakeManager(t, "linux")
	if err := m.Install(m.Spec("/usr/bin/routebox", "/cfg/config.json", "")); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(m.DefinitionPath(), ".config/systemd/user/routebox.service") || !m.Installed() {
		t.Fatalf("unit path %s", m.DefinitionPath())
	}
	want := []call{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", Unit},
		{"systemctl", "--user", "restart", Unit},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestCommandsRequireInstallAndSupportedOS(t *testing.T) {
	m, _ := fakeManager(t, "darwin")
	for name, fn := range map[string]func() error{"start": m.Start, "stop": m.Stop, "restart": m.Restart} {
		if err := fn(); err == nil || !strings.Contains(err.Error(), "service install") {
			t.Errorf("%s before install: %v", name, err)
		}
	}
	w, _ := fakeManager(t, "windows")
	if err := w.Install(Spec{}); err != ErrUnsupported {
		t.Errorf("windows install: %v", err)
	}
}
