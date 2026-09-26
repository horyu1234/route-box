// Package tui 는 RouteBox 터미널 UI 다. core 를 이벤트 스트림과 주기적
// 스냅샷으로 관찰하고, 변경은 오직 Backend(같은 프로세스의 core.App, 또는
// 백그라운드 인스턴스에 붙은 control.Remote)를 통해서만 한다.
package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/ssh"
	"github.com/horyu1234/route-box/internal/tui/components"
	"github.com/horyu1234/route-box/internal/tui/i18n"
)

// Backend 는 TUI 가 RouteBox 를 보고 바꾸는 데 쓰는 core.App 의 메서드들이다.
// getter 는 Update 안에서 동기적으로 불리므로 빨리 반환해야 한다.
type Backend interface {
	Config() config.Config
	Status() core.Status
	Routes() []router.Route
	RecentConnections() []events.ConnectionEvent
	Subscribe(buffer int) (<-chan events.Event, func())

	AddRoute(input, via string) (router.Route, error)
	UpdateRoute(oldDomain, input, via string) (router.Route, error)
	SetRouteVia(domain, via string) (router.Route, error)
	RemoveRoute(domain string) (router.Route, error)
	SetFallback(via string) (string, error)
	ClearConnections() error
	SetConnectionLog(on bool) error
	AddPreset(name, via string) ([]router.Route, error)
	AddUpstream(u config.Upstream) (config.Upstream, error)
	UpdateUpstream(oldName string, u config.Upstream) (config.Upstream, error)
	RemoveUpstream(name string) error
	RestartUpstream(name string) error
	// ScanHostKey 는 ssh 연결을 하므로 느리다: Update 밖의 tea.Cmd 에서만 부른다.
	ScanHostKey(ctx context.Context, name string) (ssh.HostKey, error)
	TrustHostKey(name, fingerprint string) error
	SetLanguage(lang string) error
}

type Options struct {
	App Backend
	// Attached 는 백그라운드로 도는 인스턴스에 관리 패널로만 붙었음을 뜻한다.
	// 이때 q 는 패널만 닫고 프록시와 ssh 터널은 계속 돈다.
	Attached bool
	// Start 는 프록시, ssh supervisor, 제어 소켓을 띄운다. 여러 번 불려도 안전해야 한다.
	Start func()
	// Shutdown 은 그것들을 취소한다. 이벤트 스트림이 닫히면 TUI 가 끝난다.
	Shutdown func()
	// LoadErr 는 config 파일이 있지만 읽지 못했을 때 설정된다. TUI 가 기본값으로
	// 시작할지 먼저 묻는다.
	LoadErr error
	// Backup 은 덮어쓰기 전에 깨진 config 를 옆으로 복사한다.
	Backup func() (string, error)
	// Lang 은 이번 실행에만 쓰는 언어("en"/"ko")다. 비어 있으면 저장값, 그다음 환경변수.
	Lang string
}

type modalKind int

const (
	modalNone modalKind = iota
	modalAddRoute
	modalEditRoute
	modalDeleteRoute
	modalUpstreams
	modalUpstreamForm
	modalDeleteUpstream
	modalPreset
	modalPresetVia
	modalHelp
	modalOnboard
	modalConfigError
	modalFatal
	modalHostKey
)

type focusArea int

const (
	focusRoutes focusArea = iota
	focusLogs
)

type onboardStep int

const (
	onboardChoose onboardStep = iota
	onboardForm
	onboardWaiting
	onboardResult
)

type onboardState struct {
	step    onboardStep
	chooser components.Chooser
	form    *components.Form
	managed bool
	name    string
	ok      bool
	detail  string
	doneAt  time.Time
}

// hostKeyState 는 host key 확인 모달의 상태다.
type hostKeyState struct {
	name     string
	scanning bool
	key      ssh.HostKey
	err      error
}

type Model struct {
	opts   Options
	app    Backend
	events <-chan events.Event
	unsub  func()
	lang   i18n.Lang

	width, height int
	now           time.Time
	frame         int

	status core.Status
	routes []router.Route
	conns  []events.ConnectionEvent

	routeList components.RouteList
	logView   components.LogView
	focus     focusArea
	showLogs  bool // 넓은 화면: routes 옆에 로그 패널
	logsOnly  bool // 좁은 화면: routes 대신 로그 패널
	toasts    components.Toasts

	modal        modalKind
	form         *components.Form
	editing      string // 편집 중인 route 도메인 또는 업스트림 이름("" 이면 새로 추가)
	deleting     string
	upCursor     int
	presetPicker components.Chooser
	viaPicker    components.Chooser
	pendingPre   string
	onboard      onboardState
	hostKey      hostKeyState

	started   bool
	quitting  bool
	stopped   bool
	fatal     string
	lastSSH   map[string]ssh.Status
	lastReach map[string]bool
}

type (
	tickMsg         time.Time
	eventMsg        struct{ e events.Event }
	eventsClosedMsg struct{}
	hostKeyMsg      struct {
		name string
		key  ssh.HostKey
		err  error
	}
	shutdownMsg  struct{}
	forceQuitMsg struct{}
)

const tickInterval = 250 * time.Millisecond

func New(opts Options) Model {
	ch, unsub := opts.App.Subscribe(1024)
	m := Model{
		opts:      opts,
		app:       opts.App,
		events:    ch,
		unsub:     unsub,
		lang:      i18n.Detect(opts.Lang, opts.App.Config().Language),
		now:       time.Now(),
		showLogs:  true,
		lastSSH:   map[string]ssh.Status{},
		lastReach: map[string]bool{},
	}
	m.refresh()
	m.started = opts.LoadErr == nil
	if opts.Attached {
		m.toast(components.ToastInfo, "Attached to the background RouteBox — q closes this panel only")
	}
	switch {
	case opts.LoadErr != nil:
		m.modal = modalConfigError
	case m.needsOnboarding():
		m.openOnboarding()
	}
	return m
}

func (m Model) needsOnboarding() bool {
	return len(m.app.Config().Upstreams) == 0
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick(), waitEvent(m.events)}
	if m.started && m.opts.Start != nil {
		cmds = append(cmds, startCmd(m.opts.Start))
	}
	return tea.Batch(cmds...)
}

func startCmd(start func()) tea.Cmd {
	return func() tea.Msg {
		start()
		return nil
	}
}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func waitEvent(ch <-chan events.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return eventsClosedMsg{}
		}
		return eventMsg{e}
	}
}

func (m *Model) refresh() {
	m.status = m.app.Status()
	m.routes = m.app.Routes()
	m.conns = m.app.RecentConnections()
}

func (m Model) t(key string, args ...any) string { return m.lang.T(key, args...) }

// Run 은 alternate screen 에서 프로그램을 시작한다. ctx 가 취소되면(SIGTERM,
// SIGHUP) q 를 누른 것과 같은 graceful shutdown 을 한다.
func Run(ctx context.Context, opts Options) error {
	m := New(opts)
	defer m.unsub()
	p := tea.NewProgram(m, tea.WithAltScreen())
	stop := context.AfterFunc(ctx, func() { p.Send(shutdownMsg{}) })
	defer stop()
	_, err := p.Run()
	return err
}
