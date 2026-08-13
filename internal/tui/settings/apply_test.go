package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestApplyValidatesBeforeAtomicSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	before := []byte(`{"version":1,"forge":{"provider":"github"}}`)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.GitHub.RequestsPerMinute = 10000
	if err := Save(path, cfg); err == nil {
		t.Fatal("invalid config saved")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("invalid save changed file: %s", after)
	}
}

func TestApplyClearsEmptySecretInsteadOfSerializingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &config.Config{GitHub: config.GitHubConfig{Tokens: []string{"sentinel"}}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.GitHub.Tokens = []string{""}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	out, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.GitHub.Tokens) != 0 {
		t.Fatalf("empty token serialized: %#v", out.GitHub.Tokens)
	}
}
