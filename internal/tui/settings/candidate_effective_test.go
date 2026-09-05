package settings

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/setupcheck"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcceptedEditRecomputesCandidateEffectiveFromStartupSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &config.Config{Forge: config.ForgeConfig{Provider: "github"}}
	config.RecordFieldValue(cfg, "forge.provider", "github")
	env := map[string]string{"GH_TOKEN": "startup-token"}
	startup := config.ResolveEffectiveConfig(cfg, nil, env)

	m := selectSettingsField(New(cfg, path, nil).WithFlags(nil).WithEnvironment(env).WithEffective(startup), "forge.provider")
	m.width, m.height = 80, 24
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("gitlab")})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	row := m.resolve(FieldByMust("forge.provider"))
	if row.Value != "gitlab" || row.Source != FileSource {
		t.Fatalf("candidate forge row = %+v, want edited FILE value", row)
	}
	if !strings.Contains(settingsFieldPart(t, m, "Provider"), "Provider: gitlab [FILE]") {
		t.Fatalf("rendered field did not use candidate effective config")
	}
	if got := m.Environment["GH_TOKEN"]; got != "startup-token" {
		t.Fatalf("environment snapshot changed: %q", got)
	}
	field := FieldByMust("embedder.model")
	candidate, err := Candidate(field, m.Config, "fast-bge-base-en-v1.5")
	if err != nil {
		t.Fatal(err)
	}
	*m.Config = *candidate
	m.refreshEffective()
	if _, err := runActionWithDeps(ActionFastEmbedCheck, &m, ActionDeps{}); err != nil {
		t.Fatalf("FastEmbed action rejected a supported model: %v", err)
	}
	m = applySettingsEdit(t, m, "ui.theme", "amber")
	appearance := m.resolve(FieldByMust("ui.theme"))
	if appearance.Value != "amber" || appearance.Source != FileSource {
		t.Fatalf("candidate appearance row = %+v", appearance)
	}
	var probed setupcheck.ProviderInput
	if _, err := runActionWithDeps(ActionProviderProbe, &m, ActionDeps{
		Provider: func(_ context.Context, input setupcheck.ProviderInput, _ http.RoundTripper) (forge.AuthInfo, error) {
			probed = input
			return forge.AuthInfo{Provider: input.Provider}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if probed.Provider != forge.ProviderGitLab {
		t.Fatalf("provider preflight used stale effective config: %#v", probed)
	}
	if _, err := runActionWithDeps(ActionSave, &m, ActionDeps{}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Forge.Provider != "gitlab" || reloaded.Embedder.Model != "fast-bge-base-en-v1.5" || reloaded.UI.Theme != "amber" {
		t.Fatalf("saved config did not preserve candidate edits: %#v", reloaded)
	}
}

func applySettingsEdit(t *testing.T, m Model, key, value string) Model {
	t.Helper()
	m = selectSettingsField(m, key)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.editing || m.confirming {
		t.Fatalf("%s edit was not accepted: alert=%q", key, m.alert)
	}
	return m
}
