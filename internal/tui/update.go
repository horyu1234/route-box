package tui

import (
	"context"
	"errors"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/ssh"
	"github.com/horyu1234/route-box/internal/tui/components"
)

const (
	modeManaged  = "managed"
	modeExternal = "external"
	// viaFirst 는 업스트림이 아직 없을 때 "첫 업스트림이 생기면 그리로" 를 뜻하는 선택지 값이다.
	viaFirst = ""
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureCursor()
		return m, nil
	case tickMsg:
		m.now = time.Time(msg)
		m.frame++
		m.refresh()
		m.toasts.Expire(m.now)
		if m.modal == modalOnboard && m.onboard.step == onboardResult && m.onboard.ok && m.now.After(m.onboard.doneAt) {
			m.modal = modalNone
		}
		m.ensureCursor()
		return m, tick()
	case eventMsg:
		m.handleEvent(msg.e)
		return m, waitEvent(m.events)
	case hostKeyMsg:
		// 기다리는 사이 모달을 닫았거나 다른 업스트림을 골랐으면 버린다.
		if m.modal == modalHostKey && m.hostKey.scanning && m.hostKey.name == msg.name {
			m.hostKey.scanning = false
			m.hostKey.key, m.hostKey.err = msg.key, msg.err
		}
		return m, nil
	case eventsClosedMsg:
		m.stopped = true
		if m.quitting {
			return m, tea.Quit
		}
		m.refresh()
		m.fatal = m.t("RouteBox stopped unexpectedly.")
		if m.opts.Attached {
			m.fatal = m.t("Lost the connection to the background RouteBox.")
		} else if m.status.Proxy.Err != "" {
			m.fatal = m.t("Could not start the proxy:") + "\n" + m.status.Proxy.Err
		}
		m.modal = modalFatal
		return m, nil
	case shutdownMsg:
		return m.beginQuit()
	case forceQuitMsg:
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m.beginQuit()
		}
		if m.modal != modalNone {
			return m.updateModal(msg)
		}
		return m.updateMain(msg)
	}
	return m, nil
}

func (m Model) beginQuit() (tea.Model, tea.Cmd) {
	// 붙어 있을 때는 패널만 닫는다: 백그라운드 인스턴스는 계속 돈다.
	if m.quitting || !m.started || m.stopped || m.opts.Attached {
		return m, tea.Quit
	}
	m.quitting = true
	m.opts.Shutdown()
	return m, tea.Tick(6*time.Second, func(time.Time) tea.Msg { return forceQuitMsg{} })
}

func (m *Model) toast(kind components.ToastKind, key string, args ...any) {
	m.toasts.Push(m.t(key, args...), kind, time.Now())
}

func (m *Model) handleEvent(e events.Event) {
	switch e := e.(type) {
	case events.SSHStateChanged:
		m.onSSH(e.Upstream, e.Status)
	case events.UpstreamHealth:
		m.onHealth(e)
	case events.Notice:
		m.toasts.Push(m.t(e.Message), components.ToastWarn, time.Now())
	case events.RoutesChanged:
		m.routes = e.Routes
		m.ensureCursor()
	}
}

func (m *Model) onSSH(name string, st ssh.Status) {
	prev := m.lastSSH[name]
	m.lastSSH[name] = st
	onboarding := m.modal == modalOnboard && m.onboard.step == onboardWaiting && m.onboard.name == name
	switch {
	case st.State == ssh.StateConnected && prev.State != ssh.StateConnected:
		if onboarding {
			m.finishOnboarding(true, "")
			return
		}
		m.toast(components.ToastSuccess, "%s: SSH tunnel connected", name)
	case (st.State == ssh.StateReconnecting || st.State == ssh.StateFailed) && st.Err != "" &&
		(prev.State != st.State || prev.Err != st.Err):
		if onboarding {
			m.finishOnboarding(false, st.Err)
			return
		}
		if ssh.IsHostKeyFailure(st.Err) {
			m.toast(components.ToastError, "%s: unknown host key — press s, then t to check it", name)
			return
		}
		m.toast(components.ToastError, "%s: SSH tunnel failed: %s", name, st.Err)
	}
}

