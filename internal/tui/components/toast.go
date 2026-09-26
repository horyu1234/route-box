package components

import (
	"strings"
	"time"
)

type ToastKind int

const (
	ToastSuccess ToastKind = iota
	ToastInfo
	ToastWarn
	ToastError
)

type Toast struct {
	Text    string
	Kind    ToastKind
	Expires time.Time
}

// Toasts 는 일시적인 메시지들의 짧은 큐다; 가장 최신 것이 표시된다.
type Toasts struct {
	items []Toast
}

func (t *Toasts) Push(text string, kind ToastKind, now time.Time) {
	ttl := 2500 * time.Millisecond
	if kind == ToastError || kind == ToastWarn {
		ttl = 4 * time.Second
	}
	text = strings.Join(strings.Fields(text), " ")
	t.items = append(t.items, Toast{Text: text, Kind: kind, Expires: now.Add(ttl)})
	if len(t.items) > 3 {
		t.items = t.items[len(t.items)-3:]
	}
}

func (t *Toasts) Expire(now time.Time) {
	kept := t.items[:0]
	for _, it := range t.items {
		if now.Before(it.Expires) {
			kept = append(kept, it)
		}
	}
	t.items = kept
}

func (t Toasts) Len() int { return len(t.items) }

// View 는 가장 최신 토스트를 한 줄로 렌더링하고, 없으면 "" 를 반환한다.
func (t Toasts) View(w int) string {
	if len(t.items) == 0 {
		return ""
	}
	it := t.items[len(t.items)-1]
	var s string
	switch it.Kind {
	case ToastSuccess:
		s = Green.Render("✓ ") + Text.Render(it.Text)
	case ToastError:
		s = Red.Render("✗ ") + Text.Render(it.Text)
	case ToastWarn:
		s = Yellow.Render("! ") + Text.Render(it.Text)
	default:
		s = Accent.Render("• ") + Text.Render(it.Text)
	}
	return " " + Truncate(s, w-1)
}
