package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultWithLayerDistinguishesDisabledMissingAndUser(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	missing := LoadDefaultWithLayer()
	if missing.Disabled || missing.Config != nil || missing.Path != SystemPath() {
		t.Fatalf("missing layer=%+v", missing)
	}
	t.Setenv("SPOON_NO_CONFIG", "1")
	disabled := LoadDefaultWithLayer()
	if !disabled.Disabled || disabled.Path != "" {
		t.Fatalf("disabled layer=%+v", disabled)
	}
	t.Setenv("SPOON_NO_CONFIG", "")
	path := filepath.Join(dir, "spoon", "config.json")
	if err := Save(path, &Config{Forge: ForgeConfig{Provider: "github"}}); err != nil {
		t.Fatal(err)
	}
	loaded := LoadDefaultWithLayer()
	if loaded.Config == nil || loaded.Path != path || loaded.System {
		t.Fatalf("user layer=%+v", loaded)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
