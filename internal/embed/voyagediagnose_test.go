package embed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func effectiveWith(t *testing.T, keyFile string, env map[string]string) config.EffectiveConfig {
	t.Helper()
	cfg := &config.Config{Embedder: config.EmbedderConfig{Voyage: config.VoyageConfig{APIKeyFile: keyFile}}}
	return config.ResolveEffectiveConfig(cfg, nil, env)
}

// TestDiagnoseVoyageKeySeparatesTheSilentStates is the reported problem in
// miniature. A user put a valid 0600 key file on disk, pointed nothing at it,
// and got "Voyage not configured" -- the same words an unconfigured host gets,
// and the same words a host with a typo'd path gets. Each of these needs a
// different action from the user, so each needs a different sentence.
func TestDiagnoseVoyageKeySeparatesTheSilentStates(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "voyage-ai")
	if err := os.WriteFile(good, []byte("pa-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(dir, "loose")
	if err := os.WriteFile(loose, []byte("pa-secret-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "not-there")

	for _, tt := range []struct {
		name     string
		keyFile  string
		env      map[string]string
		wantKey  bool
		contains []string
		absent   []string
	}{
		{
			name:     "nothing configured names both ways to configure it",
			env:      map[string]string{},
			contains: []string{"no key", "embedder.voyage.apiKeyFile", VoyageAPIKeyEnv},
		},
		{
			name:     "path set but file absent says so and names the path",
			keyFile:  missing,
			env:      map[string]string{},
			contains: []string{missing, "does not exist"},
		},
		{
			name:     "path set and readable resolves, naming the source",
			keyFile:  good,
			env:      map[string]string{},
			wantKey:  true,
			contains: []string{good, "embedder.voyage.apiKeyFile"},
		},
		{
			name:     "loose permissions are reported as the fixable error they are",
			keyFile:  loose,
			env:      map[string]string{},
			contains: []string{"chmod 600"},
		},
		{
			name:     "environment key wins and is named",
			env:      map[string]string{VoyageAPIKeyEnv: "pa-from-env"},
			wantKey:  true,
			contains: []string{VoyageAPIKeyEnv},
		},
		{
			name:     "explicitly disabled is not the same as unconfigured",
			keyFile:  good,
			env:      map[string]string{VoyageDisableEnv: "1"},
			contains: []string{"disabled"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := DiagnoseVoyageKey(effectiveWith(t, tt.keyFile, tt.env), false, tt.env)

			if got.HasKey != tt.wantKey {
				t.Errorf("HasKey = %v, want %v (summary %q)", got.HasKey, tt.wantKey, got.Summary)
			}
			for _, want := range tt.contains {
				if !strings.Contains(got.Summary, want) {
					t.Errorf("summary %q does not mention %q", got.Summary, want)
				}
			}
			for _, unwanted := range tt.absent {
				if strings.Contains(got.Summary, unwanted) {
					t.Errorf("summary %q unexpectedly mentions %q", got.Summary, unwanted)
				}
			}
			if strings.Contains(got.Summary, "pa-secret") {
				t.Fatalf("summary leaked the key material: %q", got.Summary)
			}
		})
	}
}

// TestDiagnoseVoyageKeyNeverLeaksTheSecret is stated separately because it is
// the one property that must hold in every branch, including future ones.
func TestDiagnoseVoyageKeyNeverLeaksTheSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	const secret = "pa-do-not-print-me"
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, env := range []map[string]string{
		{},
		{VoyageAPIKeyEnv: secret},
		{VoyageAPIKeyEnvAlt: secret},
	} {
		got := DiagnoseVoyageKey(effectiveWith(t, path, env), false, env)
		if strings.Contains(got.Summary, secret) {
			t.Fatalf("summary leaked the key: %q", got.Summary)
		}
	}
}