func (m *Model) onHealth(e events.UpstreamHealth) {
	prev, known := m.lastReach[e.Upstream]
	m.lastReach[e.Upstream] = e.Reachable
	u, ok := m.app.Config().Upstream(e.Upstream)
	if !ok || u.Mode != config.SSHExternal {
		return
	}
	if m.modal == modalOnboard && m.onboard.step == onboardWaiting && m.onboard.name == e.Upstream {
		detail := ""
		if e.Err != nil {
			detail = e.Err.Error()
		}
		m.finishOnboarding(e.Reachable, detail)
		return
	}
	if !known || prev == e.Reachable {
		return
	}
	if e.Reachable {
		m.toast(components.ToastSuccess, "%s: SOCKS upstream reachable", e.Upstream)
	} else {
		m.toast(components.ToastWarn, "%s: SOCKS upstream unreachable", e.Upstream)
	}
}

// listHeight 는 Update(스크롤)와 View 가 공유하는 패널 안쪽 행 수다.
func (m Model) listHeight() int { return max(1, m.layout().bodyH-3) }

func (m *Model) ensureCursor() {
	m.routeList.Ensure(components.Rows(len(m.routes)), m.listHeight())
	m.upCursor = max(0, min(len(m.app.Config().Upstreams)-1, m.upCursor))
}

func (m Model) selectedRoute() (router.Route, bool) {
	if m.routeList.Cursor < len(m.routes) {
		return m.routes[m.routeList.Cursor], true
	}
	return router.Route{}, false
}

func (m Model) upstreamNames() []string {
	ups := m.app.Config().Upstreams
	names := make([]string, len(ups))
	for i, u := range ups {
		names[i] = u.Name
	}
	return names
}

// viaOptions 는 route 가 갈 수 있는 곳: 업스트림들과 direct. 업스트림이 없으면
// "첫 업스트림" 자리표시를 앞에 둔다.
func (m Model) viaOptions() (labels, values []string) {
	names := m.upstreamNames()
	if len(names) == 0 {
		labels, values = []string{m.t("first upstream")}, []string{viaFirst}
	}
	labels = append(labels, names...)
	values = append(values, names...)
	labels = append(labels, router.ViaDirect)
	values = append(values, router.ViaDirect)
	return labels, values
}

func (m Model) viaText(r router.Route) string {
	switch v := r.Via(); v {
	case viaFirst:
		return m.t("first upstream")
	case router.ViaDirect:
		return "DIRECT"
	default:
		return v
	}
}

func (m Model) updateMain(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	lay := m.layout()
	logsFocused := m.focus == focusLogs && lay.logsShown
	rows := components.Rows(len(m.routes))
	switch msg.String() {
	case "q":
		return m.beginQuit()
	case "up", "k":
		if logsFocused {
			m.logView.Scroll(1, len(m.conns), m.listHeight())
		} else {
			m.routeList.Move(-1, rows, m.listHeight())
		}
	case "down", "j":
		if logsFocused {
			m.logView.Scroll(-1, len(m.conns), m.listHeight())
		} else {
			m.routeList.Move(1, rows, m.listHeight())
		}
	case "pgup":
		m.logView.Scroll(m.listHeight(), len(m.conns), m.listHeight())
	case "pgdown":
		m.logView.Scroll(-m.listHeight(), len(m.conns), m.listHeight())
	case "g":
		if logsFocused {
			m.logView.Scroll(len(m.conns), len(m.conns), m.listHeight())
		} else {
			m.routeList.Move(-rows, rows, m.listHeight())
		}
	case "G":
		if logsFocused {
			m.logView.Offset = 0
		} else {
			m.routeList.Move(rows, rows, m.listHeight())
		}
	case "tab":
		if lay.logsShown && lay.routesShown {
			if m.focus == focusRoutes {
				m.focus = focusLogs
			} else {
				m.focus = focusRoutes
			}
		}
	case "l":
		if lay.wide {
			m.showLogs = !m.showLogs
		} else {
			m.logsOnly = !m.logsOnly
		}
		m.focus = focusRoutes
		if !m.layout().routesShown {
			m.focus = focusLogs
		}
	case "L":
		m.lang = m.lang.Next()
		if err := m.app.SetLanguage(string(m.lang)); err != nil {
			m.toast(components.ToastError, "%v", err)
		} else {
			m.toast(components.ToastInfo, "Language: %s", m.lang.Name())
		}
	case "a":
		m.openRouteForm("", router.Route{})
	case "enter":
		if m.routeList.OnAddRow(len(m.routes)) {
			m.openRouteForm("", router.Route{})
		} else if r, ok := m.selectedRoute(); ok {
			m.openRouteForm(r.Domain, r)
		}
	case "e":
		if r, ok := m.selectedRoute(); ok {
			m.openRouteForm(r.Domain, r)
		}
	case "v", " ":
		m.cycleVia()
	case "d", "delete", "backspace":
		if r, ok := m.selectedRoute(); ok {
			m.deleting = r.Domain
			m.modal = modalDeleteRoute
		}
	case "p":
		m.presetPicker = m.presetChooser()
		m.modal = modalPreset
	case "s", "u":
		m.modal = modalUpstreams
	case "r":
		m.restartUpstream("")
	case "?":
		m.modal = modalHelp
	}
	return m, nil
}

