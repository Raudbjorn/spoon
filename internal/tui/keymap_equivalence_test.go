package tui

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func mainHelpScopes() []keymap.Scope {
	return []keymap.Scope{
		keymap.Global, keymap.MainInput, keymap.MainTable, keymap.MainDetail,
		keymap.MainExport, keymap.MainTopics, keymap.MainFilter, keymap.MainRank,
		keymap.MainSettings, keymap.MainHelp,
	}
}

func renderedBindingLine(binding keymap.Binding) string {
	key := keymap.KeyLabel(binding.Keys)
	padding := 16 - len([]rune(key))
	if padding < 1 {
		padding = 1
	}
	return "  " + key + strings.Repeat(" ", padding) + binding.Label
}

func TestMainHelpRendersEveryScopedRegistryBindingExactly(t *testing.T) {
	body := helpBody(theme.DefaultContext())
	for _, binding := range keymap.ForScopes(mainHelpScopes()...) {
		if !strings.Contains(body, renderedBindingLine(binding)) {
			t.Errorf("help missing exact registry binding %q/%q/%q", binding.Scope, binding.Keys, binding.Label)
		}
		for _, key := range binding.Keys {
			if got := keymap.Dispatch(binding.Scope, key); got != binding.Action {
				t.Errorf("Dispatch(%q, %q) = %q, want %q", binding.Scope, key, got, binding.Action)
			}
		}
	}
}

func TestEveryMainRegistryAlternateDispatches(t *testing.T) {
	for _, binding := range keymap.ForScopes(mainHelpScopes()...) {
		for _, key := range binding.Keys {
			if got := keymap.Dispatch(binding.Scope, key); got != binding.Action {
				t.Errorf("Dispatch(%q, %q) = %q, want %q", binding.Scope, key, got, binding.Action)
			}
		}
	}
}
