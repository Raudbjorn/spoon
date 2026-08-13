package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/tui/theme"
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

func TestOverlayRestoresSelectedForkByIDAfterReorder(t *testing.T) {
	m := *movementModel(3)
	m.view = viewTable
	m.cursor = 1
	wantID := m.forks[m.cursor].Fork.ID
	m.openOverlay(ui.ModalOverlay, "confirm export")

	m.forks[0], m.forks[1] = m.forks[1], m.forks[0]
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := modelValue(t, next)
	if gotID := got.forks[got.cursor].Fork.ID; gotID != wantID {
		t.Fatalf("Esc restored fork %q, want %q after reorder", gotID, wantID)
	}
	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = modelValue(t, next)
	if gotID := got.forks[got.cursor].Fork.ID; gotID != wantID {
		t.Fatalf("second Esc changed restored fork to %q, want %q", gotID, wantID)
	}
}

func TestModelViewOverlayViewportContract(t *testing.T) {
	ctx, err := theme.ResolveContext("", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []ui.OverlayKind{ui.ModalOverlay, ui.SheetOverlay} {
		t.Run(kindName(kind), func(t *testing.T) {
			m := Model{view: viewTable, width: 79, height: 24, theme: ctx}
			m.openOverlay(kind, "current operation")
			if got, want := m.View(), ui.FallbackMessageFor(ctx, 79, 24); got != want {
				t.Fatalf("79x24 overlay fallback = %q, want %q", got, want)
			}

			for _, size := range []struct{ width, height int }{{80, 24}, {160, 50}} {
				m.width, m.height = size.width, size.height
				contentWidth := ui.ContentWidth(size.width)
				want := ui.Modal(ctx, "Confirm", "current operation", contentWidth)
				if kind == ui.SheetOverlay {
					want = ui.Sheet(ctx, "Help", "current operation", contentWidth)
				}
				if got := m.View(); got != want {
					t.Fatalf("%dx%d %s = %q, want %q", size.width, size.height, kindName(kind), got, want)
				}
				for _, line := range strings.Split(want, "\n") {
					if width := lipgloss.Width(line); width > ui.MaxContentWidth {
						t.Fatalf("%dx%d %s line width = %d, want <= %d: %q", size.width, size.height, kindName(kind), width, ui.MaxContentWidth, line)
					}
				}
			}
		})
	}
}

func kindName(kind ui.OverlayKind) string {
	if kind == ui.ModalOverlay {
		return "modal"
	}
	return "sheet"
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
