package threads

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestThreadListMonoAsciiKeepsSelectedFocusMarker(t *testing.T) {
	ctx, err := theme.ResolveContext("dark", "", "mono", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	rendered := threadStateFixtures(ctx)["list"]()
	if !strings.Contains(rendered, "> alice") {
		t.Fatalf("mono/ascii thread selection lost its non-color marker:\n%s", rendered)
	}
	for _, glyph := range []string{"▲", "▼", "▸", "—"} {
		if strings.Contains(rendered, glyph) {
			t.Fatalf("mono/ascii thread list leaked %q:\n%s", glyph, rendered)
		}
	}
}
