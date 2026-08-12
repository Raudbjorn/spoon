package theme

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// ColorProfile defines the terminal color capability in upstream order.
type ColorProfile int

const (
	TrueColor ColorProfile = iota
	Ansi256
	Ansi16
	Ansi8
	Mono
	NoColor
)

func (p ColorProfile) String() string {
	switch p {
	case TrueColor:
		return "truecolor"
	case Ansi256:
		return "ansi256"
	case Ansi16:
		return "ansi16"
	case Ansi8:
		return "ansi8"
	case Mono:
		return "mono"
	case NoColor:
		return "no-color"
	default:
		return "unknown"
	}
}

// ParseColorProfile parses SPOON_TUI_COLOR. Empty means unset and defaults to
// truecolor; callers that need NO_COLOR precedence use ResolveColorProfile.
func ParseColorProfile(value string) (ColorProfile, error) {
	switch value {
	case "", "truecolor":
		return TrueColor, nil
	case "ansi256":
		return Ansi256, nil
	case "ansi16":
		return Ansi16, nil
	case "ansi8":
		return Ansi8, nil
	case "mono":
		return Mono, nil
	case "no-color":
		return NoColor, nil
	default:
		return TrueColor, fmt.Errorf("invalid SPOON_TUI_COLOR value %q (want truecolor, ansi256, ansi16, ansi8, mono, or no-color)", value)
	}
}

// ResolveColorProfile applies Spoon's explicit-profile-over-NO_COLOR
// precedence. An empty explicit value is intentionally unset.
func ResolveColorProfile(color, noColor string) (ColorProfile, error) {
	if color != "" {
		return ParseColorProfile(color)
	}
	if noColor != "" {
		return NoColor, nil
	}
	return TrueColor, nil
}

// ResolveContext resolves every startup rendering choice before any model is
// constructed. It retains the env-only surface for tests and simple callers.
func ResolveContext(envTheme, configTheme, color, noColor, glyphs string) (Context, error) {
	return ResolveConfiguredContext(envTheme, configTheme, color, noColor, "", glyphs, "")
}

// ResolveConfiguredContext applies environment-over-config precedence without
// reading process state. NO_COLOR wins over a config color, but not an explicit
// SPOON_TUI_COLOR value.
func ResolveConfiguredContext(envTheme, configTheme, envColor, noColor, configColor, envGlyphs, configGlyphs string) (Context, error) {
	name := envTheme
	if name == "" {
		name = configTheme
	}
	if name == "" {
		name = "dark"
	}
	palette, err := PaletteByName(name)
	if err != nil {
		return Context{}, err
	}
	profile, err := ResolveColorProfile(envColor, noColor)
	if err != nil {
		return Context{}, err
	}
	if envColor == "" && noColor == "" {
		profile, err = ParseColorProfile(configColor)
		if err != nil {
			return Context{}, err
		}
	}
	glyphValue := envGlyphs
	if glyphValue == "" {
		glyphValue = configGlyphs
	}
	glyphProfile, err := ParseGlyphProfile(glyphValue)
	if err != nil {
		return Context{}, err
	}
	return Context{
		Palette:      resolvePalette(palette, profile),
		ColorProfile: profile,
		GlyphProfile: glyphProfile,
		gutter:       resolveGutterColors(GutterColors(palette), profile),
		initialized:  true,
	}, nil
}

// ResolveStartupContext reads environment exactly once at program startup.
func ResolveStartupContext(configTheme, configColor, configGlyphs string) (Context, error) {
	return ResolveConfiguredContext(
		os.Getenv("SPOON_TUI_THEME"),
		configTheme,
		os.Getenv("SPOON_TUI_COLOR"),
		os.Getenv("NO_COLOR"),
		configColor,
		os.Getenv("SPOON_TUI_GLYPHS"),
		configGlyphs,
	)
}

// ResolveStartupContextWithNoColor resolves startup choices while honoring the
// main TUI's --no-color flag as an explicit command-line override.
func ResolveStartupContextWithNoColor(configTheme, configColor, configGlyphs string, noColor bool) (Context, error) {
	if noColor {
		return ResolveConfiguredContext(
			os.Getenv("SPOON_TUI_THEME"), configTheme, "no-color", "",
			configColor, os.Getenv("SPOON_TUI_GLYPHS"), configGlyphs,
		)
	}
	return ResolveStartupContext(configTheme, configColor, configGlyphs)
}

// DefaultContext is deterministic for bare models in tests and for internal
// callers that have no command startup path.
func DefaultContext() Context {
	ctx, err := ResolveContext("", "", "", "", "")
	if err != nil {
		panic(err)
	}
	return ctx
}

