package settings

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/tui/internal/rendertest"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func TestModelSelect(t *testing.T) {
	t.Run("closed row shows status", func(t *testing.T) {
		cache := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cache, "fast-bge-small-en-v1.5"), 0o700); err != nil {
			t.Fatal(err)
		}
		m := newModelSelect(t, cache)
		view := m.View()
		if !strings.Contains(view, "fast-bge-small-en-v1.5") || !strings.Contains(view, "ready") {
			t.Fatalf("closed row missing ready current model: %q", view)
		}
		for _, name := range []string{"fast-bge-base-en-v1.5", "fast-all-MiniLM-L6-v2", "fast-bge-small-zh-v1.5"} {
			if strings.Contains(view, name) {
				t.Fatalf("closed row leaked other model %s: %q", name, view)
			}
		}
	})

	t.Run("open list lists all six with mixed status", func(t *testing.T) {
		cache := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cache, "fast-bge-small-en-v1.5"), 0o700); err != nil {
			t.Fatal(err)
		}
		m := newModelSelect(t, cache)
		m = pressSettingsKey(t, m, "e")
		if !m.selecting {
			t.Fatal("selecting = false after e")
		}
		view := m.View()
		for _, name := range []string{
			"fast-bge-small-en-v1.5",
			"fast-bge-small-en",
			"fast-bge-base-en-v1.5",
			"fast-bge-base-en",
			"fast-bge-small-zh-v1.5",
			"fast-all-MiniLM-L6-v2",
		} {
			if !strings.Contains(view, name) {
				t.Fatalf("open list missing %s: %q", name, view)
			}
		}
		if !strings.Contains(view, "fast-bge-small-en-v1.5") || !strings.Contains(view, "ready") {
			t.Fatalf("default row not ready: %q", view)
		}
		if !strings.Contains(view, "fast-bge-base-en-v1.5") || !strings.Contains(view, "download") {
			t.Fatalf("base-en not download: %q", view)
		}
	})

	t.Run("select cached other model confirms reindex", func(t *testing.T) {
		cache := t.TempDir()
		for _, name := range []string{"fast-bge-small-en-v1.5", "fast-bge-base-en-v1.5"} {
			if err := os.MkdirAll(filepath.Join(cache, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		called := 0
		m := newModelSelect(t, cache).WithActionDeps(ActionDeps{
			ProvisionFastEmbed: func(context.Context, string, string) error {
				t.Fatal("provisioner called for cached model")
				return nil
			},
		})
		m = focusSelectOption(t, m, "fast-bge-base-en-v1.5")
		m = pressSettingsKey(t, m, "enter")
		if !m.confirming || m.pendingInstall != "" {
			t.Fatalf("confirming=%v pendingInstall=%q", m.confirming, m.pendingInstall)
		}
		m = pressSettingsKey(t, m, "enter")
		if m.Config.Embedder.Model != "fast-bge-base-en-v1.5" {
			t.Fatalf("model = %q", m.Config.Embedder.Model)
		}
		if called != 0 {
			t.Fatal("provisioner called")
		}
	})

	t.Run("select missing model offers install", func(t *testing.T) {
		cache := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cache, "fast-bge-small-en-v1.5"), 0o700); err != nil {
			t.Fatal(err)
		}
		m := newModelSelect(t, cache)
		m = focusSelectOption(t, m, "fast-bge-base-en-v1.5")
		m = pressSettingsKey(t, m, "enter")
		if m.pendingInstall != "fast-bge-base-en-v1.5" {
			t.Fatalf("pendingInstall = %q", m.pendingInstall)
		}
		if !strings.Contains(m.View(), "Download and install") {
			t.Fatalf("modal missing install text: %q", m.View())
		}
		before := m.Config.Embedder.Model
		updated, cmd := updateSettingsKey(t, m, "esc")
		m = updated
		if cmd != nil {
			t.Fatal("esc returned a command")
		}
		if m.Config.Embedder.Model != before || m.confirming {
			t.Fatalf("cancel mutated state: model=%q confirming=%v", m.Config.Embedder.Model, m.confirming)
		}
	})

	t.Run("confirm install applies after provision succeeds", func(t *testing.T) {
		cache := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cache, "fast-bge-small-en-v1.5"), 0o700); err != nil {
			t.Fatal(err)
		}
		var got []string
		m := newModelSelect(t, cache).WithActionDeps(ActionDeps{
			ProvisionFastEmbed: func(_ context.Context, _, model string) error {
				got = append(got, model)
				return os.MkdirAll(filepath.Join(cache, model), 0o700)
			},
		})
		m = focusSelectOption(t, m, "fast-bge-base-en-v1.5")
		m = pressSettingsKey(t, m, "enter")
		updated, cmd := updateSettingsKey(t, m, "enter")
		m = updated
		if cmd == nil {
			t.Fatal("confirm install returned no command")
		}
		if m.confirming {
			t.Fatal("confirming still true after install confirm")
		}
		m = deliverSettingsCmd(t, m, cmd)
		if m.Config.Embedder.Model != "fast-bge-base-en-v1.5" || m.Config.Embedder.MaxLength != 512 {
			t.Fatalf("applied %#v", m.Config.Embedder)
		}
		if !strings.Contains(m.alert, "Installed") {
			t.Fatalf("alert = %q", m.alert)
		}
		if len(got) != 1 || got[0] != "fast-bge-base-en-v1.5" {
			t.Fatalf("provision calls = %v", got)
		}
	})

	t.Run("provision failure does not change Config", func(t *testing.T) {
		cache := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cache, "fast-bge-small-en-v1.5"), 0o700); err != nil {
			t.Fatal(err)
		}
		m := newModelSelect(t, cache).WithActionDeps(ActionDeps{
			ProvisionFastEmbed: func(context.Context, string, string) error {
				return fmt.Errorf("boom")
			},
		})
		m = focusSelectOption(t, m, "fast-bge-base-en-v1.5")
		m = pressSettingsKey(t, m, "enter")
		before := m.Config.Embedder.Model
		updated, cmd := updateSettingsKey(t, m, "enter")
		m = updated
		if cmd == nil {
			t.Fatal("failed install returned no command")
		}
		if m.confirming {
			t.Fatal("confirming still true after failed-install confirm")
		}
		m = deliverSettingsCmd(t, m, cmd)
		if m.Config.Embedder.Model != before {
			t.Fatalf("model changed to %q", m.Config.Embedder.Model)
		}
		if !strings.Contains(m.alert, "boom") {
			t.Fatalf("alert = %q", m.alert)
		}
	})

	t.Run("unknown typed values are gone", func(t *testing.T) {
		_, err := Candidate(FieldByMust("embedder.model"), &config.Config{}, "candidate-model")
		if err == nil {
			t.Fatal("candidate-model accepted")
		}
	})

	t.Run("mono ascii shows ready and download", func(t *testing.T) {
		rendertest.Force(t, termenv.Ascii)
		ctx, err := theme.ResolveContext("dark", "", "mono", "", "ascii")
		if err != nil {
			t.Fatal(err)
		}
		cache := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cache, "fast-bge-small-en-v1.5"), 0o700); err != nil {
			t.Fatal(err)
		}
		m := newModelSelect(t, cache).WithTheme(ctx)
		m = pressSettingsKey(t, m, "e")
		view := m.View()
		if !strings.Contains(view, "ready") || !strings.Contains(view, "download") {
			t.Fatalf("mono/ascii list missing status words: %q", view)
		}
	})
}

