package settings

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestEveryEditableRowHasVisibleMonoAsciiFocusAt80x24(t *testing.T) {
	ctx, err := theme.ResolveConfiguredContext("", "dark", "mono", "", "", "ascii", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range Registry {
		if !field.Editable {
			continue
		}
		t.Run(field.Key, func(t *testing.T) {
			m := selectSettingsField(New(&config.Config{}, filepath.Join(t.TempDir(), "config.json"), nil).WithTheme(ctx), field.Key)
			m.width, m.height = 80, 24
			m.keepFocusVisible()
			focused := settingsFieldPart(t, m, field.Label)
			if !strings.HasPrefix(focused, "> ") {
				t.Fatalf("focused mono/ascii row = %q", focused)
			}
			m.focus = -1
			unfocused := settingsFieldPart(t, m, field.Label)
			if !strings.HasPrefix(unfocused, "  ") || focused == unfocused {
				t.Fatalf("focus is not visibly distinct: focused=%q unfocused=%q", focused, unfocused)
			}
		})
	}
}

func settingsFieldPart(t *testing.T, m Model, label string) string {
	t.Helper()
	for _, part := range m.sectionParts(76) {
		if strings.Contains(part.Text, label+":") {
			return part.Text
		}
	}
	t.Fatalf("field %q was not rendered", label)
	return ""
}
