package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndCloneRetainExplicitFalsePresence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"github":{"proxy":{"enabled":false}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !FieldPresent(cfg, "github.proxy.enabled") {
		t.Fatal("explicit false lost during load")
	}
	clone, err := Clone(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !FieldPresent(clone, "github.proxy.enabled") {
		t.Fatal("explicit false lost during clone")
	}
}
