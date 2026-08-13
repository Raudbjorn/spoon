package threads

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestPicker_UsesViewportGateAndMonoFocus(t *testing.T) {
	ctx, err := theme.ResolveContext("dark", "", "mono", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	m := NewPicker(samplePRs()).WithTheme(ctx)

	got, _ := m.Update(tea.WindowSizeMsg{Width: 79, Height: 24})
	if rendered := got.(PickerModel).View(); rendered != "Terminal too small - requires 80x24, current 79x24" {
		t.Fatalf("small picker = %q", rendered)
	}

	got, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	rendered := got.(PickerModel).View()
	if !strings.Contains(rendered, "> PR #") {
		t.Fatalf("mono/ascii picker selection lost its non-color marker:\n%s", rendered)
	}
	for _, glyph := range []string{"▲", "▼", "▸", "—"} {
		if strings.Contains(rendered, glyph) {
			t.Fatalf("mono/ascii picker leaked %q:\n%s", glyph, rendered)
		}
	}
}

func TestGoldenPickerViewStates(t *testing.T) {
	for _, profile := range threadRenderProfiles(t) {
		t.Run(profile.name, func(t *testing.T) {
			rendertest.Force(t, profile.termenv)
			for name, picker := range map[string]PickerModel{
				"list":  NewPicker(samplePRs()).WithTheme(profile.context),
				"empty": NewPicker(nil).WithTheme(profile.context),
			} {
				rendertest.Golden(t, "picker_"+profile.name+"_"+name, picker.View())
			}
		})
	}
}
