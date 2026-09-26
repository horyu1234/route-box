package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/ssh"
	"github.com/horyu1234/route-box/internal/tui/components"
)

const (
	minWidth    = 50
	minHeight   = 14
	wideWidth   = 96
	tallEnough  = 22
	modalMaxW   = 76
	helpBarRows = 1
	toastRows   = 1
	maxFootUps  = 4
)

type layout struct {
	wide        bool
	footerBox   bool // 업스트림 패널 전체 vs 한 줄 상태
	headerH     int
	footerH     int
	bodyH       int
	routesW     int
	logsW       int
	routesShown bool
	logsShown   bool
	warnings    []string
}

func (m Model) layout() layout {
	l := layout{wide: m.width >= wideWidth, footerBox: m.height >= tallEnough, headerH: 2}
	l.warnings = m.status.Warnings
	l.headerH += len(l.warnings)
	l.footerH = 1
	if l.footerBox {
		l.footerH = 2 + 1 + max(1, min(maxFootUps, len(m.status.Upstreams)))
	}
	l.bodyH = m.height - l.headerH - l.footerH - toastRows - helpBarRows
	switch {
	case l.wide && m.showLogs:
		l.routesW = max(34, min(52, m.width*38/100))
		l.logsW = m.width - l.routesW
		l.routesShown, l.logsShown = true, true
	case m.logsOnly && !l.wide:
		l.logsW = m.width
		l.logsShown = true
	default:
		l.routesW = m.width
		l.routesShown = true
	}
	return l
}

func (m Model) helpEntries() []components.HelpEntry {
	quit := m.t("quit")
	if m.opts.Attached {
		quit = m.t("close")
	}
	return []components.HelpEntry{
		{Key: "a", Desc: m.t("add")}, {Key: "v", Desc: m.t("via")}, {Key: "e", Desc: m.t("edit")},
		{Key: "d", Desc: m.t("delete")}, {Key: "s", Desc: m.t("upstreams")}, {Key: "p", Desc: m.t("preset")},
		{Key: "r", Desc: m.t("restart")}, {Key: "l", Desc: m.t("logs")}, {Key: "L", Desc: m.t("language")},
		{Key: "?", Desc: m.t("help")}, {Key: "q", Desc: quit},
	}
}

func (m Model) helpDetails() []components.HelpEntry {
	quit := m.t("quit (stops ssh tunnels)")
	if m.opts.Attached {
		quit = m.t("close this panel (RouteBox keeps running)")
	}
	return []components.HelpEntry{
		{Key: "↑/k ↓/j", Desc: m.t("move selection / scroll log")},
		{Key: "tab", Desc: m.t("switch focus between routes and log")},
		{Key: "a", Desc: m.t("route a domain through an upstream")},
		{Key: "v / space", Desc: m.t("send the selected route to the next upstream")},
		{Key: "e / enter", Desc: m.t("edit the selected route")},
		{Key: "d", Desc: m.t("delete the selected route")},
		{Key: "p", Desc: m.t("add domains from a preset")},
		{Key: "s", Desc: m.t("manage upstreams (SSH hosts / SOCKS servers)")},
		{Key: "r", Desc: m.t("restart all upstreams")},
		{Key: "l", Desc: m.t("show / hide the connection log")},
		{Key: "c", Desc: m.t("clear the connection log")},
		{Key: "o", Desc: m.t("turn connection logging off / on")},
		{Key: "L", Desc: m.t("switch language (English / 한국어)")},
		{Key: "g / G", Desc: m.t("jump to top / bottom")},
		{Key: "q, ctrl+c", Desc: quit},
	}
}

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	if m.quitting {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			components.Accent.Render(components.SpinnerFrame(m.frame))+" "+components.Text.Render(m.t("Shutting down RouteBox…")))
	}
	if m.width < minWidth || m.height < minHeight {
		msg := components.Yellow.Bold(true).Render(m.t("Terminal too small")) + "\n" +
			components.Dim.Render(m.t("Minimum recommended size: %d×%d", minWidth, minHeight)) + "\n" +
			components.Dim.Render(m.t("Current: %d×%d", m.width, m.height))
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, msg)
	}

	lay := m.layout()
	header := components.Header(m.status, m.width, m.frame, m.lang)
	for _, w := range lay.warnings {
		header += "\n " + components.Truncate(components.Yellow.Render("⚠ "+m.t(w)), m.width-1)
	}

	var footer string
	if lay.footerBox {
		footer = components.Panel("", components.FooterLines(m.status, m.width-4, maxFootUps, m.now, m.lang), m.width, lay.footerH, false)
	} else {
		footer = components.StatusLine(m.status, m.width, m.lang)
	}
	tail := []string{m.toasts.View(m.width), m.helpBar()}

	if m.modal != modalNone {
		modal := m.modalView()
		mh := lipgloss.Height(modal)
		switch {
		case mh > lay.bodyH+lay.footerH:
			return fitHeight(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal), m.height)
		case mh > lay.bodyH:
			body := lipgloss.Place(m.width, lay.bodyH+lay.footerH, lipgloss.Center, lipgloss.Center, modal)
			return fitHeight(strings.Join(append([]string{header, body}, tail...), "\n"), m.height)
		default:
			body := lipgloss.Place(m.width, lay.bodyH, lipgloss.Center, lipgloss.Center, modal)
			return fitHeight(strings.Join(append([]string{header, body, footer}, tail...), "\n"), m.height)
		}
	}
	return fitHeight(strings.Join(append([]string{header, m.bodyView(lay), footer}, tail...), "\n"), m.height)
}

