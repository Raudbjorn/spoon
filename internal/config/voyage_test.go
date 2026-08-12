package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVoyageAPIKeyFileIsPermissionGated is the regression guard for the trap this
// field walks into: configContainsCredentials names the fields the 0600 gate
// applies to, so a credential field added without extending it silently escapes
// the check that protects every other credential in this file.
func TestVoyageAPIKeyFileIsPermissionGated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	keyFile := filepath.Join(dir, "voyage.key")

	if err := os.WriteFile(keyFile, []byte("sk-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Appendf(nil, `{"version":1,"embedder":{"voyage":{"apiKeyFile":%q}}}`, keyFile)

	// A config naming a key file counts as credential-bearing, so the config
	// file's own mode is enforced.
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("a group-readable config naming a Voyage key file loaded without complaint: %v", err)
	}

	// With the config secured, the referenced file's mode is enforced too.
	// Chmod explicitly: WriteFile does not change the mode of an existing file.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load with a 0600 key file: %v", err)
	}
	if err := os.Chmod(keyFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "embedder.voyage.apiKeyFile") {
		t.Fatalf("a world-readable Voyage key file was accepted: %v", err)
	}
}

func TestVoyageValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  VoyageConfig
		want string
	}{
		{name: "bad dimension", cfg: VoyageConfig{OutputDimension: 777}, want: "outputDimension"},
		{name: "relative base URL", cfg: VoyageConfig{BaseURL: "api.voyageai.com"}, want: "baseUrl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Embedder: EmbedderConfig{Voyage: tc.cfg}}
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate() = %v, want an error naming %q", err, tc.want)
			}
		})
	}
	// Unset and every legal dimension must pass.
	for _, dim := range []int{0, 256, 512, 1024, 2048} {
		c := &Config{Embedder: EmbedderConfig{Voyage: VoyageConfig{OutputDimension: dim}}}
		if err := c.Validate(); err != nil {
			t.Errorf("Validate() rejected dimension %d: %v", dim, err)
		}
	}
}

// TestVoyageBackendIsNotAnEmbedderBackend pins the design decision: Voyage runs
// in addition to fastembed, so it must never become a mutually-exclusive
// embedder.backend value. Accepting one there would make a config that reads as
// "use Voyage" silently mean "stop using fastembed".
func TestVoyageBackendIsNotAnEmbedderBackend(t *testing.T) {
	c := &Config{Embedder: EmbedderConfig{Backend: "voyage"}}
	if err := c.Validate(); err == nil {
		t.Error("embedder.backend=voyage was accepted; Voyage is a sibling of fastembed, not a replacement")
	}
}

func TestReadCredentialFile(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, []byte("  sk-trimmed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCredentialFile(keyFile, "embedder.voyage.apiKeyFile")
	if err != nil {
		t.Fatalf("ReadCredentialFile: %v", err)
	}
	if got != "sk-trimmed" {
		t.Errorf("got %q, want the trimmed key", got)
	}
	// An unset path and a stale path both mean "no credential", not an error:
	// a config entry pointing at a deleted file must not fail every command.
	if got, err := ReadCredentialFile("", "field"); got != "" || err != nil {
		t.Errorf("empty path = (%q, %v), want (\"\", nil)", got, err)
	}
	if got, err := ReadCredentialFile(filepath.Join(dir, "absent"), "field"); got != "" || err != nil {
		t.Errorf("absent path = (%q, %v), want (\"\", nil)", got, err)
	}
}
