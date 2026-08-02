package tui

import (
	"testing"
	"unicode/utf8"
)

// The export prompt is a hand-rolled line editor. It originally had no cursor
// at all: every keystroke appended to the end, so a suggested filename like
// "spoon-export-owner-repo-2026-08-02.json" could not be corrected — typing
// only ever landed after ".json". Backspace also sliced by byte, which
// corrupts a multi-byte rune instead of deleting it.
//
// These exercise the editor through the same entry point the TUI uses.

func exportPromptModel(path string) *Model {
	m := &Model{view: viewExportPath, exportPath: path}
	m.exportCursor = len([]rune(path))
	return m
}

// typeKeys drives the prompt the way handleKey does. A single printable
// character is both the key name and the typed text; named keys type nothing.
func typeKeys(m *Model, keys ...string) {
	for _, k := range keys {
		typed := ""
		if utf8.RuneCountInString(k) == 1 {
			typed = k
		}
		m.handleExportPathKey(k, typed)
	}
}

func TestExportPath_CursorCanEditMidString(t *testing.T) {
	// Correct "report.json" -> "reports.json" by moving back over ".json".
	m := exportPromptModel("report.json")
	typeKeys(m, "left", "left", "left", "left", "left", "s")

	if want := "reports.json"; m.exportPath != want {
		t.Errorf("exportPath = %q, want %q — text must be inserted at the cursor, not appended", m.exportPath, want)
	}
}

func TestExportPath_HomeAndEnd(t *testing.T) {
	m := exportPromptModel("out.json")

	typeKeys(m, "home", "m", "y", "-")
	if want := "my-out.json"; m.exportPath != want {
		t.Errorf("after home: %q, want %q", m.exportPath, want)
	}

	typeKeys(m, "end", "!")
	if want := "my-out.json!"; m.exportPath != want {
		t.Errorf("after end: %q, want %q", m.exportPath, want)
	}
}

func TestExportPath_BackspaceAndDeleteAtCursor(t *testing.T) {
	m := exportPromptModel("abcd.json")

	// Cursor to just after "abcd", backspace removes "d".
	typeKeys(m, "left", "left", "left", "left", "left", "backspace")
	if want := "abc.json"; m.exportPath != want {
		t.Errorf("backspace: %q, want %q", m.exportPath, want)
	}

	// delete removes the character *at* the cursor, which is now ".".
	typeKeys(m, "delete")
	if want := "abcjson"; m.exportPath != want {
		t.Errorf("delete: %q, want %q", m.exportPath, want)
	}
}

// Byte slicing on backspace mangles any non-ASCII path: "ö" is two bytes, so
// one byte-wise backspace leaves an invalid UTF-8 fragment behind.
func TestExportPath_BackspaceDeletesWholeRune(t *testing.T) {
	m := exportPromptModel("sveinbjörn") // 10 runes, 11 bytes

	// One backspace lands on "n"; three more must consume "r" and the whole
	// two-byte "ö", not half of it.
	typeKeys(m, "backspace", "backspace", "backspace", "backspace")

	if want := "sveinb"; m.exportPath != want {
		t.Errorf("exportPath = %q, want %q — backspace must delete a whole rune, not a byte", m.exportPath, want)
	}
	if !utf8.ValidString(m.exportPath) {
		t.Errorf("backspace left invalid UTF-8: %q", m.exportPath)
	}

	// Deleting exactly onto the multi-byte rune from the other side too.
	m2 := exportPromptModel("aöb")
	typeKeys(m2, "left", "backspace")
	if want := "ab"; m2.exportPath != want {
		t.Errorf("mid-string rune delete = %q, want %q", m2.exportPath, want)
	}
	if !utf8.ValidString(m2.exportPath) {
		t.Errorf("mid-string delete left invalid UTF-8: %q", m2.exportPath)
	}
}

// The cursor must never escape the string, however much the user leans on the
// arrow keys — an out-of-range index would panic the render.
func TestExportPath_CursorStaysInBounds(t *testing.T) {
	m := exportPromptModel("ab")

	typeKeys(m, "left", "left", "left", "left", "left")
	if m.exportCursor != 0 {
		t.Errorf("cursor ran off the left edge: %d", m.exportCursor)
	}
	typeKeys(m, "right", "right", "right", "right", "right")
	if want := len([]rune(m.exportPath)); m.exportCursor != want {
		t.Errorf("cursor ran off the right edge: %d, want %d", m.exportCursor, want)
	}

	// Deleting past either end must be a no-op, not a panic.
	typeKeys(m, "delete", "delete")
	typeKeys(m, "home", "backspace", "backspace")
	if m.exportPath != "ab" {
		t.Errorf("edits past the ends changed the text: %q", m.exportPath)
	}

	// ctrl+u clears and parks the cursor at zero.
	typeKeys(m, "ctrl+u")
	if m.exportPath != "" || m.exportCursor != 0 {
		t.Errorf("ctrl+u left path=%q cursor=%d, want empty/0", m.exportPath, m.exportCursor)
	}
}

// Rendering must not panic at any cursor position and must always show the
// full path.
func TestViewExportPath_RendersCursorAtEveryPosition(t *testing.T) {
	m := exportPromptModel("out.json")
	for i := len([]rune(m.exportPath)); i >= 0; i-- {
		m.exportCursor = i
		out := m.viewExportPath()
		if out == "" {
			t.Fatalf("empty render at cursor %d", i)
		}
	}
}