func newModelSelect(t *testing.T, cache string) Model {
	t.Helper()
	cfg := &config.Config{Embedder: config.EmbedderConfig{Model: "fast-bge-small-en-v1.5", CacheDir: cache}}
	m := selectSettingsField(New(cfg, filepath.Join(t.TempDir(), "config.json"), nil), "embedder.model")
	m.width, m.height = 80, 24
	return m
}

func pressSettingsKey(t *testing.T, m Model, key string) Model {
	t.Helper()
	updated, _ := updateSettingsKey(t, m, key)
	return updated
}

func updateSettingsKey(t *testing.T, m Model, key string) (Model, tea.Cmd) {
	t.Helper()
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "e":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	updated, cmd := m.Update(msg)
	return updated.(Model), cmd
}

func deliverSettingsCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected command")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, next := range batch {
			updated, _ := m.Update(next())
			m = updated.(Model)
		}
		return m
	}
	updated, _ := m.Update(msg)
	return updated.(Model)
}

func focusSelectOption(t *testing.T, m Model, name string) Model {
	t.Helper()
	m = pressSettingsKey(t, m, "e")
	for range 6 {
		if m.selectIndex >= 0 && m.selectIndex < len(m.selectOptions) && m.selectOptions[m.selectIndex].Name == name {
			return m
		}
		m = pressSettingsKey(t, m, "down")
	}
	t.Fatalf("option %s not in select list", name)
	return m
}