func (m Model) helpBar() string {
	return components.HelpBar(m.helpEntries(), 2, m.width)
}

func (m Model) bodyView(lay layout) string {
	var cols []string
	if lay.routesShown {
		focused := m.focus == focusRoutes || !lay.logsShown
		title := components.PanelTitle(m.t("Routes"), focused, fmt.Sprintf("%d", len(m.routes)))
		fb := components.Fallback{Via: m.status.Fallback, Hits: m.status.FallbackHits}
		lines := m.routeList.View(m.routes, fb, components.HealthMap(m.status), m.status.RouteHits, lay.routesW-4, lay.bodyH-3, focused, m.lang)
		cols = append(cols, components.Panel(title, lines, lay.routesW, lay.bodyH, focused))
	}
	if lay.logsShown {
		focused := m.focus == focusLogs || !lay.routesShown
		off := !m.status.ConnectionLog
		extra := m.t("live")
		switch {
		case off:
			extra = m.t("off")
		case !m.logView.Following():
			extra = m.t("paused · %d newer", m.logView.Offset)
		}
		title := components.PanelTitle(m.t("Live Connections"), focused, extra)
		lines := m.logView.View(m.conns, off, lay.logsW-4, lay.bodyH-3, m.lang)
		cols = append(cols, components.Panel(title, lines, lay.logsW, lay.bodyH, focused))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols...)
}

func (m Model) modalWidth() int {
	return min(modalMaxW, m.width-4)
}

func (m Model) modalView() string {
	w := m.modalWidth()
	hint := func(pairs ...string) string {
		var parts []string
		for i := 0; i+1 < len(pairs); i += 2 {
			parts = append(parts, components.KeyHint(pairs[i], m.t(pairs[i+1])))
		}
		return strings.Join(parts, "  ")
	}
	switch m.modal {
	case modalAddRoute, modalEditRoute, modalUpstreamForm:
		return m.form.View(w)
	case modalDeleteRoute:
		return components.Confirm(m.t("Remove Route"), m.t("Remove %s?", m.deleting), w, m.lang)
	case modalDeleteUpstream:
		return components.Confirm(m.t("Remove Upstream"), m.t("Remove upstream %s? Its ssh tunnel will be stopped.", m.deleting), w, m.lang)
	case modalUpstreams:
		return m.upstreamsView(w)
	case modalPreset:
		body := components.ModalTitle(m.t("Add Preset")) + "\n\n" + m.presetPicker.View() + "\n\n" + hint("Enter", "add", "Esc", "cancel")
		return components.Box(body, w)
	case modalPresetVia:
		body := components.ModalTitle(m.t("Send these domains via")) + "\n\n" + m.viaPicker.View() + "\n\n" + hint("Enter", "add", "Esc", "back")
		return components.Box(body, w)
	case modalHelp:
		return components.Help(m.helpDetails(), w, m.lang)
	case modalOnboard:
		return m.onboardView(w)
	case modalConfigError:
		body := components.ModalTitle(m.t("Config could not be loaded")) + "\n\n" +
			lipgloss.NewStyle().Width(w-8).Foreground(components.ColorRed).Render(m.opts.LoadErr.Error()) + "\n\n" +
			lipgloss.NewStyle().Width(w-8).Render(components.Dim.Render(
				m.t("The file was left untouched. Continuing starts RouteBox with safe defaults and backs the broken file up before anything is saved."))) +
			"\n\n" + hint("d", "continue with defaults", "q", "quit")
		return components.Box(body, w)
	case modalHostKey:
		return m.hostKeyView(w)
	case modalFatal:
		body := components.ModalTitle(m.t("RouteBox stopped")) + "\n\n" +
			lipgloss.NewStyle().Width(w-8).Foreground(components.ColorRed).Render(m.fatal) + "\n\n" + hint("q", "quit")
		return components.Box(body, w)
	}
	return ""
}

