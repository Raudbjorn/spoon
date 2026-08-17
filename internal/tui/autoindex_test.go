package tui

import (
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/tui/settings"
)

func autoIndexModel(t *testing.T, cfg *config.Config, env map[string]string) *Model {
	t.Helper()
	m := refreshModel(t)
	effective := config.ResolveEffectiveConfig(cfg, nil, env)
	m.settings = settings.New(cfg, t.TempDir()+"/config.json", nil).
		WithEnvironment(env).
		WithEffective(effective)
	return m
}

// TestAutoIndexDefaultsFavourTheFreeProvider is the money question. FastEmbed
// costs CPU, so it runs unasked; Voyage bills per token, so opening a large
// fork network must never begin spending on its own.
func TestAutoIndexDefaultsFavourTheFreeProvider(t *testing.T) {
	m := autoIndexModel(t, &config.Config{}, map[string]string{})

	fast, voyage := m.autoIndexProviders()
	if !fast {
		t.Error("fastembed auto-index is off by default; the local embedder is free and should just run")
	}
	if voyage {
		t.Error("Voyage auto-index is ON by default; opening a fork list would start billing unasked")
	}
}

// TestAutoIndexHonoursExplicitSettings: both directions must be reachable.
func TestAutoIndexHonoursExplicitSettings(t *testing.T) {
	off := false
	cfg := &config.Config{Embedder: config.EmbedderConfig{
		AutoIndex: &off,
		Voyage:    config.VoyageConfig{AutoIndex: true},
	}}
	config.RecordFieldValue(cfg, "embedder.autoIndex", "false")
	config.RecordFieldValue(cfg, "embedder.voyage.autoIndex", "true")

	m := autoIndexModel(t, cfg, map[string]string{})
	fast, voyage := m.autoIndexProviders()
	if fast {
		t.Error("fastembed auto-index stayed on after being explicitly disabled")
	}
	if !voyage {
		t.Error("Voyage auto-index stayed off after being explicitly enabled")
	}
}

// TestAutoIndexTreatsGarbageAsTheSafeDefault: an unparseable setting must never
// be read as consent to spend money.
func TestAutoIndexTreatsGarbageAsTheSafeDefault(t *testing.T) {
	m := autoIndexModel(t, &config.Config{}, map[string]string{
		"SPOON_AUTO_INDEX":        "yes-please",
		"SPOON_VOYAGE_AUTO_INDEX": "yes-please",
	})

	fast, voyage := m.autoIndexProviders()
	if !fast {
		t.Error("garbage in SPOON_AUTO_INDEX disabled the free local embedder")
	}
	if voyage {
		t.Error("garbage in SPOON_VOYAGE_AUTO_INDEX was read as permission to bill")
	}
}

// TestAutoIndexRunsOncePerForkList: raising the tier ceiling re-enriches and
// produces a second completion message. That must not start a second pass,
// which under Voyage would be a second bill.
func TestAutoIndexRunsOncePerForkList(t *testing.T) {
	m := autoIndexModel(t, &config.Config{}, map[string]string{})
	db, _ := documentStore(t)
	m.db = db

	if cmd := m.maybeAutoIndex(); cmd == nil {
		t.Fatal("the first automatic pass did not start")
	}
	if !m.autoIndexDone {
		t.Fatal("the automatic pass did not latch")
	}
	if cmd := m.maybeAutoIndex(); cmd != nil {
		t.Fatal("a second enrichment completion started another automatic pass")
	}
}

// TestAutoIndexLatchResetsForANewForkList: latching per session rather than per
// list would mean a refreshed list is never indexed.
func TestAutoIndexLatchResetsForANewForkList(t *testing.T) {
	m := autoIndexModel(t, &config.Config{}, map[string]string{})
	m.autoIndexDone = true

	m.doRefresh()

	if m.autoIndexDone {
		t.Fatal("refresh kept the latch, so the refreshed list would never auto-index")
	}
}

// TestAutoIndexSilentWhenNothingIsEnabled: a pass the user disabled should not
// announce itself.
func TestAutoIndexSilentWhenNothingIsEnabled(t *testing.T) {
	off := false
	cfg := &config.Config{Embedder: config.EmbedderConfig{AutoIndex: &off}}
	config.RecordFieldValue(cfg, "embedder.autoIndex", "false")

	m := autoIndexModel(t, cfg, map[string]string{})
	if cmd := m.maybeAutoIndex(); cmd != nil {
		t.Fatal("an automatic pass ran with both providers disabled")
	}
	if m.errMsg != "" {
		t.Fatalf("a disabled automatic pass reported something: %q", m.errMsg)
	}
}
