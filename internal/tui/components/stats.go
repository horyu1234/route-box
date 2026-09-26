package components

import (
	"fmt"

	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/tui/i18n"
)

// StatsInline 은 연결 카운터와 전송량을 한 줄로 그린다.
func StatsInline(st core.Status, lang i18n.Lang) string {
	n := func(label string, v int64) string {
		return Dim.Render(lang.T(label)+" ") + Text.Render(fmt.Sprint(v))
	}
	failed := n("Failed", st.Stats.Failed)
	if st.Stats.Failed > 0 {
		failed = Dim.Render(lang.T("Failed")+" ") + Red.Render(fmt.Sprint(st.Stats.Failed))
	}
	return n("Active", st.Stats.Active) + "  " + n("Total", st.Stats.Total) + "  " +
		Dim.Render(lang.T("Proxied")+" ") + Accent.Render(fmt.Sprint(st.Stats.Proxied)) + "  " +
		Dim.Render(lang.T("Direct")+" ") + Gray.Render(fmt.Sprint(st.Stats.Direct)) + "  " + failed +
		Dim.Render("   ↓ ") + Text.Render(HumanBytes(st.Stats.RX)) +
		Dim.Render("  ↑ ") + Text.Render(HumanBytes(st.Stats.TX))
}
