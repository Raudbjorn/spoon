package theme

import "testing"

func TestToggleDarkLightPreservesProfiles(t *testing.T) {
	for _, profile := range []ColorProfile{TrueColor, Ansi16, Mono, NoColor} {
		t.Run(profile.String(), func(t *testing.T) {
			ctx, err := ResolveConfiguredContext("dark", "", profile.String(), "", "", "ascii", "")
			if err != nil {
				t.Fatal(err)
			}
			got := ToggleDarkLight(ctx)
			if got.ColorProfile != profile || got.GlyphProfile != Ascii {
				t.Fatalf("profiles changed: color=%v glyph=%v", got.ColorProfile, got.GlyphProfile)
			}
			if got.paletteName != "light" || got.Palette != resolvePalette(Light, profile) {
				t.Fatal("dark did not toggle to light before quantization")
			}
			if back := ToggleDarkLight(got); back != ctx {
				t.Fatal("light did not round-trip to the original context")
			}
		})
	}
}
