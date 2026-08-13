package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/tui/ui"
)

func TestModalPreventsQuitAndRestoresTableFocusOnce(t *testing.T) {
	m := *movementModel(3)
	m.view = viewTable
	m.cursor = 2
	m.openOverlay(ui.ModalOverlay, "confirm export")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	got := modelValue(t, next)
	if got.quitting {
		t.Fatal("q reached the background while modal was open")
	}
	if !got.overlay.IsOpen() {
		t.Fatal("modal closed without Esc")
	}

	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = modelValue(t, next)
	if got.overlay.IsOpen() || got.view != viewTable || got.cursor != 2 {
		t.Fatalf("Esc did not restore table focus once: overlay=%t view=%v cursor=%d", got.overlay.IsOpen(), got.view, got.cursor)
	}
	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = modelValue(t, next)
	if got.view != viewTable || got.cursor != 2 {
		t.Fatalf("second Esc changed restored focus: view=%v cursor=%d", got.view, got.cursor)
	}
}

func TestSheetPreventsTierAndFullscreenKeys(t *testing.T) {
	m := *movementModel(3)
	m.view = viewTable
	m.openOverlay(ui.SheetOverlay, "table-row-0")
	beforeTier := m.maxTier()

	for _, key := range []rune{'t', 'f'} {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		m = modelValue(t, next)
	}
	if got := m.maxTier(); got != beforeTier {
		t.Fatalf("sheet let background tier key through: got %d, want %d", got, beforeTier)
	}
	if !m.overlay.IsOpen() {
		t.Fatal("sheet closed without Esc")
	}
}

func modelValue(t *testing.T, model tea.Model) Model {
	t.Helper()
	switch model := model.(type) {
	case Model:
		return model
	case *Model:
		return *model
	default:
		t.Fatalf("unexpected model type %T", model)
		return Model{}
	}
}
