// Package components 는 RouteBox TUI 조각들을 렌더링한다. 모든 컴포넌트는
// 입력에 대한 순수 함수이거나(또는 View 메서드를 가진 작은 값 타입) 이므로
// 실행 중인 프로그램 없이도 테스트할 수 있다.
package components

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	ColorAccent = lipgloss.Color("45")
	ColorBlue   = lipgloss.Color("39")
	ColorGreen  = lipgloss.Color("42")
	ColorYellow = lipgloss.Color("214")
	ColorRed    = lipgloss.Color("203")
	ColorGray   = lipgloss.Color("245")
	ColorDim    = lipgloss.Color("241")
	ColorBorder = lipgloss.Color("238")
	ColorText   = lipgloss.Color("252")
	ColorSelBg  = lipgloss.Color("236")
)

var (
	Bold     = lipgloss.NewStyle().Bold(true)
	Accent   = lipgloss.NewStyle().Foreground(ColorAccent)
	Blue     = lipgloss.NewStyle().Foreground(ColorBlue)
	Green    = lipgloss.NewStyle().Foreground(ColorGreen)
	Yellow   = lipgloss.NewStyle().Foreground(ColorYellow)
	Red      = lipgloss.NewStyle().Foreground(ColorRed)
	Gray     = lipgloss.NewStyle().Foreground(ColorGray)
	Dim      = lipgloss.NewStyle().Foreground(ColorDim)
	Text     = lipgloss.NewStyle().Foreground(ColorText)
	Label    = lipgloss.NewStyle().Foreground(ColorDim).Bold(true)
	Selected = lipgloss.NewStyle().Background(ColorSelBg).Foreground(ColorText).Bold(true)
)

// Truncate 는 s(ANSI 스타일을 포함할 수 있다)를 w 셀로 자르고 말줄임표를 붙인다.
func Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// Pad 는 s 를 정확히 w 셀이 되도록 공백으로 채우고, 필요하면 자른다.
func Pad(s string, w int) string {
	s = Truncate(s, w)
	if n := lipgloss.Width(s); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

// Spread 는 left 와 right 를 폭 w 인 한 줄에 배치한다.
func Spread(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return Truncate(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// Panel 은 정확히 w×h 셀 크기의 둥근 박스를 타이틀 줄과 함께 그린다.
func Panel(title string, lines []string, w, h int, focused bool) string {
	innerW, innerH := w-4, h-2
	if innerW < 1 || innerH < 1 {
		return ""
	}
	content := make([]string, 0, innerH)
	if title != "" {
		content = append(content, title)
	}
	content = append(content, lines...)
	if len(content) > innerH {
		content = content[:innerH]
	}
	for i := range content {
		content[i] = Pad(content[i], innerW)
	}
	for len(content) < innerH {
		content = append(content, strings.Repeat(" ", innerW))
	}
	border := ColorBorder
	if focused {
		border = ColorAccent
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Render(strings.Join(content, "\n"))
}

// PanelTitle 은 대문자 섹션 제목을 선택적 카운트와 함께 렌더링한다.
func PanelTitle(name string, focused bool, extra string) string {
	st := Label
	if focused {
		st = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	}
	s := st.Render(strings.ToUpper(name))
	if extra != "" {
		s += " " + Dim.Render(extra)
	}
	return s
}

func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}

func HumanDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
