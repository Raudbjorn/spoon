package ui

import (
	"fmt"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"strings"
	"testing"
)

func TestAtomsGoldensAndWidths(t *testing.T) {
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
			hasANSI := false
			for name, got := range atomCases(ctx) {
				t.Run(name, func(t *testing.T) {
					if width := lipgloss.Width(got); width > 18 {
						t.Fatalf("%s width = %d, want <= 18: %q", name, width, got)
					}
					if strings.Contains(got, "\x1b[") {
						hasANSI = true
					}
					if tt.profile == "mono" && strings.Contains(got, "\x1b[") {
						t.Fatalf("%s has ANSI under mono/ascii: %q", name, got)
					}
					rendertest.Golden(t, "atom_"+tt.name+"_"+name, got)
				})
			}
			if tt.profile != "mono" && !hasANSI {
				t.Fatal("colored profile rendered no ANSI escape sequence")
			}
		})
	}
}

func TestFocusableAtomsRemainDistinctWithoutColor(t *testing.T) {
	ctx := atomContext(t, "no-color", true)
	cases := []struct {
		name      string
		focused   string
		unfocused string
	}{
		{"input", Input(ctx, InputState{Value: "ab", Cursor: 1, Focused: true, Enabled: true}, 18), Input(ctx, InputState{Value: "ab", Cursor: 1, Enabled: true}, 18)},
		{"select", Select(ctx, SelectState{Label: "Theme", Value: "dark", Focused: true, Enabled: true}, 18), Select(ctx, SelectState{Label: "Theme", Value: "dark", Enabled: true}, 18)},
		{"switch", Switch(ctx, ToggleState{Label: "Enabled", On: true, Focused: true, Enabled: true}, 18), Switch(ctx, ToggleState{Label: "Enabled", On: true, Enabled: true}, 18)},
		{"checkbox", Checkbox(ctx, ToggleState{Label: "Enabled", On: true, Focused: true, Enabled: true}, 18), Checkbox(ctx, ToggleState{Label: "Enabled", On: true, Enabled: true}, 18)},
		{"radio", Radio(ctx, ToggleState{Label: "Enabled", On: true, Focused: true, Enabled: true}, 18), Radio(ctx, ToggleState{Label: "Enabled", On: true, Enabled: true}, 18)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := ansi.Strip(tt.focused), ansi.Strip(tt.unfocused); got == want {
				t.Fatalf("focused and unfocused %s are indistinguishable without color: %q", tt.name, got)
			}
		})
	}
}

func TestDisabledAtomsRemainDistinctWithoutColor(t *testing.T) {
	ctx := atomContext(t, "no-color", true)
	cases := []struct {
		name     string
		enabled  string
		disabled string
	}{
		{"input", Input(ctx, InputState{Value: "value", Cursor: 2, Enabled: true}, 18), Input(ctx, InputState{Value: "value", Cursor: 2}, 18)},
		{"select", Select(ctx, SelectState{Label: "Theme", Value: "dark", Enabled: true}, 18), Select(ctx, SelectState{Label: "Theme", Value: "dark"}, 18)},
		{"switch", Switch(ctx, ToggleState{Label: "Enabled", On: true, Enabled: true}, 18), Switch(ctx, ToggleState{Label: "Enabled", On: true}, 18)},
		{"checkbox", Checkbox(ctx, ToggleState{Label: "Enabled", On: true, Enabled: true}, 18), Checkbox(ctx, ToggleState{Label: "Enabled", On: true}, 18)},
		{"radio", Radio(ctx, ToggleState{Label: "Enabled", On: true, Enabled: true}, 18), Radio(ctx, ToggleState{Label: "Enabled", On: true}, 18)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := ansi.Strip(tt.enabled), ansi.Strip(tt.disabled); got == want {
				t.Fatalf("enabled and disabled %s are indistinguishable without color: %q", tt.name, got)
			}
		})
	}
}

