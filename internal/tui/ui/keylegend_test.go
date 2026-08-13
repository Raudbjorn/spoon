package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestKeyLegendShowsOnlyScopeAndGlobalBindings(t *testing.T) {
	ctx := theme.DefaultContext()
	legend := KeyLegend(ctx, 80, keymap.MainFilter)
	for _, binding := range keymap.ForScopes(keymap.Global, keymap.MainFilter) {
		key := keymap.KeyLabel(binding.Keys)
		part := Kbd(ctx, key, lipgloss.Width(key)+2) + " " + Text(ctx, TextFaint, binding.Label, lipgloss.Width(binding.Label))
		if !strings.Contains(legend, part) {
			t.Errorf("legend omitted %q/%q", binding.Scope, binding.Label)
		}
	}
	tableKey := keymap.KeyLabel([]string{"enter"})
	tablePart := Kbd(ctx, tableKey, lipgloss.Width(tableKey)+2) + " " + Text(ctx, TextFaint, "View fork details", lipgloss.Width("View fork details"))
	if strings.Contains(legend, tablePart) {
		t.Error("legend leaked table-only detail binding")
	}
}

func TestKeyLegendFits80Columns(t *testing.T) {
	for _, scope := range []keymap.Scope{keymap.MainInput, keymap.MainTable, keymap.MainDetail, keymap.MainExport, keymap.MainTopics, keymap.MainFilter, keymap.MainRank, keymap.MainHelp, keymap.ThreadList, keymap.ThreadCompose, keymap.ThreadPick} {
		legend := KeyLegend(theme.DefaultContext(), 80, scope)
		for _, line := range strings.Split(legend, "\n") {
			if got := lipgloss.Width(line); got > 80 {
				t.Errorf("%s legend width = %d, want <= 80: %q", scope, got, line)
			}
		}
	}
}
