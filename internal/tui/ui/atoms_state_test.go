package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
)

func TestInputViewportKeepsCompleteClustersAndCursor(t *testing.T) {
	rendertest.Force(t, termenv.TrueColor)
	for _, tc := range []struct {
		name      string
		value     string
		cursor    int
		want      string
		caretText string
		end       bool
	}{
		{"ascii-start", "abcdefghijklmnop", 0, "> abcdef", "a", false},
		{"ascii-middle", "abcdefghijklmnop", 8, "> ijklmn", "i", false},
		{"ascii-end", "abcdefghijklmnop", 16, "> lmnop_", "", true},
		{"cjk-start", "界界界界界界", 0, "> 界界界", "界", false},
		{"cjk-middle", "界界界界界界", 3, "> 界界界", "界", false},
		{"cjk-end", "界界界界界界", 6, "> 界界_", "", true},
		{"combining-start", "ae\u0301bcdefgh", 0, "> ae\u0301bcde", "a", false},
		{"combining-middle", "ae\u0301bcdefgh", 2, "> e\u0301bcdef", "e\u0301", false},
		{"combining-end", "ae\u0301bcdefgh", 10, "> defgh_", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := atomContext(t, "no-color", true)
			got := Input(ctx, InputState{Value: tc.value, Cursor: tc.cursor, Focused: true, Enabled: true}, 8)
			if visible := ansi.Strip(got); visible != tc.want {
				t.Fatalf("viewport = %q, want %q", visible, tc.want)
			}
			if width := lipgloss.Width(got); width > 8 {
				t.Fatalf("width = %d, want <= 8: %q", width, got)
			}
			if tc.end {
				if !strings.HasSuffix(ansi.Strip(got), "_") {
					t.Fatalf("end cursor marker is missing: %q", got)
				}
			} else if !strings.Contains(got, "\x1b[7m"+tc.caretText) {
				t.Fatalf("caret does not style the complete current grapheme %q: %q", tc.caretText, got)
			}
		})
	}
}
func TestFieldChromeSurvivesSupportedNarrowWidths(t *testing.T) {
	ctx := atomContext(t, "no-color", true)
	for _, tc := range []struct {
		name  string
		value string
		width int
		want  string
	}{
		{"badge-min", Badge(ctx, "environment", 2), 2, "[]"},
		{"kbd-min", Kbd(ctx, "Ctrl+Shift+P", 2), 2, "[]"},
		{"select-open-min", Select(ctx, SelectState{Label: "very long label", Value: "very long value", Enabled: true, Open: true}, 6), 6, "   [^]"},
		{"select-closed-min", Select(ctx, SelectState{Label: "very long label", Value: "very long value", Enabled: true}, 6), 6, "   [v]"},
		{"switch-on-min", Switch(ctx, ToggleState{Label: "very long label", On: true, Enabled: true}, 7), 7, "   [on]"},
		{"switch-off-min", Switch(ctx, ToggleState{Label: "very long label", Enabled: true}, 8), 8, "   [off]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ansi.Strip(tc.value)
			if width := lipgloss.Width(got); width != tc.width {
				t.Fatalf("width = %d, want %d: %q", width, tc.width, got)
			}
			if got != tc.want {
				t.Fatalf("chrome = %q, want %q", got, tc.want)
			}
		})
	}
	if got := Select(ctx, SelectState{Label: "long", Value: "value", Enabled: true, Open: true}, 5); lipgloss.Width(got) > 5 {
		t.Fatalf("below-min Select width = %d, want <= 5: %q", lipgloss.Width(got), got)
	}
}
func TestFocusAndDisabledAreIndependentWithoutColor(t *testing.T) {
	for _, profile := range []string{"mono", "no-color"} {
		t.Run(profile, func(t *testing.T) {
			ctx := atomContext(t, profile, true)
			for name, render := range map[string]func(ToggleState) string{
				"switch":   func(s ToggleState) string { return Switch(ctx, s, 18) },
				"checkbox": func(s ToggleState) string { return Checkbox(ctx, s, 18) },
				"radio":    func(s ToggleState) string { return Radio(ctx, s, 18) },
			} {
				t.Run(name, func(t *testing.T) {
					focusedDisabled := ansi.Strip(render(ToggleState{Label: "same", On: true, Focused: true}))
					unfocusedDisabled := ansi.Strip(render(ToggleState{Label: "same", On: true}))
					if focusedDisabled == unfocusedDisabled {
						t.Fatalf("focused disabled state is not distinct: %q", focusedDisabled)
					}
				})
			}
		})
	}
}

func TestToggleStateCartesianProduct(t *testing.T) {
	for _, profile := range []string{"truecolor", "ansi16", "mono"} {
		t.Run(profile, func(t *testing.T) {
			ctx := atomContext(t, profile, profile == "mono")
			for name, render := range map[string]func(ToggleState) string{
				"switch":   func(s ToggleState) string { return Switch(ctx, s, 18) },
				"checkbox": func(s ToggleState) string { return Checkbox(ctx, s, 18) },
				"radio":    func(s ToggleState) string { return Radio(ctx, s, 18) },
			} {
				t.Run(name, func(t *testing.T) {
					seen := map[string]bool{}
					for _, on := range []bool{false, true} {
						for _, focused := range []bool{false, true} {
							for _, enabled := range []bool{false, true} {
								got := ansi.Strip(render(ToggleState{Label: "state", On: on, Focused: focused, Enabled: enabled}))
								if seen[got] {
									t.Fatalf("duplicate state %q for on=%t focused=%t enabled=%t", got, on, focused, enabled)
								}
								seen[got] = true
							}
						}
					}
				})
			}
		})
	}
}

func TestToggleStateAxesRemainDistinct(t *testing.T) {
	for _, profile := range []string{"truecolor", "ansi16", "mono"} {
		t.Run(profile, func(t *testing.T) {
			ctx := atomContext(t, profile, profile == "mono")
			for name, render := range map[string]func(ToggleState) string{
				"switch":   func(s ToggleState) string { return Switch(ctx, s, 18) },
				"checkbox": func(s ToggleState) string { return Checkbox(ctx, s, 18) },
				"radio":    func(s ToggleState) string { return Radio(ctx, s, 18) },
			} {
				t.Run(name, func(t *testing.T) {
					base := ToggleState{Label: "state", On: false, Focused: false, Enabled: true}
					for axis, state := range map[string]ToggleState{
						"on":       {Label: "state", On: true, Enabled: true},
						"focused":  {Label: "state", Focused: true, Enabled: true},
						"disabled": {Label: "state", Enabled: false},
					} {
						if got, want := ansi.Strip(render(state)), ansi.Strip(render(base)); got == want {
							t.Fatalf("%s axis is indistinguishable: %q", axis, got)
						}
					}
				})
			}
		})
	}
}
