// Package edit provides the shared, single-line rune editor used by TUI prompts.
package edit

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
)

// TypedText returns literal non-control text, including a complete bracketed
// paste event. Alt-modified runes are shortcuts, not field input.
func TypedText(msg tea.KeyMsg) string {
	if msg.Alt {
		return ""
	}
	var raw string
	switch msg.Type {
	case tea.KeyRunes:
		raw = string(msg.Runes)
	case tea.KeySpace:
		raw = " "
	default:
		return ""
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)
}

// Apply changes text at a rune-indexed cursor. It is deliberately independent
// from rendering so normal and masked credential fields have identical input
// behavior and no byte-slicing can corrupt UTF-8.
func Apply(text string, cursor int, action keymap.Action, typed string) (string, int, bool) {
	r := []rune(text)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(r) {
		cursor = len(r)
	}
	switch action {
	case keymap.CursorLeft:
		if cursor > 0 {
			cursor--
		}
	case keymap.CursorRight:
		if cursor < len(r) {
			cursor++
		}
	case keymap.CursorStart:
		cursor = 0
	case keymap.CursorEnd:
		cursor = len(r)
	case keymap.DeleteBackward:
		if cursor > 0 {
			r = append(r[:cursor-1], r[cursor:]...)
			cursor--
		}
	case keymap.DeleteForward:
		if cursor < len(r) {
			r = append(r[:cursor], r[cursor+1:]...)
		}
	case keymap.ClearInput:
		r, cursor = nil, 0
	case keymap.None:
		if typed == "" {
			return text, cursor, false
		}
		ins := []rune(typed)
		out := make([]rune, 0, len(r)+len(ins))
		out = append(out, r[:cursor]...)
		out = append(out, ins...)
		out = append(out, r[cursor:]...)
		r, cursor = out, cursor+len(ins)
	default:
		return text, cursor, false
	}
	return string(r), cursor, true
}
