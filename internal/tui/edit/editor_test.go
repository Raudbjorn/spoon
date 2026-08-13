package edit

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
)

func TestEditorPreservesRunesAndBracketedPaste(t *testing.T) {
	pasted := TypedText(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a界\nβ")})
	if pasted != "a界β" {
		t.Fatalf("pasted = %q", pasted)
	}
	text, cursor, ok := Apply("a界β", 2, keymap.DeleteBackward, "")
	if !ok || text != "aβ" || cursor != 1 {
		t.Fatalf("delete = (%q, %d, %v)", text, cursor, ok)
	}
}