// cycleVia 는 선택한 route 를 다음 업스트림(마지막은 direct)으로 즉시 옮긴다.
func (m *Model) cycleVia() {
	r, ok := m.selectedRoute()
	if !ok {
		return
	}
	_, values := m.viaOptions()
	next := values[0]
	for i, v := range values {
		if v == r.Via() {
			next = values[(i+1)%len(values)]
		}
	}
	nr, err := m.app.SetRouteVia(r.Domain, next)
	if err != nil {
		m.toast(components.ToastError, "%v", err)
		return
	}
	m.toast(components.ToastSuccess, "%s → %s", nr.Domain, m.viaText(nr))
	m.routes = m.app.Routes()
}

func (m *Model) restartUpstream(name string) {
	err := m.app.RestartUpstream(name)
	switch {
	case err == nil && name == "":
		m.toast(components.ToastInfo, "Restarting all upstreams…")
	case err == nil:
		m.toast(components.ToastInfo, "Restarting %s…", name)
	case errors.Is(err, core.ErrNoUpstream):
		m.toast(components.ToastWarn, "No upstream configured — press s to add one")
	default:
		m.toast(components.ToastError, "Restart failed: %v", err)
	}
}

// openRouteForm 은 old 가 비어 있으면 추가, 아니면 편집 폼을 연다.
func (m *Model) openRouteForm(old string, r router.Route) {
	labels, values := m.viaOptions()
	sel := 0
	if old != "" {
		for i, v := range values {
			if v == r.Via() {
				sel = i
			}
		}
	}
	title, kind := m.t("Add Route"), modalAddRoute
	if old != "" {
		title, kind = m.t("Edit Route"), modalEditRoute
	}
	m.form = components.NewForm(m.lang, title,
		components.TextField("domain", m.t("Domain"), r.Domain, "example.com",
			m.t("Subdomains are included. Pasting a URL works too.")),
		components.ChoiceField("via", m.t("Via"), labels, sel, m.t("←/→ picks where this domain goes")),
	)
	m.editing = old
	m.modal = kind
}

func (m Model) presetChooser() components.Chooser {
	var items []components.ChooserItem
	category := ""
	for _, p := range router.Presets() {
		if p.Category != category {
			category = p.Category
			items = append(items, components.ChooserItem{Label: m.t(category), Header: true})
		}
		items = append(items, components.ChooserItem{Label: p.Title, Value: p.Name, Detail: m.t("%d domains", len(p.Domains))})
	}
	return components.NewChooser(items...)
}

