package config

import (
	"encoding/json"
	"testing"
)

func TestConfigDecodesAppearanceFieldsWithoutSchemaMigration(t *testing.T) {
	var got Config
	if err := json.Unmarshal([]byte(`{"version":1,"ui":{"theme":"amber","color":"ansi16","glyphs":"ascii"}}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 {
		t.Fatalf("Version = %d, want 1", got.Version)
	}
	if got.UI != (UIConfig{Theme: "amber", Color: "ansi16", Glyphs: "ascii"}) {
		t.Fatalf("UI = %#v", got.UI)
	}
}
