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

func TestGlyphTableHasEqualWidthOneRuneASCIIAlternatives(t *testing.T) {
	if len(glyphTable) == 0 {
		t.Fatal("glyph table is empty")
	}
	for name, glyph := range glyphTable {
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
