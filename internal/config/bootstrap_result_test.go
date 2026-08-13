package config

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func TestBootstrapRetainsMissingLayerAfterPublicationFailure(t *testing.T) {
	oldDefault, oldSystem, oldLoad, oldSave := defaultPathForLoad, systemPathForLoad, loadForLayer, saveForBootstrap
	t.Cleanup(func() {
		defaultPathForLoad, systemPathForLoad, loadForLayer, saveForBootstrap = oldDefault, oldSystem, oldLoad, oldSave
	})
	defaultPathForLoad = func() (string, error) { return "/user/config.json", nil }
	systemPathForLoad = func() string { return "/system/config.json" }
	loads := 0
	loadForLayer = func(path string) (*Config, error) { loads++; return nil, os.ErrNotExist }
	saveForBootstrap = func(string, *Config) error { return errors.New("publication denied") }

	result := Bootstrap(&bytes.Buffer{})
	if result.Config == nil || result.Config.Embedder.Backend != "fastembed" {
		t.Fatalf("config = %#v", result.Config)
	}
	if result.Layer.State != LayerMissing || result.Layer.Path != "/user/config.json" || result.Layer.Reason == nil {
		t.Fatalf("layer = %#v", result.Layer)
	}
	if result.Layer.Config != result.Config {
		t.Fatal("runtime and settings did not receive the same config pointer")
	}
	if loads != 2 {
		t.Fatalf("loads = %d, want exactly initial user/system selection", loads)
	}
}