func (m *Model) openOnboarding() {
	m.onboard = onboardState{chooser: components.NewChooser(
		components.ChooserItem{Label: m.t("Managed SSH"), Value: modeManaged, Detail: m.t("RouteBox runs ssh -D for you")},
		components.ChooserItem{Label: m.t("External SOCKS"), Value: modeExternal, Detail: m.t("use a SOCKS5 server you already run")},
	)}
	m.modal = modalOnboard
}

func (m *Model) finishOnboarding(ok bool, detail string) {
	m.onboard.step = onboardResult
	m.onboard.ok = ok
	m.onboard.detail = detail
	m.onboard.doneAt = time.Now().Add(1500 * time.Millisecond)
	if ok {
		m.toast(components.ToastSuccess, "Connected to %s", m.onboard.name)
	}
}

func (m Model) updateModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch m.modal {
	case modalHelp:
		if key == "esc" || key == "?" || key == "q" || key == "enter" {
			m.modal = modalNone
		}
	case modalDeleteRoute:
		switch key {
		case "y", "Y", "enter":
			if r, err := m.app.RemoveRoute(m.deleting); err != nil {
				m.toast(components.ToastError, "%v", err)
			} else {
				m.toast(components.ToastSuccess, "Route removed: %s", r.Domain)
				m.routes = m.app.Routes()
				m.ensureCursor()
			}
			m.modal = modalNone
		case "n", "N", "esc", "q":
			m.modal = modalNone
		}
	case modalUpstreams:
		return m.updateUpstreams(key)
	case modalDeleteUpstream:
		switch key {
		case "y", "Y", "enter":
			if err := m.app.RemoveUpstream(m.deleting); err != nil {
				if errors.Is(err, core.ErrUpstreamInUse) {
					m.toast(components.ToastError, "%s is used by %d route(s) — move them with v first", m.deleting, m.app.Config().RoutesVia(m.deleting))
				} else {
					m.toast(components.ToastError, "%v", err)
				}
			} else {
				m.toast(components.ToastSuccess, "Upstream removed: %s", m.deleting)
			}
			m.modal = modalUpstreams
			m.refresh()
			m.ensureCursor()
		case "n", "N", "esc", "q":
			m.modal = modalUpstreams
		}
	case modalPreset:
		switch key {
		case "up", "k":
			m.presetPicker.Move(-1)
		case "down", "j":
			m.presetPicker.Move(1)
		case "esc", "q":
			m.modal = modalNone
		case "enter":
			m.pendingPre = m.presetPicker.Selected().Value
			if len(m.upstreamNames()) > 1 {
				labels, values := m.viaOptions()
				items := make([]components.ChooserItem, len(labels))
				for i := range labels {
					items[i] = components.ChooserItem{Label: labels[i], Value: values[i]}
				}
				m.viaPicker = components.NewChooser(items...)
				m.modal = modalPresetVia
				return m, nil
			}
			m.addPreset(viaFirst)
		}
	case modalPresetVia:
		switch key {
		case "up", "k":
			m.viaPicker.Move(-1)
		case "down", "j":
			m.viaPicker.Move(1)
		case "esc", "q":
			m.modal = modalPreset
		case "enter":
			m.addPreset(m.viaPicker.Selected().Value)
		}
	case modalAddRoute, modalEditRoute, modalUpstreamForm:
		res, cmd := m.form.Update(msg)
		switch res {
		case components.FormCancelled:
			if m.modal == modalUpstreamForm {
				m.modal = modalUpstreams
			} else {
				m.modal = modalNone
			}
		case components.FormSubmitted:
			m.submitForm()
		}
		return m, cmd
	case modalOnboard:
		return m.updateOnboarding(msg)
	case modalConfigError:
		switch key {
		case "d", "D":
			m.modal = modalNone
			m.started = true
			if m.opts.Backup != nil {
				if path, err := m.opts.Backup(); err != nil {
					m.toast(components.ToastError, "Could not back up config: %v", err)
				} else {
					m.toast(components.ToastInfo, "Broken config backed up to %s", path)
				}
			}
			if m.needsOnboarding() {
				m.openOnboarding()
			}
			return m, startCmd(m.opts.Start)
		case "q", "esc":
			return m, tea.Quit
		}
	case modalFatal:
		if key == "q" || key == "esc" || key == "enter" {
			return m, tea.Quit
		}
	case modalHostKey:
		return m.updateHostKey(key)
	}
	return m, nil
}

