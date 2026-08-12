package theme

import (
	"strconv"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
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

func TestResolveContextPreservesResolvedGutterRamp(t *testing.T) {
	for _, profile := range []string{"ansi16", "mono", "no-color"} {
		for _, name := range []string{"dark", "light", "amber"} {
			ctx, err := ResolveContext(name, "", profile, "", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, color := range ctx.GutterColors() {
				if profile == "ansi16" && color == "" {
					t.Fatalf("%s gutter color disappeared under ansi16", name)
				}
				if (profile == "mono" || profile == "no-color") && color != "" {
					t.Fatalf("%s gutter color escaped %s: %q", name, profile, color)
				}
			}
		}
	}
}

func TestResolvePaletteExactReferenceMatrix(t *testing.T) {
	for paletteName, palette := range map[string]Palette{"dark": Dark, "light": Light, "amber": Amber} {
		for _, profile := range []ColorProfile{TrueColor, Ansi256, Ansi16, Ansi8, Mono, NoColor} {
			got := paletteColors(resolvePalette(palette, profile))
			want := paletteColors(palette)
			for i := range got {
				expected := referenceQuantize(string(want[i]), profile)
				if string(got[i]) != expected {
					t.Fatalf("%s/%s role %d = %q, want independent reference %q", paletteName, profile, i, got[i], expected)
				}
			}
		}
	}
}

func TestQuantizationBoundaryAndPassThroughIndices(t *testing.T) {
	for _, index := range []string{"0", "7", "8", "15", "16", "231", "232", "255"} {
		for _, profile := range []ColorProfile{Ansi256, Ansi16, Ansi8} {
			got := string(quantizeColor(lipgloss.Color(index), profile))
			want := referenceQuantize(index, profile)
			if got != want {
				t.Fatalf("%s index %s = %q, want %q", profile, index, got, want)
			}
		}
	}
}

func TestResolveStartupContextEnvironment(t *testing.T) {
	keys := []string{"SPOON_TUI_THEME", "SPOON_TUI_COLOR", "SPOON_TUI_GLYPHS", "NO_COLOR"}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	ctx, err := ResolveStartupContext("amber", "ansi8", "ascii")
	if err != nil || ctx.ColorProfile != Ansi8 || ctx.GlyphProfile != Ascii {
		t.Fatalf("config startup context = %#v, %v", ctx, err)
	}
	t.Setenv("SPOON_TUI_THEME", "light")
	t.Setenv("SPOON_TUI_COLOR", "ansi16")
	t.Setenv("SPOON_TUI_GLYPHS", "unicode")
	t.Setenv("NO_COLOR", "1")
	ctx, err = ResolveStartupContext("amber", "ansi8", "ascii")
	if err != nil || ctx.ColorProfile != Ansi16 || ctx.GlyphProfile != Unicode || ctx.Palette != resolvePalette(Light, Ansi16) {
		t.Fatalf("environment startup context = %#v, %v", ctx, err)
	}
	t.Setenv("SPOON_TUI_COLOR", "")
	if ctx, err := ResolveStartupContext("", "", ""); err != nil || ctx.ColorProfile != NoColor {
		t.Fatalf("NO_COLOR startup context = %#v, %v", ctx, err)
	}
	t.Setenv("SPOON_TUI_COLOR", "invalid")
	if _, err := ResolveStartupContext("", "", ""); err == nil {
		t.Fatal("invalid startup color did not propagate")
	}
}

func TestNoColorFlagOverridesEveryExplicitEnvironmentProfile(t *testing.T) {
	for _, profile := range []string{"truecolor", "ansi256", "ansi16", "ansi8", "mono", "no-color"} {
		t.Run(profile, func(t *testing.T) {
			t.Setenv("SPOON_TUI_COLOR", profile)
			ctx, err := ResolveStartupContextWithNoColor("", "", "", true)
			if err != nil || ctx.ColorProfile != NoColor {
				t.Fatalf("--no-color with %s = %#v, %v", profile, ctx, err)
			}
		})
	}
}

func referenceQuantize(value string, profile ColorProfile) string {
	if profile == TrueColor {
		return value
	}
	if profile == Mono || profile == NoColor || value == "" {
		return ""
	}
	if index, err := strconv.Atoi(value); err == nil {
		if profile == Ansi256 || (profile == Ansi16 && index < 16) || (profile == Ansi8 && index < 8) {
			return value
		}
	}
	rgb, ok := referenceRGB(value)
	if !ok {
		return ""
	}
	if profile == Ansi256 {
		best, bestDistance := 16, uint32(^uint32(0))
		for index := 16; index <= 255; index++ {
			if distance := referenceDistance(rgb, referenceIndexed(index)); distance < bestDistance {
				best, bestDistance = index, distance
			}
		}
		return strconv.Itoa(best)
	}
	count := 16
	if profile == Ansi8 {
		count = 8
	}
	best, bestDistance := 0, uint32(^uint32(0))
	for index := 0; index < count; index++ {
		if distance := referenceDistance(rgb, ansi16RGB[index]); distance < bestDistance {
			best, bestDistance = index, distance
		}
	}
	return strconv.Itoa(best)
}

func referenceRGB(value string) ([3]uint8, bool) {
	if index, err := strconv.Atoi(value); err == nil && index >= 0 && index <= 255 {
		return referenceIndexed(index), true
	}
	if len(value) != 7 || value[0] != '#' {
		return [3]uint8{}, false
	}
	var rgb [3]uint8
	for i := range rgb {
		n, err := strconv.ParseUint(value[1+i*2:3+i*2], 16, 8)
		if err != nil {
			return [3]uint8{}, false
		}
		rgb[i] = uint8(n)
	}
	return rgb, true
}

func referenceIndexed(index int) [3]uint8 {
	if index < 16 {
		return ansi16RGB[index]
	}
	if index <= 231 {
		offset := index - 16
		return [3]uint8{xtermLevels[offset/36], xtermLevels[(offset%36)/6], xtermLevels[offset%6]}
	}
	gray := uint8(8 + 10*(index-232))
	return [3]uint8{gray, gray, gray}
}

func referenceDistance(left, right [3]uint8) uint32 {
	r := int32(left[0]) - int32(right[0])
	g := int32(left[1]) - int32(right[1])
	b := int32(left[2]) - int32(right[2])
	return uint32(r*r + g*g + b*b)
}
