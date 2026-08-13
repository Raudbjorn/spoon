package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestMoleculeGoldensAndWidths(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		termenv termenv.Profile
	}{
		{"truecolor-unicode", "truecolor", termenv.TrueColor},
		{"ansi16-unicode", "ansi16", termenv.ANSI},
		{"mono-ascii", "mono", termenv.Ascii},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rendertest.Force(t, tt.termenv)
			ctx := atomContext(t, tt.profile, tt.name == "mono-ascii")
			for name, got := range moleculeCases(ctx) {
				t.Run(name, func(t *testing.T) {
					for _, line := range strings.Split(ansi.Strip(got), "\n") {
						if width := lipgloss.Width(line); width > 24 {
							t.Fatalf("%s width = %d, want <= 24: %q", name, width, line)
						}
					}
					if tt.profile == "mono" && strings.Contains(got, "\x1b[") {
						t.Fatalf("%s has ANSI under mono/ascii: %q", name, got)
					}
					rendertest.Golden(t, "molecule_"+tt.name+"_"+name, got)
				})
			}
		})
	}
}

func TestFocusedButtonRemainsDistinctWithoutColor(t *testing.T) {
	ctx := atomContext(t, "no-color", true)
	focused := Button(ctx, ButtonState{Label: "Save", Focused: true, Enabled: true}, 20)
	unfocused := Button(ctx, ButtonState{Label: "Save", Enabled: true}, 20)
	if ansi.Strip(focused) == ansi.Strip(unfocused) {
		t.Fatalf("focused and unfocused button are indistinguishable: %q", focused)
	}
}

func TestStatCardAndSheetBoundPositiveNarrowWidths(t *testing.T) {
	ctx := atomContext(t, "no-color", true)
	for _, width := range []int{1, 2, 3} {
		for name, got := range map[string]string{
			"stat-card": StatCard(ctx, "界e\u0301long-label", "very-long-value", true, width),
			"sheet":     Sheet(ctx, "界e\u0301long-title", "very-long-subtitle", width),
		} {
			if measured := lipgloss.Width(ansi.Strip(got)); measured > width {
				t.Fatalf("%s width %d = %d, want <= %d: %q", name, width, measured, width, got)
			}
		}
	}
	if got := Sheet(ctx, "title", "subtitle", 0); got == "" {
		t.Fatal("uninitialized width must preserve the sheet for legacy direct-model tests")
	}
}

func TestNavBarGoldenRepresentationIsDiffSafeAndRawWidthExact(t *testing.T) {
	ctx := atomContext(t, "no-color", true)
	raw := NavBar(ctx, "owner/repo | 2 forks", 24)
	if got := lipgloss.Width(raw); got != 24 {
		t.Fatalf("raw NavBar width = %d, want 24: %q", got, raw)
	}
	if strings.HasSuffix(moleculeCases(ctx)["nav-bar"], " ") {
		t.Fatal("NavBar golden representation has trailing whitespace")
	}
}

func TestSheetUsesGlyphProfileForDash(t *testing.T) {
	ctx := atomContext(t, "no-color", true)
	got := Sheet(ctx, "spoon", ctx.Glyph(theme.EmDash)+" help", 24)
	if strings.Contains(got, "—") || !strings.Contains(got, " - help") {
		t.Fatalf("ASCII sheet does not use dash fallback: %q", got)
	}
}

func TestOverlayConsumesEventsAndRestoresFocusOnce(t *testing.T) {
	var overlay Overlay
	overlay.Open(ModalOverlay, "table-row-3")
	for _, key := range []string{"q", "t", "f"} {
		if handled, restored := overlay.HandleKey(key); !handled || restored != "" {
			t.Fatalf("modal key %q = handled=%t restored=%q, want handled without restore", key, handled, restored)
		}
	}
	if handled, restored := overlay.HandleKey("esc"); !handled || restored != "table-row-3" {
		t.Fatalf("modal Esc = handled=%t restored=%q", handled, restored)
	}
	if handled, restored := overlay.HandleKey("esc"); handled || restored != "" {
		t.Fatalf("closed overlay Esc = handled=%t restored=%q, want no second restoration", handled, restored)
	}
	overlay.Open(SheetOverlay, "detail-actions")
	if handled, restored := overlay.HandleKey("esc"); !handled || restored != "detail-actions" {
		t.Fatalf("sheet Esc = handled=%t restored=%q", handled, restored)
	}
}

func moleculeCases(ctx theme.Context) map[string]string {
	const width = 24
	return map[string]string{
		"button-focused":       Button(ctx, ButtonState{Label: "Save ���", Focused: true, Enabled: true}, width),
		"button-disabled":      Button(ctx, ButtonState{Label: "Save", Enabled: false}, width),
		"button-loading":       Button(ctx, ButtonState{Label: "Save", Enabled: true, Loading: true}, width),
		"card":                 Card(ctx, "Fork details", width, []BoxPart{{Text: Text(ctx, TextDefault, "界e\u0301", width-4)}}),
		"alert-info":           TitledAlert(ctx, AlertInfo, "Notice", "界e\u0301", width),
		"alert-success":        TitledAlert(ctx, AlertSuccess, "Saved", "界e\u0301", width),
		"alert-warning":        TitledAlert(ctx, AlertWarning, "Limit", "界e\u0301", width),
		"alert-error":          TitledAlert(ctx, AlertError, "Error", "界e\u0301", width),
		"stat-card":            StatCard(ctx, "heat", "88/100", true, width),
		"table-header":         TableHeader(ctx, "HEAT  REPOSITORY", width),
		"table-row-selected":   TableRow(ctx, "> 88  repo", true, width),
		"table-row-unselected": TableRow(ctx, "  88  repo", false, width),
		"nav-bar":              strings.TrimRight(NavBar(ctx, "owner/repo | 2 forks", width), " "),
		"sheet":                Sheet(ctx, "spoon", ctx.Glyph(theme.EmDash)+" help", width),
		"modal":                Modal(ctx, "Confirm", "Affects every user", width),
	}
}
