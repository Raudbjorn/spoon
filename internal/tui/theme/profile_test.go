package theme

import (
	"github.com/muesli/termenv"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestParseColorProfile(t *testing.T) {
	cases := []struct {
		value string
		want  ColorProfile
	}{
		{"", TrueColor},
		{"truecolor", TrueColor},
		{"ansi256", Ansi256},
		{"ansi16", Ansi16},
		{"ansi8", Ansi8},
		{"mono", Mono},
		{"no-color", NoColor},
	}
	for _, tt := range cases {
		t.Run(tt.value, func(t *testing.T) {
			got, err := ParseColorProfile(tt.value)
			if err != nil || got != tt.want {
				t.Fatalf("ParseColorProfile(%q) = %v, %v; want %v, nil", tt.value, got, err, tt.want)
			}
		})
	}
	if _, err := ParseColorProfile("chartreuse"); err == nil {
		t.Fatal("ParseColorProfile accepted an unknown profile")
	}
}

func TestResolveColorProfilePrecedence(t *testing.T) {
	cases := []struct {
		name, color, noColor string
		want                 ColorProfile
	}{
		{"defaults", "", "", TrueColor},
		{"empty explicit is unset", "", "1", NoColor},
		{"no color", "", "1", NoColor},
		{"explicit wins", "ansi16", "1", Ansi16},
		{"explicit no-color", "no-color", "", NoColor},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveColorProfile(tt.color, tt.noColor)
			if err != nil || got != tt.want {
				t.Fatalf("ResolveColorProfile(%q, %q) = %v, %v; want %v, nil", tt.color, tt.noColor, got, err, tt.want)
			}
		})
	}
	if _, err := ResolveColorProfile("invalid", "1"); err == nil {
		t.Fatal("invalid explicit profile silently fell back")
	}
}

func TestResolvePaletteAllRolesAndProfiles(t *testing.T) {
	palettes := []Palette{Dark, Light, Amber}
	profiles := []ColorProfile{TrueColor, Ansi256, Ansi16, Ansi8, Mono, NoColor}
	for paletteIndex, palette := range palettes {
		for _, profile := range profiles {
			t.Run(string(rune('A'+paletteIndex))+"/"+profile.String(), func(t *testing.T) {
				got := resolvePalette(palette, profile)
				for _, color := range paletteColors(got) {
					if profile == Mono || profile == NoColor {
						if color != "" {
							t.Fatalf("%s palette color = %q; want no color", profile, color)
						}
					} else if color == "" {
						t.Fatalf("%s palette lost a role", profile)
					}
				}
			})
		}
	}
}

func TestQuantizeColorUpstreamParity(t *testing.T) {
	cases := []struct {
		name    string
		color   string
		profile ColorProfile
		want    string
	}{
		{"truecolor identity", "#4ec9b0", TrueColor, "#4ec9b0"},
		{"xterm cube", "#4ec9b0", Ansi256, "79"},
		{"ansi16", "#4ec9b0", Ansi16, "8"},
		{"ansi8", "#4ec9b0", Ansi8, "7"},
		{"cube gray", "#858585", Ansi256, "102"},
		{"mono", "#4ec9b0", Mono, ""},
		{"no-color", "#4ec9b0", NoColor, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := quantizeColor(lipgloss.Color(tt.color), tt.profile); string(got) != tt.want {
				t.Fatalf("quantizeColor(%q, %s) = %q; want %q", tt.color, tt.profile, got, tt.want)
			}
		})
	}
}

func TestMonoAndNoColorParity(t *testing.T) {
	for _, palette := range []Palette{Dark, Light, Amber} {
		mono := resolvePalette(palette, Mono)
		noColor := resolvePalette(palette, NoColor)
		if mono != noColor {
			t.Fatalf("mono and no-color differ: %#v != %#v", mono, noColor)
		}
	}
}

func TestResolveConfiguredContextPrecedenceAndErrors(t *testing.T) {
	ctx, err := ResolveConfiguredContext("", "amber", "", "", "ansi8", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.ColorProfile != Ansi8 || ctx.GlyphProfile != Ascii {
		t.Fatalf("config context = %#v", ctx)
	}
	ctx, err = ResolveConfiguredContext("light", "amber", "ansi16", "1", "ansi8", "unicode", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.ColorProfile != Ansi16 || ctx.GlyphProfile != Unicode || ctx.Palette != resolvePalette(Light, Ansi16) {
		t.Fatalf("environment did not win: %#v", ctx)
	}
	ctx, err = ResolveConfiguredContext("", "amber", "", "1", "ansi8", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.ColorProfile != NoColor {
		t.Fatalf("NO_COLOR did not beat config color: %s", ctx.ColorProfile)
	}
	for _, input := range []struct {
		theme, color, glyphs string
	}{
		{theme: "violet"},
		{color: "chartreuse"},
		{glyphs: "wingdings"},
	} {
		if _, err := ResolveConfiguredContext(input.theme, "", input.color, "", "", input.glyphs, ""); err == nil {
			t.Fatalf("invalid startup input %#v did not return an error", input)
		}
	}
}

func TestPinColorProfileMatchesResolvedContext(t *testing.T) {
	previous := lipgloss.DefaultRenderer().ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
	for _, tt := range []struct {
		profile ColorProfile
		want    termenv.Profile
	}{
		{TrueColor, termenv.TrueColor},
		{Ansi256, termenv.ANSI256},
		{Ansi16, termenv.ANSI},
		{Ansi8, termenv.ANSI},
		{Mono, termenv.Ascii},
		{NoColor, termenv.Ascii},
	} {
		PinColorProfile(Context{ColorProfile: tt.profile})
		if got := lipgloss.DefaultRenderer().ColorProfile(); got != tt.want {
			t.Fatalf("PinColorProfile(%s) = %v, want %v", tt.profile, got, tt.want)
		}
	}
}

func TestResolveContextPreservesThemeGutterRamp(t *testing.T) {
	for _, name := range []string{"dark", "light", "amber"} {
		ctx, err := ResolveContext(name, "", "ansi16", "", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, color := range ctx.GutterColors() {
			if color == "" {
				t.Fatalf("%s gutter color disappeared under ansi16", name)
			}
		}
	}
}
