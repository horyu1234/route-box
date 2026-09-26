// Package tui 는 RouteBox 터미널 UI 다. core 를 이벤트 스트림과 주기적
// 스냅샷으로 관찰하고, 변경은 오직 core.App 을 통해서만 한다.
package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/ssh"
	"github.com/horyu1234/route-box/internal/tui/components"
	"github.com/horyu1234/route-box/internal/tui/i18n"
)

type Options struct {
	App *core.App
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

type Model struct {
	opts   Options
	app    *core.App
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
	shutdownMsg     struct{}
	forceQuitMsg    struct{}
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
	if m.started {
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
