package tui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/tui/settings"
)

func TestRootSettingsOneFieldSaveReloadPreservesFullLoadedConfig(t *testing.T) {
	t.Setenv("SPOON_NO_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	initial := matrixFullConfig(t)
	if err := config.Save(path, &initial); err != nil {
		t.Fatal(err)
	}
	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	layer := config.LoadDefaultWithLayer()
	if layer.State != config.LayerLoaded || layer.Path != path {
		t.Fatalf("loaded layer = %#v", layer)
	}

	m := matrixRootWithSettings(layer)
	m = matrixRootKey(t, m, ",")
	m = matrixRootKey(t, m, "e")
	m = matrixRootKey(t, m, "ctrl+u")
	m = matrixRootKey(t, m, "gitlab")
	m = matrixRootKey(t, m, "enter")
	if !strings.Contains(m.View(), "gitlab") {
		t.Fatalf("edited value was not rendered through root: %q", m.View())
	}
	var cmd tea.Cmd
	var updated tea.Model
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = rootModel(t, updated)
	m = matrixDeliverAction(t, m, cmd)

	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Forge.Provider != "gitlab" {
		t.Fatalf("saved provider = %q", after.Forge.Provider)
	}
	after.Forge.Provider = before.Forge.Provider
	beforeJSON, err := configJSON(before)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, err := configJSON(after)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatalf("one-field settings save changed another persisted value:\nbefore=%s\nafter=%s", beforeJSON, afterJSON)
	}
}

func TestRootSettingsRejectsInvalidRequestsPerMinuteWithoutDiskMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	initial := matrixFullConfig(t)
	if err := config.Save(path, &initial); err != nil {
		t.Fatal(err)
	}
	before, err := configJSONFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	m := matrixRootWithSettings(config.LoadedLayer{Config: &initial, Path: path, State: config.LayerLoaded})
	m = matrixRootKey(t, m, ",")
	m = matrixRootKey(t, m, "right")
	m = matrixRootKey(t, m, "down")
	m = matrixRootKey(t, m, "e")
	m = matrixRootKey(t, m, "ctrl+u")
	m = matrixRootKey(t, m, "9999")
	m = matrixRootKey(t, m, "enter")
	if !strings.Contains(m.View(), "Requests per minute") || !strings.Contains(m.View(), "github.requestsPerMinute") {
		t.Fatalf("invalid edit did not render field-naming alert: %q", m.View())
	}
	after, err := configJSONFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid root edit mutated the saved configuration")
	}
}

func TestRootSettingsTreatsInjectedUnwritableLoadedLayerAsReadOnly(t *testing.T) {
	path := filepath.Join("/proc", "spoon-settings-matrix", "config.json")
	m := matrixRootWithSettings(config.LoadedLayer{
		Config: &config.Config{Forge: config.ForgeConfig{Provider: "github"}},
		Path:   path,
		State:  config.LayerLoaded,
	})
	m = matrixRootKey(t, m, ",")
	if !strings.Contains(m.View(), "cannot atomically publish") {
		t.Fatalf("unwritable layer was not rendered read-only: %q", m.View())
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = rootModel(t, updated)
	if cmd != nil || !strings.Contains(m.View(), "cannot atomically publish") {
		t.Fatalf("read-only loaded layer offered save: command=%v view=%q", cmd != nil, m.View())
	}
}

func TestRootSettingsNoConfigEntryOffersNoWriteAction(t *testing.T) {
	t.Setenv("SPOON_NO_CONFIG", "1")
	layer := config.LoadDefaultWithLayer()
	if layer.State != config.LayerDisabled {
		t.Fatalf("layer = %#v", layer)
	}
	m := matrixRootWithSettings(layer)
	m = matrixRootKey(t, m, ",")
	if m.view != viewSettings || !strings.Contains(m.View(), "disabled by SPOON_NO_CONFIG=1") {
		t.Fatalf("no-config root entry did not render disabled settings: %q", m.View())
	}
	for _, key := range []string{"e", "s", "r"} {
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		m = rootModel(t, updated)
		if cmd != nil {
			t.Fatalf("no-config settings offered %q action", key)
		}
	}
}

func matrixRootWithSettings(layer config.LoadedLayer) Model {
	m := NewModel(nil, forge.AuthInfo{}, "", false)
	m.view, m.width, m.height = viewTable, 80, 24
	return m.WithSettings(settings.NewFromLayer(layer, nil))
}

func matrixRootKey(t *testing.T, m Model, key string) Model {
	t.Helper()
	var message tea.KeyMsg
	switch key {
	case "enter":
		message = tea.KeyMsg{Type: tea.KeyEnter}
	case "right":
		message = tea.KeyMsg{Type: tea.KeyRight}
	case "down":
		message = tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+u":
		message = tea.KeyMsg{Type: tea.KeyCtrlU}
	default:
		message = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	updated, _ := m.Update(message)
	return rootModel(t, updated)
}

func matrixDeliverAction(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	m = matrixDeliverCommand(t, m, cmd)
	if strings.Contains(m.View(), "Working") || !strings.Contains(m.View(), "saved") {
		t.Fatalf("save lifecycle did not settle through root: %q", m.View())
	}
	return m
}

func matrixDeliverCommand(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("settings action returned no command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("settings action command = %T", cmd())
	}
	for _, next := range batch {
		updated, _ := m.Update(next())
		m = rootModel(t, updated)
	}
	return m
}

func matrixFullConfig(t *testing.T) config.Config {
	t.Helper()
	key := func(name string) string {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte("matrix-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	whitelist := true
	return config.Config{
		Version: config.CurrentVersion,
		Forge: config.ForgeConfig{Provider: "github", Host: "forge.example.test"},
		GitHub: config.GitHubConfig{Tokens: []string{"matrix-token"}, RequestsPerMinute: 300, Proxy: config.ProxyConfig{
			Enabled: true, APIKeyFile: key("proxy-api"), StaticFile: key("proxy-static"), WhitelistPublicIP: &whitelist, CacheTTL: "2m",
		}},
		Embedder: config.EmbedderConfig{Backend: "fastembed", Model: "fast-bge-small-en-v1.5", CacheDir: filepath.Join(t.TempDir(), "cache"), MaxLength: 512, BatchSize: 32, Voyage: config.VoyageConfig{
			Disabled: true, APIKeyFile: key("voyage-api"), EmbedModel: "voyage-code-3", RerankModel: "rerank-2.5", OutputDimension: 512, BaseURL: "https://voyage.example.test/v1",
		}},
		UI: config.UIConfig{Theme: "amber", Color: "ansi16", Glyphs: "ascii"},
	}
}

func configJSON(c *config.Config) ([]byte, error) {
	return json.Marshal(c)
}

func configJSONFromPath(path string) ([]byte, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return configJSON(cfg)
}
