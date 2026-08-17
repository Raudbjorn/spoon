package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
)

func TestRegistryAndMainHelpAreBidirectional(t *testing.T) {
	body := helpBody(renderProfiles(t)[0].context)
	for _, binding := range keymap.ForScopes(keymap.MainTable, keymap.MainDetail, keymap.MainHelp) {
		if !strings.Contains(body, binding.Label) {
			t.Errorf("help omits registry action %q", binding.Label)
		}
		for _, key := range binding.Keys {
			if got := keymap.Dispatch(binding.Scope, key); got != binding.Action {
				t.Errorf("registry dispatch %q/%q = %q, want %q", binding.Scope, key, got, binding.Action)
			}
		}
	}
}

func TestD1MovesApplyInTableAndDetail(t *testing.T) {
	for _, state := range []viewState{viewTable, viewDetail} {
		t.Run(fmt.Sprint(state), func(t *testing.T) {
			m := detailTestModel(t, 80)
			m.view = state
			m.setMaxTier(3)
			if state == viewTable {
				_, _ = m.handleTableKey("c")
			} else {
				_, _ = m.handleDetailKey("c")
			}
			if got := m.maxTier(); got != 2 {
				t.Fatalf("c ceiling = %d, want 2", got)
			}
			m.parent = &forge.ParentData{FullName: "upstream/repo", DefaultBranch: "main"}
			var command tea.Cmd
			if state == viewTable {
				_, command = m.handleTableKey("d")
			} else {
				_, command = m.handleDetailKey("d")
			}
			if command == nil {
				t.Fatal("d did not dispatch compare")
			}
		})
	}
}

func TestThemeAndFullscreenPreserveMainState(t *testing.T) {
	profile := renderProfiles(t)[1]
	m := detailTestModel(t, 80)
	m.view, m.width, m.height = viewDetail, 80, 24
	m.theme = profile.context
	m.detailOffset = 3
	cursor := m.cursor
	before := m.theme
	_, _ = m.handleDetailKey("t")
	if m.theme.ColorProfile != before.ColorProfile || m.theme.GlyphProfile != before.GlyphProfile || m.theme == before {
		t.Fatal("t did not toggle only the palette")
	}
	_, _ = m.handleDetailKey("t")
	if m.theme != before {
		t.Fatal("theme did not round-trip")
	}
	chrome := m.View()
	_, _ = m.handleDetailKey("f")
	if !m.fullscreen || m.cursor != cursor || m.detailOffset != 3 {
		t.Fatal("fullscreen changed detail state")
	}
	if strings.Contains(m.View(), "[o] Open") {
		t.Fatal("fullscreen kept detail chrome")
	}
	_, _ = m.handleDetailKey("f")
	if m.fullscreen || m.cursor != cursor || m.detailOffset != 3 || m.View() != chrome {
		t.Fatal("fullscreen did not restore detail state and chrome")
	}
}

func TestTableFullscreenPreservesCursorAndPaging(t *testing.T) {
	m := filterModel()
	m.width, m.height, m.cursor = 80, 24, 1
	beforeCursor, beforePage := m.cursor, m.pageSize()
	normal := m.View()

	_, _ = m.handleTableKey("f")
	if !m.fullscreen || m.cursor != beforeCursor {
		t.Fatal("fullscreen changed table selection")
	}
	// Paging deliberately does NOT stay equal across the toggle. This assertion
	// used to require m.pageSize() == beforePage, which held only because the
	// chrome budget was a constant that ignored fullscreen -- the same constant
	// that budgeted one line for a footer wrapping to nine, and so pushed the
	// status bar and column header off the top of the terminal for good. Now
	// that the budget measures what is actually drawn, hiding the chrome buys
	// rows, which is the entire point of the key. What must hold is that the
	// gain is real and matches the chrome that went away.
	if got := m.pageSize(); got <= beforePage {
		t.Fatalf("fullscreen page size = %d, want more than the %d rows the chrome left", got, beforePage)
	}
	// "Cycle sort column" is in the curated table footer; "Move selection up"
	// is not, so asserting on it would pass whether the chrome were hidden or
	// not.
	if strings.Contains(m.View(), "Cycle sort column") {
		t.Fatal("fullscreen kept table chrome")
	}

	_, _ = m.handleTableKey("f")
	if m.fullscreen || m.cursor != beforeCursor || m.pageSize() != beforePage || m.View() != normal {
		t.Fatal("table chrome did not restore losslessly")
	}
}

func TestDetailLegendReservesItsWrappedFooterAt80x24(t *testing.T) {
	m := detailTestModel(t, 80)
	m.view, m.width, m.height = viewDetail, 80, 24
	rendered := m.viewDetail()
	if lines := len(strings.Split(rendered, "\n")); lines > m.height {
		t.Fatalf("detail view uses %d rows at 80x24, want <= %d", lines, m.height)
	}
	for _, binding := range keymap.ForScopes(keymap.Global, keymap.MainDetail) {
		if !strings.Contains(m.detailLegend(), binding.Label) {
			t.Errorf("detail legend omits %q/%q", binding.Scope, binding.Label)
		}
	}
}

func TestHelpTransitionNoticeAnd80x24Goldens(t *testing.T) {
	for _, profile := range renderProfiles(t) {
		t.Run(profile.name, func(t *testing.T) {
			rendertest.Force(t, profile.termenv)
			m := NewModel(nil, forge.AuthInfo{}, "", false).WithTheme(profile.context)
			m.view, m.width, m.height = viewHelp, 80, 24
			got := trimGoldenRender(m.View())
			for _, text := range []string{"t: enrichment ceiling -> dark/light theme", "c: open compare -> enrichment ceiling", "d: unbound -> open compare"} {
				if !strings.Contains(got, text) {
					t.Fatalf("transition notice missing %q", text)
				}
			}
			rendertest.Golden(t, "keymap_help_80x24_"+profile.name, got)
		})
	}
}
