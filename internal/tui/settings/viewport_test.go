package settings

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
)

func TestSettingsViewportFallbackAndEnvironmentScroll(t *testing.T) {
	m := New(&config.Config{}, filepath.Join(t.TempDir(), "config.json"), nil)
	m.width, m.height = 79, 24
	if got := m.View(); got != "Terminal too small — requires 80x24, current 79x24" {
		t.Fatalf("fallback = %q", got)
	}
	m.width, m.height, m.section = 80, 24, 6
	first := m.View()
	for range 16 {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(Model)
	}
	if m.scroll == 0 || m.View() == first {
		t.Fatalf("environment did not scroll: %d", m.scroll)
	}
	if !strings.Contains(m.View(), "SPOON_TUI_THEME") && !strings.Contains(m.View(), "TURSO_DATABASE_URL") {
		t.Fatalf("scroll did not expose later environment rows: %q", m.View())
	}
}
