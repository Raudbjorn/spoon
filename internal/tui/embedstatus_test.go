package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/tui/settings"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func embedStatusModel(t *testing.T, cfg *config.Config, env map[string]string) *Model {
	t.Helper()
	ctx, err := theme.ResolveContext("", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	effective := config.ResolveEffectiveConfig(cfg, nil, env)
	settingsModel := settings.New(cfg, filepath.Join(t.TempDir(), "config.json"), nil).
		WithEnvironment(env).
		WithEffective(effective)

	m := newClusterTestModel([]ScoredFork{makeSF("alice/tool", 90, "", "", 0)})
	themed := m.WithTheme(ctx).WithSettings(settingsModel)
	themed.view, themed.width, themed.height = viewTable, 120, 30
	return &themed
}

// TestEmbedStatusAnswersWhetherTheProvidersAreUsed is the user-facing question
// behind the whole feature: "there is no way to confirm it actually gets used".
// Two facts have to be visible without leaving the table -- whether the local
// embedder can run, and whether the paid one is configured.
func TestEmbedStatusAnswersWhetherTheProvidersAreUsed(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "voyage-ai")
	if err := os.WriteFile(keyFile, []byte("pa-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	modelCache := filepath.Join(dir, "cache")
	if err := os.MkdirAll(filepath.Join(modelCache, "fast-bge-small-en-v1.5"), 0o700); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(dir, "libonnxruntime.so")
	if err := os.WriteFile(lib, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("nothing configured says so for both", func(t *testing.T) {
		m := embedStatusModel(t, &config.Config{}, map[string]string{})
		m.refreshEmbedStatus()

		line := m.embedStatusLine()
		if !strings.Contains(line, "fastembed") || !strings.Contains(line, "voyage") {
			t.Fatalf("status line names neither provider: %q", line)
		}
		if !strings.Contains(line, "no key") {
			t.Errorf("status line does not say Voyage has no key: %q", line)
		}
	})

	t.Run("configured providers are reported as ready", func(t *testing.T) {
		cfg := &config.Config{Embedder: config.EmbedderConfig{
			CacheDir: modelCache,
			Voyage:   config.VoyageConfig{APIKeyFile: keyFile},
		}}
		m := embedStatusModel(t, cfg, map[string]string{embed.ONNXPathEnv: lib})
		m.refreshEmbedStatus()

		line := m.embedStatusLine()
		if !strings.Contains(line, "ready") {
			t.Errorf("fastembed with model and runtime present is not reported ready: %q", line)
		}
		if !strings.Contains(line, "key") {
			t.Errorf("voyage with a resolvable key file is not reported configured: %q", line)
		}
		if strings.Contains(line, "pa-secret") {
			t.Fatalf("status line leaked the key: %q", line)
		}
	})

	t.Run("a missing model is distinguished from a missing runtime", func(t *testing.T) {
		noModel := embedStatusModel(t, &config.Config{Embedder: config.EmbedderConfig{CacheDir: filepath.Join(dir, "empty")}},
			map[string]string{embed.ONNXPathEnv: lib})
		noModel.refreshEmbedStatus()

		noRuntime := embedStatusModel(t, &config.Config{Embedder: config.EmbedderConfig{CacheDir: modelCache}},
			map[string]string{})
		noRuntime.refreshEmbedStatus()

		if noModel.embedStatusLine() == noRuntime.embedStatusLine() {
			t.Fatalf("a missing model and a missing runtime report identically: %q", noModel.embedStatusLine())
		}
	})
}

// TestEmbedStatusReachesTheStatusBar: the value of this is that it is visible
// without going looking for it, so it has to actually render.
func TestEmbedStatusReachesTheStatusBar(t *testing.T) {
	m := embedStatusModel(t, &config.Config{}, map[string]string{})
	m.refreshEmbedStatus()

	if !strings.Contains(m.viewTable(), "emb:") {
		t.Fatalf("table frame does not carry the embedder status:\n%s", m.viewTable())
	}
}

// TestEmbedStatusReprobesAfterSettingsClose: the settings panel is where the
// user fixes "Voyage never reads my key file", so a status line that keeps its
// startup answer would report the problem as unfixed immediately after it was
// fixed -- until the next restart.
func TestEmbedStatusReprobesAfterSettingsClose(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "voyage-ai")
	if err := os.WriteFile(keyFile, []byte("pa-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	m := embedStatusModel(t, cfg, map[string]string{})
	m.refreshEmbedStatus()
	if before := m.embedStatusLine(); !strings.Contains(before, "no key") {
		t.Fatalf("precondition: expected no key, got %q", before)
	}

	// Stand in for the user editing embedder.voyage.apiKeyFile in the panel:
	// the settings model recomputes Effective after each accepted edit.
	cfg.Embedder.Voyage.APIKeyFile = keyFile
	m.settings = m.settings.WithEffective(config.ResolveEffectiveConfig(cfg, nil, map[string]string{}))

	// CloseRequested is only interpreted while the settings view is the active
	// one, which is the state the user is in when they press esc.
	m.view = viewSettings
	updated, _ := m.Update(settings.CloseRequested{})
	after := rootModel(t, updated)

	if line := after.embedStatusLine(); !strings.Contains(line, "key set") {
		t.Fatalf("status did not re-probe after the settings panel closed: %q", line)
	}
}
