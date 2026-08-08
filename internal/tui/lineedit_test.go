package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Both text prompts (repo search, export path) used to gate insertion on
// len(key) == 1. A bracketed paste arrives as a single KeyRunes event holding
// the whole pasted string, so it was never length 1 and was dropped outright —
// pasting into either field did nothing at all.

func TestTypedText_AcceptsPasteAndPlainKeys(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyMsg
		want string
	}{
		{"single rune", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}, "a"},
		{"space", tea.KeyMsg{Type: tea.KeySpace}, " "},
		{
			"bracketed paste",
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("dustinrue/proxmox-packer"), Paste: true},
			"dustinrue/proxmox-packer",
		},
		{"enter contributes no text", tea.KeyMsg{Type: tea.KeyEnter}, ""},
		{"esc contributes no text", tea.KeyMsg{Type: tea.KeyEsc}, ""},
		{"left contributes no text", tea.KeyMsg{Type: tea.KeyLeft}, ""},
		{"ctrl+u contributes no text", tea.KeyMsg{Type: tea.KeyCtrlU}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := typedText(tc.msg); got != tc.want {
				t.Errorf("typedText = %q, want %q", got, tc.want)
			}
		})
	}
}

// A pasted value routinely carries a trailing newline (copying a line from a
// terminal or editor). Embedding it in a filename or repo slug produces a
// broken path or a lookup that silently fails, so control characters are
// dropped rather than inserted.
func TestTypedText_StripsControlCharactersFromPaste(t *testing.T) {
	msg := tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("owner/repo\n"),
		Paste: true,
	}
	got := typedText(msg)
	if strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("typedText kept a control character: %q", got)
	}
	if got != "owner/repo" {
		t.Errorf("typedText = %q, want %q", got, "owner/repo")
	}

	multi := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\r\nb"), Paste: true}
	if got := typedText(multi); got != "ab" {
		t.Errorf("multi-line paste = %q, want %q", got, "ab")
	}
}

func TestLineEdit_InsertsPasteAtCursor(t *testing.T) {
	text, cursor := "", 0
	text, cursor, ok := lineEdit(text, cursor, "[owner/repo]", "owner/repo")
	if !ok {
		t.Fatal("paste was not consumed as an edit")
	}
	if text != "owner/repo" {
		t.Errorf("text = %q, want %q", text, "owner/repo")
	}
	if cursor != len([]rune("owner/repo")) {
		t.Errorf("cursor = %d, want %d", cursor, len([]rune("owner/repo")))
	}

	// Paste into the middle rather than at the end.
	text, cursor = "ab", 1
	text, cursor, _ = lineEdit(text, cursor, "[XY]", "XY")
	if text != "aXYb" {
		t.Errorf("mid-string paste = %q, want %q", text, "aXYb")
	}
	if cursor != 3 {
		t.Errorf("cursor = %d, want 3", cursor)
	}
}

func TestLineEdit_NavigationAndDeletion(t *testing.T) {
	// Cursor movement.
	_, c, ok := lineEdit("abc", 3, "left", "")
	if !ok || c != 2 {
		t.Errorf("left: cursor=%d ok=%v, want 2/true", c, ok)
	}
	_, c, _ = lineEdit("abc", 0, "home", "")
	if c != 0 {
		t.Errorf("home: cursor=%d, want 0", c)
	}
	_, c, _ = lineEdit("abc", 0, "end", "")
	if c != 3 {
		t.Errorf("end: cursor=%d, want 3", c)
	}

	// Rune-safe deletion.
	got, c, _ := lineEdit("aöb", 2, "backspace", "")
	if got != "ab" || c != 1 {
		t.Errorf("backspace: %q cursor=%d, want %q/1", got, c, "ab")
	}
	got, _, _ = lineEdit("aöb", 1, "delete", "")
	if got != "ab" {
		t.Errorf("delete: %q, want %q", got, "ab")
	}

	// ctrl+u clears.
	got, c, _ = lineEdit("abc", 3, "ctrl+u", "")
	if got != "" || c != 0 {
		t.Errorf("ctrl+u: %q cursor=%d, want empty/0", got, c)
	}
}

// Keys the prompt owns (enter/esc) must not be swallowed by the editor, or the
// caller can never confirm or cancel.
func TestLineEdit_LeavesControlKeysToTheCaller(t *testing.T) {
	for _, key := range []string{"enter", "esc"} {
		if _, _, ok := lineEdit("abc", 3, key, ""); ok {
			t.Errorf("lineEdit consumed %q; the prompt must handle it", key)
		}
	}
}

// End-to-end through handleKey, the way bubbletea actually delivers events:
// a paste must reach both text prompts, and a plain keypress must still work.
func TestHandleKey_PasteReachesBothPrompts(t *testing.T) {
	paste := tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("dustinrue/proxmox-packer"),
		Paste: true,
	}

	t.Run("repo search input", func(t *testing.T) {
		m := &Model{view: viewInput}
		m.handleKey(paste)
		if m.input != "dustinrue/proxmox-packer" {
			t.Errorf("input = %q, want the pasted repo", m.input)
		}
		// A normal keystroke still appends after the paste.
		m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
		if m.input != "dustinrue/proxmox-packer!" {
			t.Errorf("input after keypress = %q", m.input)
		}
	})

	t.Run("export path prompt", func(t *testing.T) {
		m := &Model{view: viewExportPath}
		m.handleKey(paste)
		if m.exportPath != "dustinrue/proxmox-packer" {
			t.Errorf("exportPath = %q, want the pasted text", m.exportPath)
		}
	})
}

// Navigation keys must move the caret, not type their own names into the
// field — "pgup" and friends are printable strings and a naive
// "insert anything printable" rule would spell them out.
func TestHandleKey_NavigationKeysAreNotTypedAsText(t *testing.T) {
	m := &Model{view: viewInput}
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyPgUp}, {Type: tea.KeyPgDown},
		{Type: tea.KeyTab}, {Type: tea.KeyUp}, {Type: tea.KeyDown},
	} {
		m.handleKey(msg)
	}
	if m.input != "" {
		t.Errorf("navigation keys were typed into the field: %q", m.input)
	}
}

// alt+f arrives as the same KeyRunes{Runes:['f']} shape as a plain 'f'
// keystroke, distinguished only by the Alt flag. Without checking it,
// typedText inserted the shortcut's letter into the field instead of treating
// it as a keybinding the caller should handle.
func TestTypedText_RejectsAltModifiedRunes(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true}
	if got := typedText(msg); got != "" {
		t.Errorf("typedText(alt+f) = %q, want empty — alt+f is a shortcut, not text", got)
	}

	// A bracketed paste never carries Alt, so this must not affect real pastes.
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("owner/repo"), Paste: true}
	if got := typedText(paste); got != "owner/repo" {
		t.Errorf("typedText(paste) = %q, want %q", got, "owner/repo")
	}
}
