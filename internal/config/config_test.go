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

func TestLayerEmbedder(t *testing.T) {
	cfg := &Config{Embedder: EmbedderConfig{
		Backend: "openai", Endpoint: "http://ovms:8978", Model: "nomic-ai/x",
		SidecarEndpoint: "http://side:8766", LabelerModel: "llama3.2:3b",
	}}

	t.Run("no flag backend adopts config fully", func(t *testing.T) {
		b, ep, m, _, lab := cfg.LayerEmbedder("", "", "", "", "")
		if b != "openai" || ep != "http://ovms:8978" || m != "nomic-ai/x" || lab != "llama3.2:3b" {
			t.Errorf("got backend=%q ep=%q model=%q labeler=%q", b, ep, m, lab)
		}
	})

	t.Run("switching backend does NOT inherit the saved endpoint/model", func(t *testing.T) {
		b, ep, m, _, lab := cfg.LayerEmbedder("ollama", "", "", "", "")
		if b != "ollama" {
			t.Errorf("backend=%q want ollama", b)
		}
		if ep != "" || m != "" {
			t.Errorf("openai endpoint/model leaked into ollama: ep=%q model=%q", ep, m)
		}
		if lab != "llama3.2:3b" {
			t.Errorf("labeler is backend-agnostic, want it layered; got %q", lab)
		}
	})

	t.Run("matching backend inherits endpoint/model", func(t *testing.T) {
		_, ep, m, _, _ := cfg.LayerEmbedder("openai", "", "", "", "")
		if ep != "http://ovms:8978" || m != "nomic-ai/x" {
			t.Errorf("ep=%q model=%q", ep, m)
		}
	})

	t.Run("flag overrides config endpoint", func(t *testing.T) {
		_, ep, _, _, _ := cfg.LayerEmbedder("openai", "http://flag:1", "", "", "")
		if ep != "http://flag:1" {
			t.Errorf("ep=%q want flag value", ep)
		}
	})
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
