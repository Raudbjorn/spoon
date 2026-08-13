package settings

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNoConfigVoyageActionCompletesWithoutPanic(t *testing.T) {
	m := New(nil, "", nil)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	m = updated.(Model)
	if cmd == nil || !m.busy {
		t.Fatal("Voyage action was not scheduled")
	}
	message := cmd()
	updated, _ = m.Update(message)
	m = updated.(Model)
	if m.busy || !strings.Contains(m.alert, "disabled") {
		t.Fatalf("result not delivered: busy=%v alert=%q", m.busy, m.alert)
	}
}