func atomContext(t *testing.T, color string, ascii bool) theme.Context {
	t.Helper()
	glyphs := "unicode"
	if ascii {
		glyphs = "ascii"
	}
	ctx, err := theme.ResolveContext("dark", "", color, "", glyphs)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func atomCases(ctx theme.Context) map[string]string {
	long := "界e\u0301 very long text"
	return map[string]string{
		"text-default":                     Text(ctx, TextDefault, long, 18),
		"text-strong":                      Text(ctx, TextStrong, long, 18),
		"text-muted":                       Text(ctx, TextMuted, long, 18),
		"text-faint":                       Text(ctx, TextFaint, long, 18),
		"heading-1":                        Heading(ctx, 1, long, 18),
		"heading-2":                        Heading(ctx, 2, long, 18),
		"heading-3":                        Heading(ctx, 3, long, 18),
		"heading-4":                        Heading(ctx, 4, long, 18),
		"badge-env":                        Badge(ctx, "ENV", 18),
		"badge-file":                       Badge(ctx, "FILE", 18),
		"badge-flag":                       Badge(ctx, "FLAG", 18),
		"badge-default":                    Badge(ctx, "DEFAULT", 18),
		"kbd":                              Kbd(ctx, "Ctrl+U", 18),
		"alert-info":                       Alert(ctx, AlertInfo, long, 18),
		"alert-success":                    Alert(ctx, AlertSuccess, long, 18),
		"alert-warning":                    Alert(ctx, AlertWarning, long, 18),
		"alert-error":                      Alert(ctx, AlertError, long, 18),
		"input-focused-enabled":            Input(ctx, InputState{Value: long, Cursor: 2, Focused: true, Enabled: true}, 18),
		"input-unfocused-enabled":          Input(ctx, InputState{Value: long, Cursor: 2, Enabled: true}, 18),
		"input-focused-disabled":           Input(ctx, InputState{Value: long, Cursor: 2, Focused: true}, 18),
		"input-unfocused-disabled":         Input(ctx, InputState{Value: long, Cursor: 2}, 18),
		"select-closed-focused-enabled":    Select(ctx, SelectState{Label: "Theme", Value: long, Focused: true, Enabled: true}, 18),
		"select-open-focused-enabled":      Select(ctx, SelectState{Label: "Theme", Value: long, Focused: true, Enabled: true, Open: true}, 18),
		"select-closed-unfocused-enabled":  Select(ctx, SelectState{Label: "Theme", Value: long, Enabled: true}, 18),
		"select-open-unfocused-enabled":    Select(ctx, SelectState{Label: "Theme", Value: long, Enabled: true, Open: true}, 18),
		"select-closed-focused-disabled":   Select(ctx, SelectState{Label: "Theme", Value: long, Focused: true}, 18),
		"select-open-focused-disabled":     Select(ctx, SelectState{Label: "Theme", Value: long, Focused: true, Open: true}, 18),
		"select-closed-unfocused-disabled": Select(ctx, SelectState{Label: "Theme", Value: long}, 18),
		"select-open-unfocused-disabled":   Select(ctx, SelectState{Label: "Theme", Value: long, Open: true}, 18),
		"switch-focused-enabled":           Switch(ctx, ToggleState{Label: long, On: true, Focused: true, Enabled: true}, 18),
		"switch-unfocused-enabled":         Switch(ctx, ToggleState{Label: long, On: false, Enabled: true}, 18),
		"switch-focused-disabled":          Switch(ctx, ToggleState{Label: long, On: true, Focused: true}, 18),
		"switch-unfocused-disabled":        Switch(ctx, ToggleState{Label: long, On: false}, 18),
		"checkbox-focused-enabled":         Checkbox(ctx, ToggleState{Label: long, On: true, Focused: true, Enabled: true}, 18),
		"checkbox-unfocused-enabled":       Checkbox(ctx, ToggleState{Label: long, On: false, Enabled: true}, 18),
		"checkbox-focused-disabled":        Checkbox(ctx, ToggleState{Label: long, On: true, Focused: true}, 18),
		"checkbox-unfocused-disabled":      Checkbox(ctx, ToggleState{Label: long, On: false}, 18),
		"radio-focused-enabled":            Radio(ctx, ToggleState{Label: long, On: true, Focused: true, Enabled: true}, 18),
		"radio-unfocused-enabled":          Radio(ctx, ToggleState{Label: long, On: false, Enabled: true}, 18),
		"radio-focused-disabled":           Radio(ctx, ToggleState{Label: long, On: true, Focused: true}, 18),
		"radio-unfocused-disabled":         Radio(ctx, ToggleState{Label: long, On: false}, 18),
		"box":                              Box(ctx, 18, []BoxPart{{Text: "Title"}, {Divider: true}, {Text: long}}),
	}
}

func ExampleInput() {
	ctx, _ := theme.ResolveContext("dark", "", "no-color", "", "ascii")
	fmt.Print(Input(ctx, InputState{Value: "repo", Cursor: 4, Focused: true, Enabled: true}, 18))
	// Output:
	// > repo_
}
