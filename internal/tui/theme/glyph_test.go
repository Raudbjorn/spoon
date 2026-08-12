package theme

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestParseGlyphProfile(t *testing.T) {
	cases := []struct {
		value string
		want  GlyphProfile
	}{
		{"", Unicode},
		{"unicode", Unicode},
		{"ascii", Ascii},
	}
	for _, tt := range cases {
		t.Run(tt.value, func(t *testing.T) {
			got, err := ParseGlyphProfile(tt.value)
			if err != nil || got != tt.want {
				t.Fatalf("ParseGlyphProfile(%q) = %v, %v; want %v, nil", tt.value, got, err, tt.want)
			}
		})
	}
	if _, err := ParseGlyphProfile("wingdings"); err == nil {
		t.Fatal("ParseGlyphProfile accepted an unknown profile")
	}
}

func TestGlyphInventoryHasEqualWidthOneRuneASCIIAlternatives(t *testing.T) {
	if len(allGlyphs) == 0 {
		t.Fatal("glyph inventory is empty")
	}
	if len(glyphTable) != len(allGlyphs) {
		t.Fatalf("glyph table has %d mappings, inventory has %d", len(glyphTable), len(allGlyphs))
	}
	for _, name := range allGlyphs {
		glyph, ok := glyphTable[name]
		if !ok {
			t.Fatalf("glyph inventory entry %q has no mapping", name)
		}
		if glyph.Unicode == "" || glyph.ASCII == "" {
			t.Fatalf("%s has an empty glyph", name)
		}
		if len([]rune(glyph.ASCII)) != 1 {
			t.Fatalf("%s ASCII fallback must be one rune: %q", name, glyph.ASCII)
		}
		asciiRune := []rune(glyph.ASCII)[0]
		if asciiRune > 0x7f || asciiRune < 0x21 {
			t.Fatalf("%s ASCII fallback must be an ASCII graphic rune: %q", name, glyph.ASCII)
		}
		if got, want := lipgloss.Width(glyph.ASCII), lipgloss.Width(glyph.Unicode); got != want {
			t.Fatalf("%s width = %d, want %d (%q -> %q)", name, got, want, glyph.Unicode, glyph.ASCII)
		}
	}
	for name := range glyphTable {
		found := false
		for _, expected := range allGlyphs {
			if name == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("glyph table mapping %q is absent from authoritative inventory", name)
		}
	}
}

func TestASCIIFallbackMatchesUpstreamRules(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"a→", "a"},
		{"─", "-"},
		{"│", "|"},
		{"✓", "+"},
		{"⚠", "!"},
		{"✘", "x"},
		{"←", "<"},
		{"↑", "^"},
		{"↓", "v"},
		{"┌", "+"},
		{"░", "#"},
		{"⠁", "*"},
		{"中", "?"},
		{"\ue000", "?"},
		{"🔥", "*"},
		{"\u200d", "*"},
		{"Ω", "?"},
	}
	for _, tt := range cases {
		t.Run(tt.input, func(t *testing.T) {
			if got := ASCIIFallback(tt.input); got != tt.want {
				t.Fatalf("ASCIIFallback(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestContextGlyphSelectsProfile(t *testing.T) {
	if got := DefaultContext().Glyph(ArrowRight); got != "→" {
		t.Fatalf("unicode arrow = %q", got)
	}
	ascii := DefaultContext()
	ascii.GlyphProfile = Ascii
	if got := ascii.Glyph(ArrowRight); got != "-" {
		t.Fatalf("ASCII arrow = %q", got)
	}
}
