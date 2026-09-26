package components

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/ssh"
	"github.com/horyu1234/route-box/internal/tui/i18n"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// SpinnerFrame 은 tick n 에 해당하는 애니메이션 프레임을 돌려준다.
func SpinnerFrame(n int) string { return spinnerFrames[n%len(spinnerFrames)] }

// UpHealth 는 route 목록에서 업스트림 이름을 칠할 색을 정하는 상태다.
type UpHealth int

const (
	UpOK UpHealth = iota
	UpConnecting
	UpDown
	UpMissing
)

// HealthMap 은 상태에서 업스트림 이름별 UpHealth 를 만든다.
func HealthMap(st core.Status) map[string]UpHealth {
	m := make(map[string]UpHealth, len(st.Upstreams))
	for _, u := range st.Upstreams {
		switch {
		case !st.Proxy.Running || u.Healthy():
			m[u.Name] = UpOK
		case u.Connecting() || u.Health.Checked.IsZero():
			m[u.Name] = UpConnecting
		default:
			m[u.Name] = UpDown
		}
	}
	return m
}

func BadgeColor(b core.Badge) lipgloss.Color {
	switch b {
	case core.BadgeConnected:
		return ColorGreen
	case core.BadgeDegraded, core.BadgeReconnecting:
		return ColorYellow
	default:
		return ColorRed
	}
}

// Badge 는 전체 상태 표시를 그린다.
func Badge(b core.Badge, frame int, lang i18n.Lang) string {
	icon := "●"
	switch b {
	case core.BadgeReconnecting:
		icon = SpinnerFrame(frame)
	case core.BadgeDegraded:
		icon = "▲"
	case core.BadgeDisconnected:
		icon = "○"
	}
	return lipgloss.NewStyle().Foreground(BadgeColor(b)).Bold(true).Render(icon + " " + lang.T(string(b)))
}

// Header 는 앱 제목과 오른쪽 상태 배지로 된 두 줄이다.
func Header(st core.Status, w, frame int, lang i18n.Lang) string {
	title := " " + lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("RouteBox")
	sub := " " + Dim.Render(lang.T("Selective Tunnel Router"))
	right2 := ""
	if st.Proxy.Running {
		right2 = Dim.Render(lang.T("listening on")+" ") + Text.Render(st.Proxy.Listen)
	} else if st.Proxy.Err != "" {
		right2 = Red.Render(lang.T("proxy stopped"))
	}
	return Spread(title, Badge(st.Badge, frame, lang)+" ", w) + "\n" + Spread(sub, right2+" ", w)
}

// UpstreamState 는 업스트림 하나의 상태를 색이 입혀진 짧은 말로 요약한다.
func UpstreamState(u core.UpstreamStatus, running bool, now time.Time, lang i18n.Lang) string {
	if !running {
		return Dim.Render(lang.T("idle"))
	}
	if u.Mode == config.SSHExternal {
		switch {
		case u.Health.Reachable:
			return Green.Render(lang.T("reachable"))
		case u.Health.Checked.IsZero():
			return Dim.Render(lang.T("checking…"))
		default:
			return Red.Render(lang.T("unreachable"))
		}
	}
	switch u.SSH.State {
	case ssh.StateConnected:
		return Green.Render(lang.T("connected")) + Dim.Render(fmt.Sprintf(" · PID %d", u.SSH.PID))
	case ssh.StateStarting:
		return Yellow.Render(lang.T("connecting…"))
	case ssh.StateReconnecting:
		if wait := u.SSH.NextRetry.Sub(now); wait > 0 {
			return Yellow.Render(lang.T("retry in %ds", int(wait.Seconds()+0.99)))
		}
		return Yellow.Render(lang.T("reconnecting…"))
	case ssh.StateFailed:
		return Red.Render(lang.T("failed"))
	default:
		return Dim.Render(lang.T("idle"))
	}
}

// UpstreamKind 는 "ssh host" 또는 "SOCKS" 처럼 업스트림 종류를 보여 준다.
func UpstreamKind(u core.UpstreamStatus, lang i18n.Lang) string {
	if u.Mode == config.SSHExternal {
		return lang.T("external SOCKS")
	}
	return "ssh " + u.Host
}

// UpstreamRow 는 업스트림 한 줄: 이름, 종류, SOCKS 주소, 상태, route 수.
func UpstreamRow(u core.UpstreamStatus, running bool, w int, now time.Time, lang i18n.Lang) string {
	dot := Green.Render("●")
	switch HealthMap(core.Status{Proxy: core.ProxyStatus{Running: running}, Upstreams: []core.UpstreamStatus{u}})[u.Name] {
	case UpConnecting:
		dot = Yellow.Render("●")
	case UpDown:
		dot = Red.Render("●")
	}
	if !running {
		dot = Dim.Render("○")
	}
	nameW := 12
	kindW := max(10, min(24, w/4))
	left := dot + " " + Pad(Bold.Foreground(ColorText).Render(u.Name), nameW) + " " +
		Pad(Dim.Render(UpstreamKind(u, lang)), kindW) + " " +
		Pad(Dim.Render("SOCKS ")+Text.Render(u.Socks), 22) + " " + UpstreamState(u, running, now, lang)
	return Spread(left, Dim.Render(RouteCount(u.Routes, lang)), w)
}

// FooterLines 는 하단 패널: 요약 한 줄과 업스트림별 한 줄(최대 maxUps 개).
func FooterLines(st core.Status, w, maxUps int, now time.Time, lang i18n.Lang) []string {
	up := "—"
	if !st.StartedAt.IsZero() {
		up = HumanDuration(st.Uptime().Truncate(time.Second))
	}
	head := Dim.Render("HTTP ") + Text.Render(st.Proxy.Listen) + Dim.Render("  ·  "+lang.T("up")+" ") + Text.Render(up) +
		Dim.Render("  ·  ") + StatsInline(st, lang)
	lines := []string{Truncate(head, w)}
	if len(st.Upstreams) == 0 {
		return append(lines, Yellow.Render(lang.T("No upstreams yet — press s to add an SSH host or SOCKS server.")))
	}
	for i, u := range st.Upstreams {
		if i == maxUps-1 && len(st.Upstreams) > maxUps {
			lines = append(lines, Dim.Render(lang.T("+%d more — press s", len(st.Upstreams)-i)))
			break
		}
		lines = append(lines, UpstreamRow(u, st.Proxy.Running, w, now, lang))
	}
	return lines
}

// StatusLine 은 좁거나 낮은 터미널에서 쓰는 한 줄짜리 하단 표시다.
func StatusLine(st core.Status, w int, lang i18n.Lang) string {
	ok := 0
	for _, u := range st.Upstreams {
		if u.Healthy() {
			ok++
		}
	}
	s := Text.Render(lang.T("upstreams %d/%d ok", ok, len(st.Upstreams))) +
		Dim.Render("  ·  "+lang.T("Active")+" ") + Text.Render(fmt.Sprint(st.Stats.Active)) +
		Dim.Render("  ↓") + Text.Render(HumanBytes(st.Stats.RX)) +
		Dim.Render(" ↑") + Text.Render(HumanBytes(st.Stats.TX))
	return " " + Truncate(s, w-1)
}

// RouteCount 는 "1 route" / "3 routes" 를 언어에 맞게 쓴다.
func RouteCount(n int, lang i18n.Lang) string {
	if n == 1 {
		return lang.T("%d route", n)
	}
	return lang.T("%d routes", n)
}
