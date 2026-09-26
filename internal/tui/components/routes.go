package components

import (
	"fmt"

	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/tui/i18n"
)

// RouteList 는 routes 패널의 커서 상태다. route 들 다음 len(routes) 번째 행은
// fallback("그 외 모든 트래픽"), 그다음 행은 "+ Add Route" 동작이다.
type RouteList struct {
	Cursor int
	Offset int
}

// Rows 는 라우트 n 개일 때 선택 가능한 행 수(fallback 행과 추가 행 포함)다.
func Rows(n int) int { return n + 2 }

// Fallback 은 routes 패널에 보일 fallback 행의 내용이다.
type Fallback struct {
	Via  string // 업스트림 이름 또는 "direct"
	Hits int64
}

// Route 는 fallback 을 ViaTag 가 그릴 수 있는 route 모양으로 바꾼다.
func (f Fallback) Route() router.Route {
	if f.Via == router.ViaDirect || f.Via == "" {
		return router.Route{Mode: router.ModeDirect}
	}
	return router.Route{Mode: router.ModeProxy, Upstream: f.Via}
}

func (l *RouteList) Move(delta, rows, height int) {
	l.Cursor = max(0, min(rows-1, l.Cursor+delta))
	l.Ensure(rows, height)
}

// Ensure 는 커서를 범위 안에 두고 보이도록 최소한만 스크롤한다.
func (l *RouteList) Ensure(rows, height int) {
	l.Cursor = max(0, min(rows-1, l.Cursor))
	if height < 1 {
		return
	}
	if l.Cursor < l.Offset {
		l.Offset = l.Cursor
	}
	if l.Cursor >= l.Offset+height {
		l.Offset = l.Cursor - height + 1
	}
	l.Offset = max(0, min(l.Offset, max(0, rows-height)))
}

func (l RouteList) OnFallbackRow(n int) bool { return l.Cursor == n }
func (l RouteList) OnAddRow(n int) bool      { return l.Cursor == n+1 }

// View 는 최대 height 행을 너비 w 로 그린다. health 는 업스트림 이름별 상태,
// hits 는 route 도메인별 hit 수다.
func (l RouteList) View(routes []router.Route, fb Fallback, health map[string]UpHealth, hits map[string]int64, w, height int, focused bool, lang i18n.Lang) []string {
	n := len(routes)
	if n == 0 && height >= 7 {
		lines := []string{
			Dim.Render(lang.T("No routes yet.")),
			"",
			Dim.Render(lang.T("Press a to route a domain,")),
			Dim.Render(lang.T("or p to add a preset.")),
			"",
		}
		return append(lines, l.fallbackRow(fb, health, 0, w, focused, lang), l.addRow(1, w, focused, lang))
	}
	var lines []string
	end := min(Rows(n), l.Offset+height)
	for i := l.Offset; i < end; i++ {
		switch {
		case i == n:
			lines = append(lines, l.fallbackRow(fb, health, i, w, focused, lang))
		case i == n+1:
			lines = append(lines, l.addRow(i, w, focused, lang))
		default:
			r := routes[i]
			lines = append(lines, l.routeRow(Text.Render, r.Domain, r, health, hits[r.Domain], i, w, focused, lang))
		}
	}
	return lines
}

func (l RouteList) fallbackRow(fb Fallback, health map[string]UpHealth, i, w int, focused bool, lang i18n.Lang) string {
	return l.routeRow(Dim.Render, lang.T("everything else"), fb.Route(), health, fb.Hits, i, w, focused, lang)
}

// hitW 는 hit 열 너비다. CompactCount 는 999G 까지 이 안에 들어간다.
const hitW = 4

// routeRow 는 route 한 행을 그린다. style 은 선택되지 않았을 때 이름에 쓰인다.
func (l RouteList) routeRow(style func(...string) string, label string, r router.Route, health map[string]UpHealth, hits int64, i, w int, focused bool, lang i18n.Lang) string {
	dot, tag := Accent.Render("●"), ViaTag(r, health, lang)
	if r.Mode == router.ModeDirect {
		dot = Gray.Render("○")
	}
	selected := i == l.Cursor && focused
	tagW := min(14, max(8, w/3))
	nameW := w - 3 - tagW - 1
	right := Truncate(tag, tagW)
	// 도메인 이름이 너무 좁아지지 않을 때만 hit 열을 보인다.
	if nameW-hitW-1 >= 10 {
		nameW -= hitW + 1
		st := Dim
		if hits > 0 {
			st = Text
		}
		right += " " + st.Render(fmt.Sprintf("%*s", hitW, CompactCount(hits)))
	}
	name := Truncate(label, max(1, nameW))
	if selected {
		name = Bold.Foreground(ColorText).Render(name)
	} else {
		name = style(name)
	}
	row := Spread(dot+" "+name, right, w-1)
	return l.decorate(row, i, w, focused)
}

// CompactCount 는 n 을 4 셀 이내로 줄여 쓴다: 9999, 10k, 999k, 1M ….
func CompactCount(n int64) string {
	switch {
	case n < 10_000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1_000)
	case n < 1_000_000_000:
		return fmt.Sprintf("%dM", n/1_000_000)
	default:
		return fmt.Sprintf("%dG", n/1_000_000_000)
	}
}

// ViaTag 는 route 가 어디로 나가는지를 업스트림 상태 색으로 보여 준다.
func ViaTag(r router.Route, health map[string]UpHealth, lang i18n.Lang) string {
	switch {
	case r.Mode == router.ModeDirect:
		return Gray.Render("DIRECT")
	case r.Upstream == "":
		return Red.Render(lang.T("no upstream"))
	}
	st := Accent
	switch health[r.Upstream] {
	case UpConnecting:
		st = Yellow
	case UpDown, UpMissing:
		st = Red
	}
	return st.Render("→ " + r.Upstream)
}

func (l RouteList) addRow(i, w int, focused bool, lang i18n.Lang) string {
	return l.decorate(Dim.Render(lang.T("+ Add Route")), i, w, focused)
}

func (l RouteList) decorate(row string, i, w int, focused bool) string {
	if i == l.Cursor && focused {
		return Accent.Render("▌") + Selected.Render(Pad(row, w-1))
	}
	if i == l.Cursor {
		return Dim.Render("▏") + Pad(row, w-1)
	}
	return " " + Pad(row, w-1)
}