func (m Model) upstreamsView(w int) string {
	inner := w - 8
	var b strings.Builder
	b.WriteString(components.ModalTitle(m.t("Upstreams")) + "\n")
	b.WriteString(components.Dim.Render(m.t("Each upstream is a separate tunnel. Routes pick one with Via.")) + "\n\n")
	if len(m.status.Upstreams) == 0 {
		b.WriteString(components.Dim.Render(m.t("No upstreams yet. Press a to add an SSH host or a SOCKS server.")) + "\n")
	}
	for i, u := range m.status.Upstreams {
		marker := "  "
		name := components.Text.Render(components.Pad(u.Name, 12))
		if i == m.upCursor {
			marker = components.Accent.Render("› ")
			name = components.Accent.Bold(true).Render(components.Pad(u.Name, 12))
		}
		line1 := marker + name + " " + components.Dim.Render(components.UpstreamKind(u, m.lang))
		line2 := "    " + components.Dim.Render("SOCKS ") + components.Text.Render(u.Socks) + "  " +
			components.UpstreamState(u, m.status.Proxy.Running, m.now, m.lang) + "  " +
			components.Dim.Render(components.RouteCount(u.Routes, m.lang))
		b.WriteString(components.Truncate(line1, inner) + "\n" + components.Truncate(line2, inner) + "\n")
		if i == m.upCursor && u.SSH.Err != "" && !u.Healthy() {
			b.WriteString("    " + components.Red.Render(components.Truncate(u.SSH.Err, inner-4)) + "\n")
		}
	}
	keys := []string{"a", "add", "e", "edit", "d", "delete", "r", "restart"}
	if m.upCursor < len(m.status.Upstreams) {
		if u := m.status.Upstreams[m.upCursor]; u.Mode == config.SSHManaged {
			keys = append(keys, "t", "host key")
		}
	}
	keys = append(keys, "Esc", "close")
	var parts []string
	for i := 0; i < len(keys); i += 2 {
		parts = append(parts, components.KeyHint(keys[i], m.t(keys[i+1])))
	}
	b.WriteString("\n" + strings.Join(parts, "  "))
	return components.Box(b.String(), w)
}