// scanHostKey 는 ssh 로 서버의 host key 를 받아 온다. 연결을 두 번 하므로
// Update 를 막지 않도록 tea.Cmd 로 돈다.
func (m *Model) scanHostKey(name string) tea.Cmd {
	m.hostKey = hostKeyState{name: name, scanning: true}
	m.modal = modalHostKey
	app := m.app
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		k, err := app.ScanHostKey(ctx, name)
		return hostKeyMsg{name: name, key: k, err: err}
	}
}

func (m Model) updateHostKey(key string) (tea.Model, tea.Cmd) {
	h := m.hostKey
	switch {
	case key == "esc" || key == "q" || key == "n" || key == "N":
		m.modal = modalUpstreams
	case h.scanning:
	case h.err == nil && (key == "y" || key == "Y"):
		if err := m.app.TrustHostKey(h.name, h.key.Fingerprint()); err != nil {
			m.toast(components.ToastError, "Could not save the host key: %v", err)
		} else {
			m.toast(components.ToastSuccess, "Host key saved — reconnecting %s…", h.name)
		}
		m.modal = modalUpstreams
		m.refresh()
	case h.err != nil && key == "enter":
		m.modal = modalUpstreams
	}
	return m, nil
}

func (m *Model) addPreset(via string) {
	p, err := router.FindPreset(m.pendingPre)
	if err != nil {
		m.toast(components.ToastError, "%v", err)
		m.modal = modalNone
		return
	}
	added, err := m.app.AddPreset(p.Name, via)
	switch {
	case err != nil:
		m.toast(components.ToastError, "%v", err)
	case len(added) == 0:
		m.toast(components.ToastInfo, "All %s domains are already routed", p.Title)
	default:
		m.toast(components.ToastSuccess, "Added %d %s domains", len(added), p.Title)
	}
	m.routes = m.app.Routes()
	m.ensureCursor()
	m.modal = modalNone
}

func (m Model) updateUpstreams(key string) (tea.Model, tea.Cmd) {
	ups := m.app.Config().Upstreams
	selected := func() (config.Upstream, bool) {
		if m.upCursor < len(ups) {
			return ups[m.upCursor], true
		}
		return config.Upstream{}, false
	}
	switch key {
	case "esc", "q", "s":
		m.modal = modalNone
	case "up", "k":
		m.upCursor = max(0, m.upCursor-1)
	case "down", "j":
		m.upCursor = min(max(0, len(ups)-1), m.upCursor+1)
	case "a":
		m.openUpstreamForm(config.Upstream{Mode: config.SSHManaged, Reconnect: true, Socks: core.SuggestSocks(m.app.Config(), "")}, "")
	case "e", "enter":
		if u, ok := selected(); ok {
			m.openUpstreamForm(u, u.Name)
		}
	case "d", "delete", "backspace":
		if u, ok := selected(); ok {
			m.deleting = u.Name
			m.modal = modalDeleteUpstream
		}
	case "r":
		if u, ok := selected(); ok {
			m.restartUpstream(u.Name)
		}
	case "t":
		if u, ok := selected(); ok && u.Mode == config.SSHManaged {
			return m, m.scanHostKey(u.Name)
		}
	}
	return m, nil
}

func (m *Model) openUpstreamForm(u config.Upstream, editing string) {
	title := m.t("Add Upstream")
	if editing != "" {
		title = m.t("Edit Upstream")
	}
	m.form = m.upstreamForm(title, u, true)
	m.editing = editing
	m.modal = modalUpstreamForm
}

