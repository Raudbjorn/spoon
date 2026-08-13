package tui

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestPromptFieldsUseNonColorInputFocusMarker(t *testing.T) {
	ctx, err := theme.ResolveContext("dark", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	m := Model{theme: ctx, width: 80, filterInput: "界e\u0301", filterCursor: 1}
	if got := m.viewFilterPrompt(); !strings.Contains(got, "Match: > ") {
		t.Fatalf("filter prompt lacks non-color input focus marker: %q", got)
	}
}
