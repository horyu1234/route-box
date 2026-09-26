package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/horyu1234/route-box/internal/tui/i18n"
)

// Box 는 modal 내용을 accent 색 테두리로 감싼다.
func Box(content string, w int) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(w - 2).
		Render(content)
}

func ModalTitle(s string) string {
	return lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render(s)
}

func KeyHint(key, label string) string {
	return Accent.Render("["+key+"]") + " " + Dim.Render(label)
}

type FieldKind int

const (
	FieldText FieldKind = iota
	FieldChoice
)

type Field struct {
	// Key 는 번역과 무관한 필드 식별자다.
	Key     string
	Label   string
	Hint    string
	Kind    FieldKind
	Input   textinput.Model
	Options []string
	Choice  int
	// Group 은 폼의 첫 choice 필드 값이 이것과 일치할 때만 필드를 보여준다.
	Group string
}

func TextField(key, label, value, placeholder, hint string) Field {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = placeholder
	in.SetValue(value)
	in.CharLimit = 255
	in.PlaceholderStyle = Dim
	in.TextStyle = Text
	in.Cursor.Style = Accent
	return Field{Key: key, Label: label, Hint: hint, Kind: FieldText, Input: in}
}

func ChoiceField(key, label string, options []string, selected int, hint string) Field {
	return Field{Key: key, Label: label, Hint: hint, Kind: FieldChoice, Options: options, Choice: selected}
}

// Form 은 라벨이 붙은 필드들의 수직 목록이다. Enter 로 제출, Esc 로 취소하며
// Tab/↑/↓ 로 필드 간 이동, ←/→/space 로 choice 를 바꾼다.
type Form struct {
	Lang   i18n.Lang
	Title  string
	Intro  string
	Fields []Field
	Focus  int
	Err    string
}

func NewForm(lang i18n.Lang, title string, fields ...Field) *Form {
	f := &Form{Lang: lang, Title: title, Fields: fields}
	f.focus(0)
	return f
}

// Get 은 Key 로 필드 값을 찾는다. 숨겨진 필드도 값을 돌려준다.
func (f *Form) Get(key string) (string, bool) {
	for i, fl := range f.Fields {
		if fl.Key == key {
			return f.Value(i), true
		}
	}
	return "", false
}

func (f *Form) Value(i int) string {
	if f.Fields[i].Kind == FieldChoice {
		return f.Fields[i].Options[f.Fields[i].Choice]
	}
	return strings.TrimSpace(f.Fields[i].Input.Value())
}

func (f *Form) visible(i int) bool {
	g := f.Fields[i].Group
	if g == "" {
		return true
	}
	for _, fl := range f.Fields {
		if fl.Kind == FieldChoice && fl.Group == "" {
			return fl.Options[fl.Choice] == g
		}
	}
	return true
}

func (f *Form) focus(i int) {
	for j := range f.Fields {
		f.Fields[j].Input.Blur()
	}
	f.Focus = i
	if f.Fields[i].Kind == FieldText {
		f.Fields[i].Input.Focus()
	}
}

func (f *Form) step(delta int) {
	n := len(f.Fields)
	for i, j := 0, f.Focus; i < n; i++ {
		j = (j + delta + n) % n
		if f.visible(j) {
			f.focus(j)
			return
		}
	}
}

type FormResult int

const (
	FormActive FormResult = iota
	FormSubmitted
	FormCancelled
)

func (f *Form) Update(msg tea.KeyMsg) (FormResult, tea.Cmd) {
	fl := &f.Fields[f.Focus]
	switch msg.String() {
	case "esc":
		return FormCancelled, nil
	case "enter":
		return FormSubmitted, nil
	case "tab", "down":
		f.step(1)
		return FormActive, nil
	case "shift+tab", "up":
		f.step(-1)
		return FormActive, nil
	}
	if fl.Kind == FieldChoice {
		switch msg.String() {
		case "left", "h":
			fl.Choice = (fl.Choice - 1 + len(fl.Options)) % len(fl.Options)
		case "right", "l", " ":
			fl.Choice = (fl.Choice + 1) % len(fl.Options)
		}
		return FormActive, nil
	}
	f.Err = ""
	var cmd tea.Cmd
	fl.Input, cmd = fl.Input.Update(msg)
	return FormActive, cmd
}