func (m *Model) submitForm() {
	switch m.modal {
	case modalAddRoute, modalEditRoute:
		_, values := m.viaOptions()
		domain, _ := m.form.Get("domain")
		via := values[m.form.Fields[1].Choice]
		var r router.Route
		var err error
		if m.modal == modalAddRoute {
			r, err = m.app.AddRoute(domain, via)
		} else {
			r, err = m.app.UpdateRoute(m.editing, domain, via)
		}
		if err != nil {
			m.form.Err = err.Error()
			return
		}
		if m.modal == modalAddRoute {
			m.toast(components.ToastSuccess, "Route added: %s → %s", r.Domain, m.viaText(r))
		} else {
			m.toast(components.ToastSuccess, "Route updated: %s → %s", r.Domain, m.viaText(r))
		}
		m.selectRoute(r.Domain)
		m.modal = modalNone
	case modalUpstreamForm:
		u, err := parseUpstreamForm(m.form, m.app.Config(), m.editing)
		if err != nil {
			m.form.Err = err.Error()
			return
		}
		if m.editing == "" {
			u, err = m.app.AddUpstream(u)
		} else {
			u, err = m.app.UpdateUpstream(m.editing, u)
		}
		if err != nil {
			m.form.Err = err.Error()
			return
		}
		if u.Mode == config.SSHManaged {
			m.toast(components.ToastInfo, "Saved %s — connecting to %s…", u.Name, u.Host)
		} else {
			m.toast(components.ToastSuccess, "Saved %s", u.Name)
		}
		m.refresh()
		for i, x := range m.app.Config().Upstreams {
			if x.Name == u.Name {
				m.upCursor = i
			}
		}
		m.modal = modalUpstreams
	}
}

func (m *Model) selectRoute(domain string) {
	m.routes = m.app.Routes()
	for i, r := range m.routes {
		if r.Domain == domain {
			m.routeList.Cursor = i
		}
	}
	m.focus = focusRoutes
	m.ensureCursor()
}

func (m Model) updateOnboarding(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	o := &m.onboard
	switch o.step {
	case onboardChoose:
		switch key {
		case "up", "k":
			o.chooser.Move(-1)
		case "down", "j":
			o.chooser.Move(1)
		case "esc", "q":
			m.modal = modalNone
		case "enter":
			o.managed = o.chooser.Selected().Value == modeManaged
			u := config.Upstream{Mode: config.SSHManaged, Reconnect: true, Socks: config.DefaultSocks}
			title := m.t("Managed SSH")
			intro := m.t("RouteBox runs ssh -N -D for this host. Hosts from ~/.ssh/config work as-is; key-based auth only.")
			if !o.managed {
				u.Mode = config.SSHExternal
				title = m.t("External SOCKS")
				intro = m.t("Use a SOCKS5 server you already run, e.g. ssh -N -D 127.0.0.1:1080 user@server")
			}
			o.form = m.upstreamForm(title, u, false)
			o.form.Intro = components.Dim.Render(intro)
			o.step = onboardForm
		}
	case onboardForm:
		res, cmd := o.form.Update(msg)
		switch res {
		case components.FormCancelled:
			o.step = onboardChoose
		case components.FormSubmitted:
			u, err := parseUpstreamForm(o.form, m.app.Config(), "")
			if err != nil {
				o.form.Err = err.Error()
				return m, nil
			}
			if u, err = m.app.AddUpstream(u); err != nil {
				o.form.Err = err.Error()
				return m, nil
			}
			o.name = u.Name
			o.step = onboardWaiting
			delete(m.lastSSH, u.Name)
		}
		return m, cmd
	case onboardWaiting:
		if key == "esc" {
			m.modal = modalNone
		}
	case onboardResult:
		if key == "enter" && !o.ok {
			m.modal = modalUpstreams
			return m, nil
		}
		m.modal = modalNone
	}
	return m, nil
}

