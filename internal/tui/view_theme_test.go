package tui

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestMainTableMonoAsciiKeepsSelectedFocusMarker(t *testing.T) {
	ctx, err := theme.ResolveContext("dark", "", "mono", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	m := filterModel()
	m.theme, m.width, m.height = ctx, 120, 30
	rendered := m.View()
	if !strings.Contains(rendered, "> ") {
		t.Fatalf("mono/ascii table selection lost its non-color marker:\n%s", rendered)
	}
	for _, glyph := range []string{"▲", "▼", "▸", "—"} {
		if strings.Contains(rendered, glyph) {
			t.Fatalf("mono/ascii table leaked %q:\n%s", glyph, rendered)
		}
	}
}
