package components

import (
	"fmt"

	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/tui/i18n"
)

// RouteList 는 routes 패널의 커서 상태다. len(routes) 번째 행은 "+ Add Route" 동작이다.
type RouteList struct {
	Cursor int
	Offset int
}

// Rows 는 라우트 n 개일 때 선택 가능한 행 수(추가 행 포함)다.
func Rows(n int) int { return n + 1 }

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

func (l RouteList) OnAddRow(n int) bool { return l.Cursor == n }

// View 는 최대 height 행을 너비 w 로 그린다. health 는 업스트림 이름별 상태,
// hits 는 route 도메인별 hit 수다.
func (l RouteList) View(routes []router.Route, health map[string]UpHealth, hits map[string]int64, w, height int, focused bool, lang i18n.Lang) []string {
	if len(routes) == 0 && height >= 4 {
		lines := []string{
			Dim.Render(lang.T("No routes yet.")),
			"",
			Dim.Render(lang.T("Press a to route a domain,")),
			Dim.Render(lang.T("or p to add a preset.")),
			"",
		}
		return append(lines, l.addRow(0, w, focused, lang))
	}
	var lines []string
	end := min(Rows(len(routes)), l.Offset+height)
	for i := l.Offset; i < end; i++ {
		if i == len(routes) {
			lines = append(lines, l.addRow(i, w, focused, lang))
			continue
		}
		lines = append(lines, l.routeRow(routes[i], health, hits[routes[i].Domain], i, w, focused, lang))
	}
	return lines
}

// hitW 는 hit 열 너비다. CompactCount 는 999G 까지 이 안에 들어간다.
const hitW = 4

func (l RouteList) routeRow(r router.Route, health map[string]UpHealth, hits int64, i, w int, focused bool, lang i18n.Lang) string {
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
	name := Truncate(r.Domain, max(1, nameW))
	if selected {
		name = Bold.Foreground(ColorText).Render(name)
	} else {
		name = Text.Render(name)
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
