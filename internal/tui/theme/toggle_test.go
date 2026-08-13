package theme

import "testing"

func TestToggleDarkLightPreservesProfiles(t *testing.T) {
	ctx, err := ResolveConfiguredContext("dark", "", "ansi16", "", "", "ascii", "")
	if err != nil {
		t.Fatal(err)
	}
	got := ToggleDarkLight(ctx)
	if got.ColorProfile != Ansi16 || got.GlyphProfile != Ascii {
		t.Fatalf("profiles changed: color=%v glyph=%v", got.ColorProfile, got.GlyphProfile)
	}
	if got.Palette != resolvePalette(Light, Ansi16) {
		t.Fatal("dark did not toggle to light")
	}
	if back := ToggleDarkLight(got); back != ctx {
		t.Fatal("light did not round-trip to the original context")
	}
}
