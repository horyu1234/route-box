// Package service 는 RouteBox 를 로그인할 때 뜨는 사용자 서비스(macOS launchd
// LaunchAgent, Linux systemd --user unit)로 등록한다. 브라우저 프록시가 늘
// RouteBox 를 가리키므로, 터미널을 열어 두지 않아도 프록시가 떠 있어야 한다.
package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// Label 은 launchd 라벨이다.
	Label = "io.github.horyu1234.routebox"
	// Unit 은 systemd user unit 이름이다.
	Unit = "routebox.service"
)

var ErrUnsupported = errors.New("background service is supported on macOS (launchd) and Linux (systemd --user) only")

// Spec 은 서비스가 실행할 명령이다.
type Spec struct {
	Exe        string // routebox 바이너리의 절대 경로
	ConfigPath string // 절대 경로; 서비스에는 셸의 ROUTEBOX_CONFIG 가 없다
	LogPath    string // launchd 전용: --log-file 로 넘길 경로
	ErrPath    string // launchd 전용: 패닉 등 stderr 가 남는 곳
	PATH       string // ssh 와 ProxyCommand 가 찾을 PATH
}

// Args 는 서비스가 실행할 인자다(Exe 포함).
func (s Spec) Args() []string {
	args := []string{s.Exe, "--no-tui", "--config", s.ConfigPath}
	if s.LogPath != "" {
		args = append(args, "--log-file", s.LogPath)
	}
	return args
}

// Plist 는 launchd LaunchAgent 정의다. 비정상 종료(오류로 인한 exit 1,
// 크래시)에만 다시 띄우므로, SIGTERM 으로 깔끔히 멈춘 서비스는 멈춘 채로 남는다.
func Plist(s Spec) string {
	var b bytes.Buffer
	esc := func(v string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(v))
		return e.String()
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range s.Args() {
		b.WriteString("\t\t<string>" + esc(a) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>ProcessType</key>
	<string>Interactive</string>
`)
	if s.PATH != "" {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n\t\t<key>PATH</key>\n\t\t<string>" + esc(s.PATH) + "</string>\n\t</dict>\n")
	}
	if s.ErrPath != "" {
		b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + esc(s.ErrPath) + "</string>\n")
		b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + esc(s.ErrPath) + "</string>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// SystemdUnit 은 systemd user unit 이다. 로그는 journald 가 받는다.
func SystemdUnit(s Spec) string {
	s.LogPath = ""
	quoted := make([]string, 0, 4)
	for _, a := range s.Args() {
		quoted = append(quoted, systemdQuote(a))
	}
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=RouteBox selective proxy router\nAfter=network-online.target\nWants=network-online.target\n\n")
	b.WriteString("[Service]\nType=simple\nExecStart=" + strings.Join(quoted, " ") + "\n")
	if s.PATH != "" {
		b.WriteString("Environment=" + systemdQuote("PATH="+s.PATH) + "\n")
	}
	b.WriteString("Restart=on-failure\nRestartSec=10\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

// systemdQuote 는 systemd 가 한 인자로 읽도록 따옴표로 감싼다. % 는 specifier,
// $ 는 환경변수 치환이라 두 번 써서 글자 그대로 둔다.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(s) + `"`
}

// Manager 는 현재 OS 의 서비스 관리자를 다룬다.
type Manager struct {
	OS   string
	Home string
	UID  int
	// Run 은 launchctl/systemctl 을 실행한다. 테스트에서 바꿔 끼운다.
	Run func(name string, args ...string) (string, error)
}

func NewManager() (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Manager{OS: runtime.GOOS, Home: home, UID: os.Getuid(), Run: run}, nil
}

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s == "" {
			return s, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return s, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, s)
	}
	return s, nil
}

func (m *Manager) supported() error {
	if m.OS != "darwin" && m.OS != "linux" {
		return ErrUnsupported
	}
	return nil
}

// DefinitionPath 는 plist 또는 unit 파일 위치다.
func (m *Manager) DefinitionPath() string {
	if m.OS == "darwin" {
		return filepath.Join(m.Home, "Library", "LaunchAgents", Label+".plist")
	}
	return filepath.Join(m.Home, ".config", "systemd", "user", Unit)
}

// LogPath 는 launchd 서비스의 로그 파일이다. Linux 는 journald 를 쓴다.
func (m *Manager) LogPath() string {
	if m.OS == "darwin" {
		return filepath.Join(m.Home, "Library", "Logs", "RouteBox", "routebox.log")
	}
	return ""
}

func (m *Manager) errPath() string {
	if m.OS == "darwin" {
		return filepath.Join(m.Home, "Library", "Logs", "RouteBox", "stderr.log")
	}
	return ""
}

