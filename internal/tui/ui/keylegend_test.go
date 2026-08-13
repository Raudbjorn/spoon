package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestKeyLegendMatchesEveryEffectiveScopeExactlyOnce(t *testing.T) {
	ctx := theme.DefaultContext()
	scopes := []keymap.Scope{keymap.MainInput, keymap.MainTable, keymap.MainDetail, keymap.MainExport, keymap.MainTopics, keymap.MainFilter, keymap.MainRank, keymap.MainHelp, keymap.ThreadList, keymap.ThreadCompose, keymap.ThreadPick}
	for _, scope := range scopes {
		t.Run(string(scope), func(t *testing.T) {
			legend := KeyLegend(ctx, 80, scope)
			seen := map[string]keymap.Binding{}
			effective := map[string]bool{}
			for _, binding := range keymap.ForScopes(keymap.Global, scope) {
				key := keymap.KeyLabel(binding.Keys)
				part := Kbd(ctx, key, lipgloss.Width(key)+2) + " " + Text(ctx, TextFaint, binding.Label, lipgloss.Width(binding.Label))
				if got := strings.Count(legend, part); got != 1 {
					t.Errorf("effective binding %q/%q rendered %d times, want once", binding.Scope, binding.Label, got)
				}
				effective[key+"\x00"+binding.Label] = true
				for _, raw := range binding.Keys {
					if prior, duplicate := seen[raw]; duplicate {
						t.Errorf("effective bindings duplicate %q: %q and %q", raw, prior.Scope, binding.Scope)
					}
					seen[raw] = binding
				}
			}
			for _, foreign := range keymap.Registry {
				if foreign.Scope == keymap.Global || foreign.Scope == scope {
					continue
				}
				key := keymap.KeyLabel(foreign.Keys)
				if effective[key+"\x00"+foreign.Label] {
					continue // the same behavior is intentionally shared by scopes.
				}
				part := Kbd(ctx, key, lipgloss.Width(key)+2) + " " + Text(ctx, TextFaint, foreign.Label, lipgloss.Width(foreign.Label))
				if strings.Contains(legend, part) {
					t.Errorf("legend leaked %q binding %q", foreign.Scope, foreign.Label)
				}
			}
		})
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
