package components

import (
	"net"
	"strings"

	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/tui/i18n"
)

// LogView 는 연결 로그를 스크롤한다. Offset 은 최신 항목부터 위로 센 행 수이며
// 0 이면 새 연결을 따라간다.
type LogView struct {
	Offset int
}

func (v *LogView) Scroll(delta, total, height int) {
	v.Offset = max(0, min(max(0, total-height), v.Offset+delta))
}

func (v LogView) Following() bool { return v.Offset == 0 }

// View 는 최신 항목을 맨 아래에 두고 height 행을 너비 w 로 그린다. off 는
// 연결 기록이 꺼져 있다는 뜻이다.
func (v LogView) View(entries []events.ConnectionEvent, off bool, w, height int, lang i18n.Lang) []string {
	if len(entries) == 0 && off {
		return []string{
			Dim.Render(lang.T("The connection log is off.")),
			"",
			Dim.Render(lang.T("Press o to turn it back on.")),
		}
	}
	if len(entries) == 0 {
		return []string{
			Dim.Render(lang.T("Waiting for connections…")),
			"",
			Dim.Render(lang.T("Point your browser's HTTP proxy at RouteBox;")),
			Dim.Render(lang.T("traffic shows up here as it happens.")),
		}
	}
	end := len(entries) - v.Offset
	start := max(0, end-height)
	lines := make([]string, 0, end-start)
	for _, e := range entries[start:end] {
		lines = append(lines, logRow(e, w))
	}
	return lines
}

const viaColW = 8

func logRow(e events.ConnectionEvent, w int) string {
	ts := Dim.Render(e.Time.Local().Format("15:04:05"))
	via := Gray.Render(Pad("DIRECT", viaColW))
	if e.Route == router.ModeProxy {
		name := e.Upstream
		if name == "" {
			name = "PROXY"
		}
		via = Accent.Render(Pad(name, viaColW))
	}
	target := net.JoinHostPort(e.Host, e.Port)
	if e.Method == "HTTP" {
		target = "http " + target
	}

	var status string
	switch e.State {
	case events.ConnOpen:
		status = Blue.Render("●") + Dim.Render(" open")
	case events.ConnClosed:
		status = Green.Render("✓") + Dim.Render(" "+HumanDuration(e.Duration)+" "+HumanBytes(e.BytesIn+e.BytesOut))
	case events.ConnFailed:
		status = Red.Render("✗ " + shortError(e.Error))
	}

	left := ts + " " + via + " "
	fixed := 8 + 1 + viaColW + 1
	statusW := min(20, max(0, w-fixed-18))
	hostW := w - fixed - statusW - 1
	if statusW < 6 {
		return left + Truncate(Text.Render(target), w-fixed)
	}
	return left + Pad(Text.Render(Truncate(target, hostW)), hostW) + " " + Truncate(status, statusW)
}

// shortError 는 오류 체인의 끝부분("connection refused", "no such host")만 남긴다.
func shortError(err error) string {
	if err == nil {
		return "failed"
	}
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		msg = msg[i+2:]
	}
	return msg
}
