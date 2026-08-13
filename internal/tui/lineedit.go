package tui

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
)

// typedText returns the literal characters a key event contributes to a text
// field, or "" for navigation and control keys.
//
// This must read msg.Runes rather than msg.String(): a bracketed paste arrives
// as a single KeyRunes event carrying the entire pasted string, and its
// String() form is wrapped in brackets ("[owner/repo]") precisely so that it
// cannot match a keybinding. Gating insertion on a one-character key string —
// as both prompts used to — therefore dropped every paste on the floor.
//
// Control characters are stripped: pasted text routinely carries a trailing
// newline, and embedding one in a filename or repo slug yields a broken path
// or a lookup that fails for no visible reason.
func typedText(msg tea.KeyMsg) string {
	// An Alt-modified rune is a keyboard shortcut (alt+f, alt+b, ...), not
	// text: bubbletea reports it as the same KeyRunes/KeySpace shape as a
	// plain keystroke, distinguished only by this flag. A bracketed paste
	// never carries Alt, so this cannot swallow a real paste.
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

// lineEdit applies one key event to a single-line text field with a
// rune-indexed cursor, returning the updated text and cursor position.
//
// handled reports whether the key was consumed. Keys the surrounding prompt
// owns (enter, esc, and anything else unrecognised) come back false so the
// caller can act on them.
//
// Everything is indexed in runes, never bytes: byte-slicing the tail off a
// multi-byte character leaves an invalid UTF-8 fragment in the field.
func lineEdit(text string, cursor int, action keymap.Action, typed string) (string, int, bool) {
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
		r = out
		cursor += len(ins)
	default:
		return text, cursor, false
	}

	return string(r), cursor, true
}
