package config

import (
	"errors"
	"os"
	"testing"
)

func TestLoadDefaultWithLayerDistinguishesMissingAndInvalidWithoutHostFiles(t *testing.T) {
	oldDefault, oldSystem, oldLoad := defaultPathForLoad, systemPathForLoad, loadForLayer
	t.Cleanup(func() { defaultPathForLoad, systemPathForLoad, loadForLayer = oldDefault, oldSystem, oldLoad })
	defaultPathForLoad = func() (string, error) { return "/user/config.json", nil }
	systemPathForLoad = func() string { return "/system/config.json" }
	loadForLayer = func(string) (*Config, error) { return nil, os.ErrNotExist }
	if layer := LoadDefaultWithLayer(); layer.State != LayerMissing || layer.Path != "/user/config.json" || layer.System {
		t.Fatalf("missing = %#v", layer)
	}
	loadForLayer = func(path string) (*Config, error) {
		if path == "/user/config.json" {
			return nil, errors.New("bad JSON")
		}
		return nil, os.ErrNotExist
	}
	if layer := LoadDefaultWithLayer(); layer.State != LayerInvalid || layer.Reason == nil || layer.Path != "/user/config.json" {
		t.Fatalf("invalid = %#v", layer)
	}
}