// upstreamForm 은 업스트림 설정 폼이다. full 이 false 면(온보딩) 이름과 종류
// 선택 없이 u.Mode 에 맞는 필드만 보여 주고 이름은 자동으로 짓는다.
func (m Model) upstreamForm(title string, u config.Upstream, full bool) *components.Form {
	port := ""
	if u.Port > 0 {
		port = strconv.Itoa(u.Port)
	}
	socks := components.TextField("socks", m.t("Local SOCKS"), u.Socks, config.DefaultSocks, m.t("where ssh -D listens; must differ per upstream"))
	if !full && u.Mode == config.SSHExternal {
		socks = components.TextField("socks", m.t("SOCKS Address"), u.Socks, config.DefaultSocks, m.t("host:port of your SOCKS5 server"))
		return components.NewForm(m.lang, title, socks)
	}
	group := ""
	if full {
		group = modeManaged
	}
	rc := 0
	if !u.Reconnect {
		rc = 1
	}
	sshFields := []components.Field{
		components.TextField("host", m.t("SSH Host"), u.Host, "proxy-seoul", m.t("a Host alias from ~/.ssh/config or a hostname")),
		components.TextField("user", m.t("SSH User"), u.User, m.t("optional"), m.t("empty = from ~/.ssh/config")),
		components.TextField("port", m.t("SSH Port"), port, m.t("optional"), m.t("empty = from ~/.ssh/config (usually 22)")),
		components.TextField("identity", m.t("Identity File"), u.IdentityFile, "~/.ssh/id_ed25519", m.t("path only; the key itself is never stored")),
	}
	if full {
		sshFields = append(sshFields, components.ChoiceField("reconnect", m.t("Reconnect"), []string{m.t("yes"), m.t("no")}, rc,
			m.t("restart ssh automatically when it exits")))
	}
	for i := range sshFields {
		sshFields[i].Group = group
	}
	if !full {
		return components.NewForm(m.lang, title, append(sshFields, socks)...)
	}
	mode := 0
	if u.Mode == config.SSHExternal {
		mode = 1
	}
	fields := []components.Field{
		components.ChoiceField("mode", m.t("Type"), []string{modeManaged, modeExternal}, mode,
			m.t("managed: RouteBox runs ssh · external: an existing SOCKS5 server")),
		components.TextField("name", m.t("Name"), u.Name, m.t("auto from host"), m.t("used by routes, e.g. seoul, work, lab")),
	}
	fields = append(fields, sshFields...)
	return components.NewForm(m.lang, title, append(fields, socks)...)
}

// parseUpstreamForm 은 폼 값을 업스트림으로 읽는다. 이름이 비어 있으면 호스트에서 짓는다.
func parseUpstreamForm(f *components.Form, cfg config.Config, editing string) (config.Upstream, error) {
	u := config.Upstream{Mode: config.SSHManaged, Reconnect: true}
	if prev, ok := cfg.Upstream(editing); ok {
		u = prev
	}
	_, hasHost := f.Get("host")
	if v, ok := f.Get("mode"); ok {
		u.Mode = config.SSHMode(v)
	} else if !hasHost {
		u.Mode = config.SSHExternal
	}
	if u.Mode == config.SSHManaged {
		u.Host, _ = f.Get("host")
		u.User, _ = f.Get("user")
		u.IdentityFile, _ = f.Get("identity")
		u.Port = 0
		if v, _ := f.Get("port"); v != "" {
			p, err := strconv.Atoi(v)
			if err != nil || p < 1 || p > 65535 {
				return u, errors.New("SSH port must be a number between 1 and 65535")
			}
			u.Port = p
		}
		for _, fl := range f.Fields {
			if fl.Key == "reconnect" {
				u.Reconnect = fl.Choice == 0
			}
		}
		if u.Host == "" {
			return u, errors.New("SSH host is required")
		}
	} else {
		u.Host, u.User, u.IdentityFile, u.Port = "", "", "", 0
	}

	others := cfg
	others.Upstreams = nil
	for _, x := range cfg.Upstreams {
		if x.Name != editing {
			others.Upstreams = append(others.Upstreams, x)
		}
	}
	u.Socks, _ = f.Get("socks")
	if u.Socks == "" {
		u.Socks = core.SuggestSocks(others, "")
	}
	name, _ := f.Get("name")
	if name == "" {
		seed := u.Host
		if u.Mode == config.SSHExternal {
			seed = "socks"
		}
		name = core.SuggestName(others, seed)
	}
	u.Name = name
	return u, config.ValidateUpstream(u)
}
