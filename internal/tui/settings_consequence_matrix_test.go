package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/tui/settings"
)

func TestEveryConsequenceFieldUsesRootModalBeforeMutationThenSaveReload(t *testing.T) {
	for _, field := range settings.Registry {
		if !field.Editable || field.Consequence == settings.NoConsequence {
			continue
		}
		if field.Consequence != settings.Billing && field.Consequence != settings.Reindex && field.Consequence != settings.Hostwide {
			t.Fatalf("unknown consequence %q for %s", field.Consequence, field.Key)
		}
		if field.Consequence == settings.Hostwide {
			continue // System-layer publication is exercised with an injected system identity below.
		}
		t.Run(field.Key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			cfg, value := matrixConsequenceConfig(t, field.Key)
			if err := config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			before, err := configJSONFromPath(path)
			if err != nil {
				t.Fatal(err)
			}
			m := matrixRootWithSettings(config.LoadedLayer{Config: cfg, Path: path, State: config.LayerLoaded})
			m = matrixRootKey(t, m, ",")
			m = matrixRootFocus(t, m, field.Key)
			m = matrixRootKey(t, m, "e")
			m = matrixRootKey(t, m, "ctrl+u")
			m = matrixRootKey(t, m, value)
			m = matrixRootKey(t, m, "enter")
			if !strings.Contains(m.View(), "Confirm consequence") || !strings.Contains(m.View(), consequenceModalPrefix(field.Consequence)) {
				t.Fatalf("%s did not render its actual consequence modal: %q", field.Key, m.View())
			}
			unchanged, err := configJSONFromPath(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, unchanged) {
				t.Fatal("consequence mutation reached disk before confirmation")
			}
			m = matrixRootKey(t, m, "enter")
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
			m = rootModel(t, updated)
			m = matrixDeliverAction(t, m, cmd)
			loaded, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := field.Get(loaded); got != value {
				t.Fatalf("accepted %s saved %q, want %q", field.Key, got, value)
			}
		})
	}
}

func TestRootSystemLayerHostwideModalAcceptsAndPublishesTempIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system", "config.json")
	cfg := &config.Config{Forge: config.ForgeConfig{Provider: "github"}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	m := matrixRootWithSettings(config.LoadedLayer{Config: cfg, Path: path, System: true, State: config.LayerLoaded})
	m = matrixRootKey(t, m, ",")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = rootModel(t, updated)
	if cmd != nil || !strings.Contains(m.View(), "every user") {
		t.Fatalf("system save did not stop at hostwide modal: command=%v view=%q", cmd != nil, m.View())
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = rootModel(t, updated)
	m = matrixDeliverAction(t, m, cmd)
	loaded, err := config.Load(path)
	if err != nil || loaded.Forge.Provider != "github" {
		t.Fatalf("hostwide temp identity did not save/reload: config=%#v err=%v", loaded, err)
	}
}

func consequenceModalPrefix(consequence settings.Consequence) string {
	switch consequence {
	case settings.Billing:
		return "This change can enable Voyage"
	case settings.Reindex:
		return "This changes vector/index partitioning."
	case settings.Hostwide:
		return "This saves the system configuration"
	default:
		panic("unknown consequence " + string(consequence))
	}
}
func matrixConsequenceConfig(t *testing.T, key string) (*config.Config, string) {
	t.Helper()
	cfg := &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{Disabled: true, OutputDimension: 512}}}
	switch key {
	case "embedder.voyage.disabled":
		return cfg, "false"
	case "embedder.voyage.apiKeyFile":
		path := filepath.Join(t.TempDir(), "voyage.key")
		if err := os.WriteFile(path, []byte("voyage-key"), 0o600); err != nil {
			t.Fatal(err)
		}
		return cfg, path
	case "embedder.voyage.outputDimension":
		return cfg, "1024"
	default:
		t.Fatalf("missing consequence fixture for %s", key)
		return nil, ""
	}
}

func matrixRootFocus(t *testing.T, m Model, key string) Model {
	t.Helper()
	sectionOrder := []settings.Section{
		settings.ForgeSection, settings.GitHubSection, settings.ProxySection, settings.EmbedderSection,
		settings.VoyageSection, settings.AppearanceSection, settings.EnvironmentSection, settings.HostSection,
	}
	var target settings.Field
	found := false
	for _, field := range settings.Registry {
		if field.Key == key {
			target, found = field, true
			break
		}
	}
	if !found {
		t.Fatalf("unknown field %s", key)
	}
	sectionIndex, fieldIndex := -1, 0
	for i, section := range sectionOrder {
		if section != target.Section {
			continue
		}
		sectionIndex = i
		for _, field := range settings.Registry {
			if field.Section == section && field.Key != key {
				fieldIndex++
			}
			if field.Key == key {
				break
			}
		}
		break
	}
	if sectionIndex < 0 {
		t.Fatalf("field %s has no section", key)
	}
	for range sectionIndex {
		m = matrixRootKey(t, m, "right")
	}
	for range fieldIndex {
		m = matrixRootKey(t, m, "down")
	}
	return m
}