// hostKeyView 는 서버의 host key 를 보여 주고 신뢰할지 묻는다. 지문은 잘리면
// 비교할 수 없으므로 줄바꿈만 하고 절대 자르지 않는다.
func (m Model) hostKeyView(w int) string {
	h := m.hostKey
	para := lipgloss.NewStyle().Width(w - 8)
	hint := func(pairs ...string) string {
		var parts []string
		for i := 0; i+1 < len(pairs); i += 2 {
			parts = append(parts, components.KeyHint(pairs[i], m.t(pairs[i+1])))
		}
		return strings.Join(parts, "  ")
	}
	title := components.ModalTitle(m.t("Host key of %s", h.name)) + "\n\n"
	switch {
	case h.scanning:
		return components.Box(title+components.Accent.Render(components.SpinnerFrame(m.frame))+" "+
			components.Text.Render(m.t("Fetching the host key…"))+"\n\n"+hint("Esc", "cancel"), w)
	case errors.Is(h.err, ssh.ErrHostKeyChanged):
		body := title + para.Foreground(components.ColorRed).Bold(true).Render(m.t("The host key has CHANGED since you last connected.")) + "\n\n" +
			para.Render(components.Text.Render(m.t("Someone may be intercepting the connection, so RouteBox will not offer to trust it. If the server was reinstalled, confirm the new key with its administrator, remove the old one and try again:"))) + "\n\n" +
			para.Render(components.Accent.Render("ssh-keygen -R "+m.hostKeyHost())) + "\n\n" + hint("Esc", "close")
		return components.Box(body, w)
	case errors.Is(h.err, ssh.ErrHostKeyKnown):
		body := title + para.Render(components.Text.Render(m.t("This host key is already trusted; the connection fails for another reason. Check the error in the upstream list or run ssh in a terminal."))) +
			"\n\n" + hint("Esc", "close")
		return components.Box(body, w)
	case h.err != nil:
		body := title + para.Foreground(components.ColorRed).Render(h.err.Error()) + "\n\n" + hint("Esc", "close")
		return components.Box(body, w)
	}
	var fps []string
	for _, f := range h.key.Fingerprints {
		fps = append(fps, para.Render(components.Accent.Bold(true).Render(f)))
	}
	body := title +
		para.Render(components.Text.Render(m.t("%s has not been verified yet. The server presented:", h.key.Host))) + "\n\n" +
		strings.Join(fps, "\n") + "\n\n" +
		para.Render(components.Dim.Render(m.t("Compare it with the fingerprint shown on the server, e.g. ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub"))) + "\n" +
		para.Render(components.Dim.Render(m.t("Trusting saves it to %s and reconnects.", tildePath(h.key.KnownHosts)))) + "\n\n" +
		hint("y", "trust", "n", "cancel")
	return components.Box(body, w)
}

// tildePath 는 홈 디렉터리 아래 경로를 ~ 로 줄인다.
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func (m Model) hostKeyHost() string {
	if u, ok := m.app.Config().Upstream(m.hostKey.name); ok {
		return u.Host
	}
	return m.hostKey.name
}

func (m Model) onboardView(w int) string {
	o := m.onboard
	brand := lipgloss.NewStyle().Foreground(components.ColorAccent).Bold(true).Render("RouteBox") + "\n" +
		components.Dim.Render(m.t("Selective Tunnel Router"))
	switch o.step {
	case onboardForm:
		return o.form.View(w)
	case onboardWaiting:
		body := brand + "\n\n" + components.Accent.Render(components.SpinnerFrame(m.frame)) + " " +
			components.Text.Render(m.t("Connecting %s…", o.name)) + "\n\n" + components.KeyHint("Esc", m.t("close"))
		return components.Box(body, w)
	case onboardResult:
		if o.ok {
			return components.Box(brand+"\n\n"+components.Green.Render("✓ ")+components.Text.Render(m.t("Connected to %s", o.name)), w)
		}
		title := m.t("SSH connection failed")
		if !o.managed {
			title = m.t("SOCKS server unreachable")
		}
		next := m.t("RouteBox keeps retrying in the background.")
		if ssh.IsHostKeyFailure(o.detail) {
			next = m.t("The server's host key is not trusted yet. Press Enter, then t to check it.")
		}
		body := brand + "\n\n" + components.Red.Render("✗ "+title) + "\n" +
			lipgloss.NewStyle().Width(w-10).PaddingLeft(2).Foreground(components.ColorText).Render(o.detail) + "\n\n" +
			lipgloss.NewStyle().Width(w-8).Render(components.Dim.Render(next)) + "\n\n" +
			components.KeyHint("Enter", m.t("open upstreams")) + "  " + components.KeyHint("Esc", m.t("close"))
		return components.Box(body, w)
	default:
		body := brand + "\n\n" + components.Text.Render(m.t("Route the domains you choose through the tunnels you choose.")) + "\n" +
			components.Dim.Render(m.t("Start by adding your first upstream:")) + "\n\n" + o.chooser.View() + "\n\n" +
			components.KeyHint("Enter", m.t("select")) + "  " + components.KeyHint("Esc", m.t("skip for now"))
		return components.Box(body, w)
	}
}

// fitHeight 는 터미널이 스크롤되지 않도록 s 를 최대 h 줄로 자른다.
func fitHeight(s string, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}
