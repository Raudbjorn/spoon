package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPath_XDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xc")
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != "/tmp/xc/spoon/config.json" {
		t.Errorf("path=%q", p)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	in := &Config{
		Forge:    ForgeConfig{Provider: "github"},
		Embedder: EmbedderConfig{Backend: "openai", Endpoint: "http://x:8978", Model: "m"},
	}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	if in.Version != CurrentVersion {
		t.Errorf("Save did not stamp version: %d", in.Version)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Embedder.Backend != "openai" || out.Embedder.Endpoint != "http://x:8978" || out.Forge.Provider != "github" {
		t.Errorf("roundtrip mismatch: %+v", out)
	}
}

func TestLoad_NotExist(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err=%v want os.ErrNotExist", err)
	}
}

func TestValidate_RejectsBadEnums(t *testing.T) {
	if err := (&Config{Forge: ForgeConfig{Provider: "bitbucket"}}).Validate(); err == nil {
		t.Error("expected provider rejection")
	}
	if err := (&Config{Embedder: EmbedderConfig{Backend: "magic"}}).Validate(); err == nil {
		t.Error("expected backend rejection")
	}
	if err := (&Config{Forge: ForgeConfig{Provider: "GitHub"}, Embedder: EmbedderConfig{Backend: "OpenAI"}}).Validate(); err != nil {
		t.Errorf("case-insensitive enums should pass: %v", err)
	}
}

func TestCoalesce(t *testing.T) {
	if got := Coalesce("", "", "c"); got != "c" {
		t.Errorf("got %q", got)
	}
	if got := Coalesce("a", "b"); got != "a" {
		t.Errorf("flag should win: %q", got)
	}
	if got := Coalesce("", ""); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestLoadDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// Absent → (nil, nil).
	c, err := LoadDefault()
	if c != nil || err != nil {
		t.Fatalf("absent: c=%v err=%v", c, err)
	}

	// Present → loaded.
	p, _ := DefaultPath()
	if err := Save(p, &Config{Embedder: EmbedderConfig{Backend: "openai"}}); err != nil {
		t.Fatal(err)
	}
	c, err = LoadDefault()
	if err != nil || c == nil || c.Embedder.Backend != "openai" {
		t.Fatalf("present: c=%v err=%v", c, err)
	}

	// SPOON_NO_CONFIG disables.
	t.Setenv("SPOON_NO_CONFIG", "1")
	c, err = LoadDefault()
	if c != nil || err != nil {
		t.Fatalf("disabled: c=%v err=%v", c, err)
	}
}

func TestSave_RejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, &Config{Embedder: EmbedderConfig{Backend: "nope"}}); err == nil {
		t.Error("Save should reject invalid config")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("invalid config must not be written")
	}
}