// PinColorProfile prevents Lip Gloss from independently downgrading Spoon's
// already-quantized palette. It is called once by command startup.
func PinColorProfile(ctx Context) {
	switch ctx.ColorProfile {
	case Ansi256:
		lipgloss.SetColorProfile(termenv.ANSI256)
	case Ansi16, Ansi8:
		lipgloss.SetColorProfile(termenv.ANSI)
	case Mono, NoColor:
		lipgloss.SetColorProfile(termenv.Ascii)
	default:
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
}

var ansi16RGB = [16][3]uint8{
	{0, 0, 0}, {128, 0, 0}, {0, 128, 0}, {128, 128, 0},
	{0, 0, 128}, {128, 0, 128}, {0, 128, 128}, {192, 192, 192},
	{128, 128, 128}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
	{0, 0, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
}

var xtermLevels = [6]uint8{0, 95, 135, 175, 215, 255}

func resolvePalette(p Palette, profile ColorProfile) Palette {
	return Palette{
		Bg: quantizeColor(p.Bg, profile), Surface1: quantizeColor(p.Surface1, profile), Surface2: quantizeColor(p.Surface2, profile), Surface3: quantizeColor(p.Surface3, profile),
		Border: quantizeColor(p.Border, profile), Text: quantizeColor(p.Text, profile), TextStrong: quantizeColor(p.TextStrong, profile), TextMuted: quantizeColor(p.TextMuted, profile), TextFaint: quantizeColor(p.TextFaint, profile),
		Accent: quantizeColor(p.Accent, profile), Accent2: quantizeColor(p.Accent2, profile), AccentRust: quantizeColor(p.AccentRust, profile), MixTarget: quantizeColor(p.MixTarget, profile),
		Success: quantizeColor(p.Success, profile), Error: quantizeColor(p.Error, profile), Warning: quantizeColor(p.Warning, profile), Info: quantizeColor(p.Info, profile),
		SynKeyword: quantizeColor(p.SynKeyword, profile), SynString: quantizeColor(p.SynString, profile), SynVar: quantizeColor(p.SynVar, profile), SynFunc: quantizeColor(p.SynFunc, profile), SynComment: quantizeColor(p.SynComment, profile), SynNumber: quantizeColor(p.SynNumber, profile),
	}
}

func paletteColors(p Palette) []lipgloss.Color {
	return []lipgloss.Color{p.Bg, p.Surface1, p.Surface2, p.Surface3, p.Border, p.Text, p.TextStrong, p.TextMuted, p.TextFaint, p.Accent, p.Accent2, p.AccentRust, p.MixTarget, p.Success, p.Error, p.Warning, p.Info, p.SynKeyword, p.SynString, p.SynVar, p.SynFunc, p.SynComment, p.SynNumber}
}

func resolveGutterColors(colors [6]lipgloss.Color, profile ColorProfile) [6]lipgloss.Color {
	for i := range colors {
		colors[i] = quantizeColor(colors[i], profile)
	}
	return colors
}

func quantizeColor(color lipgloss.Color, profile ColorProfile) lipgloss.Color {
	if profile == TrueColor {
		return color
	}
	if profile == Mono || profile == NoColor {
		return ""
	}
	value := string(color)
	if value == "" {
		return ""
	}
	if index, err := strconv.Atoi(value); err == nil {
		switch profile {
		case Ansi256:
			return color
		case Ansi16:
			if index < 16 {
				return color
			}
		case Ansi8:
			if index < 8 {
				return color
			}
		}
	}
	rgb, ok := colorRGB(value)
	if !ok {
		return ""
	}
	if profile == Ansi256 {
		return lipgloss.Color(strconv.Itoa(int(nearestXtermIndex(rgb))))
	}
	count := 16
	if profile == Ansi8 {
		count = 8
	}
	return lipgloss.Color(strconv.Itoa(nearestANSIIndex(rgb, count)))
}

func colorRGB(value string) ([3]uint8, bool) {
	if index, err := strconv.Atoi(value); err == nil && index >= 0 && index <= 255 {
		return indexedColorRGB(uint8(index)), true
	}
	if len(value) != 7 || !strings.HasPrefix(value, "#") {
		return [3]uint8{}, false
	}
	var rgb [3]uint8
	for i := range rgb {
		parsed, err := strconv.ParseUint(value[1+i*2:3+i*2], 16, 8)
		if err != nil {
			return [3]uint8{}, false
		}
		rgb[i] = uint8(parsed)
	}
	return rgb, true
}

func nearestXtermIndex(rgb [3]uint8) uint8 {
	bestIndex := uint8(16)
	bestDistance := ^uint32(0)
	for index := uint8(16); ; index++ {
		distance := colorDistance(rgb, indexedColorRGB(index))
		if distance < bestDistance {
			bestDistance, bestIndex = distance, index
		}
		if index == 255 {
			return bestIndex
		}
	}
}

func nearestANSIIndex(rgb [3]uint8, count int) int {
	bestIndex := 0
	bestDistance := ^uint32(0)
	for index := 0; index < count; index++ {
		distance := colorDistance(rgb, ansi16RGB[index])
		if distance < bestDistance {
			bestDistance, bestIndex = distance, index
		}
	}
	return bestIndex
}

func indexedColorRGB(index uint8) [3]uint8 {
	switch {
	case index <= 15:
		return ansi16RGB[index]
	case index <= 231:
		offset := index - 16
		return [3]uint8{xtermLevels[offset/36], xtermLevels[(offset%36)/6], xtermLevels[offset%6]}
	default:
		gray := uint8(8 + 10*(index-232))
		return [3]uint8{gray, gray, gray}
	}
}

func colorDistance(left, right [3]uint8) uint32 {
	red := int32(left[0]) - int32(right[0])
	green := int32(left[1]) - int32(right[1])
	blue := int32(left[2]) - int32(right[2])
	return uint32(red*red + green*green + blue*blue)
}