func (f *Form) View(w int) string {
	const labelW = 15
	inner := w - 8
	ctrlW := inner - 2 - labelW
	var b strings.Builder
	b.WriteString(ModalTitle(f.Title) + "\n")
	if f.Intro != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Width(inner).Render(f.Intro) + "\n")
	}
	b.WriteString("\n")
	for i := range f.Fields {
		if !f.visible(i) {
			continue
		}
		fl := &f.Fields[i]
		focused := i == f.Focus
		marker, label := "  ", Label.Render(Pad(fl.Label, labelW))
		if focused {
			marker = Accent.Render("› ")
			label = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render(Pad(fl.Label, labelW))
		}
		var ctrl string
		if fl.Kind == FieldChoice {
			var opts []string
			for j, o := range fl.Options {
				if j == fl.Choice {
					opts = append(opts, lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("● "+o))
				} else {
					opts = append(opts, Dim.Render("○ "+o))
				}
			}
			ctrl = strings.Join(opts, "  ")
		} else {
			fl.Input.Width = max(4, ctrlW-6)
			frame := lipgloss.NewStyle().Foreground(ColorBorder)
			if focused {
				frame = frame.Foreground(ColorAccent)
			}
			ctrl = frame.Render("[ ") + Pad(fl.Input.View(), ctrlW-4) + frame.Render(" ]")
		}
		b.WriteString(marker + label + ctrl + "\n")
		if fl.Hint != "" && focused {
			b.WriteString(strings.Repeat(" ", 2+labelW) + Dim.Render(Truncate(fl.Hint, ctrlW)) + "\n")
		}
	}
	if f.Err != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(ColorRed).Width(inner).Render("✗ "+f.Err) + "\n")
	}
	hints := []string{KeyHint("Enter", f.Lang.T("save")), KeyHint("Esc", f.Lang.T("cancel"))}
	if len(f.Fields) > 1 {
		hints = append(hints, KeyHint("Tab", f.Lang.T("next")))
	}
	b.WriteString("\n" + strings.Join(hints, "  "))
	return Box(b.String(), w)
}

// Confirm 은 yes/no 질문을 렌더링한다.
func Confirm(title, question string, w int, lang i18n.Lang) string {
	body := ModalTitle(title) + "\n\n" + lipgloss.NewStyle().Width(w-8).Render(Text.Render(question)) + "\n\n" +
		KeyHint("y", lang.T("Yes")) + "   " + KeyHint("n", lang.T("No"))
	return Box(body, w)
}

// ChooserItem 은 선택 목록의 한 줄이다. Header 는 선택할 수 없는 구분 제목이다.
type ChooserItem struct {
	Label  string
	Detail string
	Value  string
	Header bool
}

// Chooser 는 단일 선택 세로 목록이다. 커서는 제목 줄을 건너뛴다.
type Chooser struct {
	Items  []ChooserItem
	Cursor int
}

func NewChooser(items ...ChooserItem) Chooser {
	c := Chooser{Items: items}
	c.Cursor = -1
	c.Move(1)
	return c
}

// Options 는 제목 없는 단순 목록을 만든다.
func Options(labels ...string) Chooser {
	items := make([]ChooserItem, len(labels))
	for i, l := range labels {
		items[i] = ChooserItem{Label: l, Value: l}
	}
	return NewChooser(items...)
}

func (c *Chooser) Move(delta int) {
	for i := c.Cursor + delta; i >= 0 && i < len(c.Items); i += delta {
		if !c.Items[i].Header {
			c.Cursor = i
			return
		}
	}
}

func (c Chooser) Selected() ChooserItem {
	if c.Cursor < 0 || c.Cursor >= len(c.Items) {
		return ChooserItem{}
	}
	return c.Items[c.Cursor]
}

func (c Chooser) View() string {
	var lines []string
	for i, it := range c.Items {
		switch {
		case it.Header:
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, Label.Render(it.Label))
		case i == c.Cursor:
			lines = append(lines, Accent.Render("› ")+lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render(Pad(it.Label, 14))+Dim.Render(it.Detail))
		default:
			lines = append(lines, "  "+Text.Render(Pad(it.Label, 14))+Dim.Render(it.Detail))
		}
	}
	return strings.Join(lines, "\n")
}

// HelpEntry 는 help overlay 의 키 바인딩 하나다.
type HelpEntry struct{ Key, Desc string }

func Help(entries []HelpEntry, w int, lang i18n.Lang) string {
	var b strings.Builder
	b.WriteString(ModalTitle(lang.T("Keyboard shortcuts")) + "\n\n")
	for _, e := range entries {
		b.WriteString(Accent.Render(Pad(e.Key, 12)) + Text.Render(e.Desc) + "\n")
	}
	b.WriteString("\n" + KeyHint("Esc", lang.T("close")))
	return Box(b.String(), w)
}

// HelpBar 는 하단 키 범례를 렌더링한다. 마지막 `pinned` 개 항목(help, quit)은
// 항상 남고, 공간이 부족하면 중간 항목부터 빠진다.
func HelpBar(entries []HelpEntry, pinned, w int) string {
	render := func(e HelpEntry) string {
		return Accent.Render("["+e.Key+"]") + " " + Dim.Render(e.Desc)
	}
	pinned = min(pinned, len(entries))
	var tail []string
	for _, e := range entries[len(entries)-pinned:] {
		tail = append(tail, render(e))
	}
	tailStr := strings.Join(tail, "  ")
	budget := w - 1 - lipgloss.Width(tailStr) - 2
	var head []string
	used := 0
	for _, e := range entries[:len(entries)-pinned] {
		item := render(e)
		if used+lipgloss.Width(item)+2 > budget {
			break
		}
		head = append(head, item)
		used += lipgloss.Width(item) + 2
	}
	return " " + Truncate(strings.Join(append(head, tailStr), "  "), w-1)
}
