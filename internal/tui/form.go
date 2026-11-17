package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// A form is a column of labelled fields: text inputs, choices and toggles.
type form struct {
	title  string
	submit string // what enter does, for the help line
	fields []*field
	focus  int
	err    string
}

type fieldKind int

const (
	textField fieldKind = iota
	choiceField
	toggleField
)

type field struct {
	key     string
	label   string
	kind    fieldKind
	input   textinput.Model
	options []string
	choice  int
	on      bool
	caption string           // after a toggle's box
	show    func(*form) bool // nil: always shown
	hint    func(*form) string
}

func textInput(key, label, placeholder string) *field {
	in := textinput.New()
	in.Prompt, in.Placeholder, in.CharLimit = "", placeholder, 200
	in.SetWidth(36)
	return &field{key: key, label: label, kind: textField, input: in}
}

func choice(key, label string, options ...string) *field {
	return &field{key: key, label: label, kind: choiceField, options: options}
}

func toggle(key, label, caption string) *field {
	return &field{key: key, label: label, kind: toggleField, caption: caption}
}

func (f *form) get(key string) *field {
	for _, x := range f.fields {
		if x.key == key {
			return x
		}
	}
	return nil
}

// text is a text field's value, trimmed.
func (f *form) text(key string) string { return strings.TrimSpace(f.get(key).input.Value()) }

// chosen is a choice field's option.
func (f *form) chosen(key string) string {
	x := f.get(key)
	if len(x.options) == 0 {
		return ""
	}
	return x.options[x.choice]
}

func (f *form) shown(i int) bool { return f.fields[i].show == nil || f.fields[i].show(f) }

// start focuses the first field.
func (f *form) start() tea.Cmd {
	f.focus = -1
	return f.move(1)
}

// move focuses the next or previous field that's shown.
func (f *form) move(step int) tea.Cmd {
	n := len(f.fields)
	for i, at := 0, f.focus; i < n; i++ {
		at = (at + step + n) % n
		if f.shown(at) {
			f.focus = at
			break
		}
	}
	var cmd tea.Cmd
	for i, x := range f.fields {
		if x.kind != textField {
			continue
		}
		if i == f.focus {
			cmd = x.input.Focus()
		} else {
			x.input.Blur()
		}
	}
	return cmd
}

// key handles a key for the focused field; moving between fields, submitting
// and cancelling are the caller's.
func (f *form) key(msg tea.KeyPressMsg) tea.Cmd {
	x := f.fields[f.focus]
	switch x.kind {
	case choiceField:
		switch msg.String() {
		case "right", "space", "l":
			x.choice = (x.choice + 1) % len(x.options)
		case "left", "h":
			x.choice = (x.choice + len(x.options) - 1) % len(x.options)
		}
	case toggleField:
		switch msg.String() {
		case "space", "right", "left", "x":
			x.on = !x.on
		}
	case textField:
		var cmd tea.Cmd
		x.input, cmd = x.input.Update(msg)
		return cmd
	}
	return nil
}

// other passes anything but a key, such as a cursor blink, to the focused input.
func (f *form) other(msg tea.Msg) tea.Cmd {
	if f.focus < 0 || f.fields[f.focus].kind != textField {
		return nil
	}
	var cmd tea.Cmd
	x := f.fields[f.focus]
	x.input, cmd = x.input.Update(msg)
	return cmd
}

func (f *form) view() string {
	var b strings.Builder
	b.WriteString(bold.Render(f.title) + "\n\n")
	for i, x := range f.fields {
		if !f.shown(i) {
			continue
		}
		mark, label := "  ", faint.Render(pad(x.label, 11))
		if i == f.focus {
			mark, label = "› ", bold.Render(pad(x.label, 11))
		}
		var value string
		switch x.kind {
		case textField:
			value = x.input.View()
		case choiceField:
			value = "‹ " + x.options[x.choice] + " ›"
		case toggleField:
			value = "[ ] " + x.caption
			if x.on {
				value = "[x] " + x.caption
			}
		}
		b.WriteString(mark + label + " " + value)
		if x.hint != nil {
			if h := x.hint(f); h != "" {
				b.WriteString("  " + faint.Render(h))
			}
		}
		b.WriteString("\n")
	}
	if f.err != "" {
		b.WriteString("\n" + red.Render(f.err) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func pad(s string, n int) string { return s + strings.Repeat(" ", max(0, n-len(s))) }
