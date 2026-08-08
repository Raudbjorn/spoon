package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDefault_firstRunWritesConfigAndReadme(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	var stderr bytes.Buffer
	cfg, err := EnsureDefault(&stderr)
	if err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if cfg == nil || cfg.Embedder.Backend != "fastembed" {
		t.Fatalf("expected fastembed-enabled defaults, got %+v", cfg)
	}

	path := filepath.Join(dir, "spoon", "config.json")
	loaded, lerr := Load(path)
	if lerr != nil {
		t.Fatalf("bootstrapped config not loadable: %v", lerr)
	}
	if loaded.Version != CurrentVersion || loaded.Embedder.Backend != "fastembed" {
		t.Errorf("bootstrapped config = %+v", loaded)
	}
	// Credentials must never be written by bootstrap.
	if len(loaded.GitHub.Tokens) != 0 {
		t.Errorf("bootstrap wrote tokens: %v", loaded.GitHub.Tokens)
	}

	readme, rerr := os.ReadFile(filepath.Join(dir, "spoon", "README.md"))
	if rerr != nil {
		t.Fatalf("README not written: %v", rerr)
	}
	for _, want := range []string{"SPOON_NO_CONFIG", "TURSO_DATABASE_URL", "embedder.backend", "spoon setup"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README missing %q", want)
		}
	}
	if !strings.Contains(stderr.String(), "first run: wrote default config") {
		t.Errorf("missing first-run notice:\n%s", stderr.String())
	}
}

func TestEnsureDefault_secondRunIsSilentAndKeepsEdits(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	var first bytes.Buffer
	if _, err := EnsureDefault(&first); err != nil {
		t.Fatal(err)
	}

	// User edits survive: change a field, re-run, nothing overwritten.
	path := filepath.Join(dir, "spoon", "config.json")
	edited, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	edited.Forge.Provider = "gitlab"
	if err := Save(path, edited); err != nil {
		t.Fatal(err)
	}

	var second bytes.Buffer
	cfg, err := EnsureDefault(&second)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Len() != 0 {
		t.Errorf("second run should be silent, got:\n%s", second.String())
	}
	if cfg.Forge.Provider != "gitlab" {
		t.Errorf("second run clobbered a user edit: %+v", cfg)
	}
}

func TestEnsureDefault_respectsNoConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("SPOON_NO_CONFIG", "1")

	var stderr bytes.Buffer
	cfg, err := EnsureDefault(&stderr)
	if cfg != nil || err != nil {
		t.Fatalf("SPOON_NO_CONFIG must disable bootstrap, got cfg=%v err=%v", cfg, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "spoon", "config.json")); !os.IsNotExist(statErr) {
		t.Error("bootstrap wrote a config despite SPOON_NO_CONFIG=1")
	}
}

func TestDefaultPath_noHomeFallsBackToEtc(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	// os.UserHomeDir consults $HOME on Unix; empty means unresolvable.
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath must not error without a home: %v", err)
	}
	if path != SystemPath() {
		t.Errorf("DefaultPath = %q, want %q", path, SystemPath())
	}
}

func TestLoadDefault_fallsBackToSystemPath(t *testing.T) {
	// A user with a home but no personal config picks up admin defaults from
	// the system path. The system path is not writable in tests, so this
	// asserts the fallback order indirectly: with neither file present the
	// result is (nil, nil), proving the user path miss consulted the system
	// path without erroring.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := LoadDefault()
	if cfg != nil || err != nil {
		t.Fatalf("expected (nil, nil) with no config anywhere, got cfg=%v err=%v", cfg, err)
	}
}

func TestLoad_legacyOpenVINOBackendNormalizes(t *testing.T) {
	// A config written by a pre-removal build must load, not brick every
	// command; reranker/labeler keys are unknown fields and simply ignored.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
		"version": 1,
		"embedder": {"backend": "openvino", "model": "keep-me"},
		"reranker": {"modelPath": "/old/reranker"},
		"labeler": {"modelPath": "/old/labeler"}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("legacy openvino config failed to load: %v", err)
	}
	if cfg.Embedder.Backend != "" {
		t.Errorf("backend = %q, want normalized empty (fastembed)", cfg.Embedder.Backend)
	}
	if cfg.Embedder.Model != "keep-me" {
		t.Errorf("non-backend embedder fields must survive normalization, got %q", cfg.Embedder.Model)
	}
}