// Spec 은 이 OS 에 맞는 로그 경로를 채운 Spec 이다.
func (m *Manager) Spec(exe, configPath, path string) Spec {
	return Spec{Exe: exe, ConfigPath: configPath, LogPath: m.LogPath(), ErrPath: m.errPath(), PATH: path}
}

func (m *Manager) target() string { return "gui/" + strconv.Itoa(m.UID) }

func (m *Manager) service() string { return m.target() + "/" + Label }

// Installed 는 정의 파일이 있는지 보고한다.
func (m *Manager) Installed() bool {
	_, err := os.Stat(m.DefinitionPath())
	return err == nil
}

// Install 은 정의 파일을 쓰고 서비스를 (다시) 올린다. 이미 설치돼 있으면
// 새 정의로 교체하고 재시작한다.
func (m *Manager) Install(s Spec) error {
	if err := m.supported(); err != nil {
		return err
	}
	var def string
	if m.OS == "darwin" {
		def = Plist(s)
		if err := os.MkdirAll(filepath.Dir(s.ErrPath), 0o700); err != nil {
			return fmt.Errorf("create log dir: %w", err)
		}
	} else {
		def = SystemdUnit(s)
	}
	path := m.DefinitionPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(def), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if m.OS == "darwin" {
		if err := m.unload(); err != nil {
			return err
		}
		_, err := m.Run("launchctl", "bootstrap", m.target(), path)
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", Unit}, {"restart", Unit}} {
		if _, err := m.Run("systemctl", append([]string{"--user"}, args...)...); err != nil {
			return err
		}
	}
	return nil
}

// unload 는 올라가 있는 서비스를 내리고, launchd 가 정리를 끝낼 때까지 기다린다.
// 정리 중에 bootstrap 하면 launchctl 이 거부하고, 옛 인스턴스가 아직 제어
// 소켓에 답해 새 인스턴스가 뜬 것처럼 보이기 때문이다.
func (m *Manager) unload() error {
	if !m.Loaded() {
		return nil
	}
	_, _ = m.Run("launchctl", "bootout", m.service())
	for deadline := time.Now().Add(15 * time.Second); m.Loaded(); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			return errors.New("the previous service did not stop within 15s; try `routebox service uninstall` first")
		}
	}
	return nil
}

// Uninstall 은 서비스를 멈추고 정의 파일을 지운다.
func (m *Manager) Uninstall() error {
	if err := m.supported(); err != nil {
		return err
	}
	path := m.DefinitionPath()
	if m.OS == "darwin" {
		if err := m.unload(); err != nil {
			return err
		}
	} else {
		_, _ = m.Run("systemctl", "--user", "disable", "--now", Unit)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if m.OS == "linux" {
		_, _ = m.Run("systemctl", "--user", "daemon-reload")
	}
	return nil
}

func (m *Manager) requireInstalled() error {
	if err := m.supported(); err != nil {
		return err
	}
	if !m.Installed() {
		return errors.New("the service is not installed; run `routebox service install` first")
	}
	return nil
}

// Start 는 서비스를 띄운다.
func (m *Manager) Start() error {
	if err := m.requireInstalled(); err != nil {
		return err
	}
	if m.OS == "darwin" {
		if !m.Loaded() {
			_, err := m.Run("launchctl", "bootstrap", m.target(), m.DefinitionPath())
			return err
		}
		_, err := m.Run("launchctl", "kickstart", m.service())
		return err
	}
	_, err := m.Run("systemctl", "--user", "start", Unit)
	return err
}

// Stop 은 서비스를 멈춘다. launchd 에서는 다음 로그인 때 다시 뜬다.
func (m *Manager) Stop() error {
	if err := m.requireInstalled(); err != nil {
		return err
	}
	if m.OS == "darwin" {
		return m.unload()
	}
	_, err := m.Run("systemctl", "--user", "stop", Unit)
	return err
}

// Restart 는 서비스를 다시 띄운다(바이너리를 교체한 뒤 등).
func (m *Manager) Restart() error {
	if err := m.requireInstalled(); err != nil {
		return err
	}
	if m.OS == "darwin" {
		if !m.Loaded() {
			_, err := m.Run("launchctl", "bootstrap", m.target(), m.DefinitionPath())
			return err
		}
		_, err := m.Run("launchctl", "kickstart", "-k", m.service())
		return err
	}
	_, err := m.Run("systemctl", "--user", "restart", Unit)
	return err
}

// Loaded 는 서비스 관리자가 서비스를 올려 두었는지(launchd) 또는
// 활성인지(systemd) 보고한다.
func (m *Manager) Loaded() bool {
	if m.OS == "darwin" {
		_, err := m.Run("launchctl", "print", m.service())
		return err == nil
	}
	_, err := m.Run("systemctl", "--user", "is-active", "--quiet", Unit)
	return err == nil
}
