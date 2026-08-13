//go:generate go run gen.go

// Package theme owns Spoon's terminal palette contracts.
package theme

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// Context is the selected immutable terminal rendering context.
type Context struct {
	Palette      Palette
	ColorProfile ColorProfile
	GlyphProfile GlyphProfile

	gutter      [6]lipgloss.Color
	initialized bool
}

// PaletteByName returns one of Spoon's built-in resolved palettes.
func PaletteByName(name string) (Palette, error) {
	switch name {
	case "dark":
		return Dark, nil
	case "light":
		return Light, nil
	case "amber":
		return Amber, nil
	default:
		return Palette{}, fmt.Errorf("unknown TUI palette %q (want dark, light, or amber)", name)
	}
}

// ToggleDarkLight switches the live palette while retaining every terminal
// capability choice resolved at startup. Amber is a configured palette rather
// than a toggle endpoint, so it moves to dark on its first toggle.
func ToggleDarkLight(ctx Context) Context {
	target := Dark
	if ctx.Palette == resolvePalette(Dark, ctx.ColorProfile) {
		target = Light
	}
	return Context{
		Palette:      resolvePalette(target, ctx.ColorProfile),
		ColorProfile: ctx.ColorProfile,
		GlyphProfile: ctx.GlyphProfile,
		gutter:       resolveGutterColors(GutterColors(target), ctx.ColorProfile),
		initialized:  true,
	}
}

// The gutter has no upstream semantic role: it identifies duplicate groups,
// so each theme defines six distinct categorical hues separate from heat.
var (
	DarkGutterColors = [6]lipgloss.Color{
		"#c586c0", "#d19a66", "#56b6c2",
		"#c678dd", "#98c379", "#61afef",
	}
	LightGutterColors = [6]lipgloss.Color{
		"#8b3fa0", "#b35b00", "#007f8b",
		"#b23a6f", "#3f7c33", "#2e63b6",
	}
	AmberGutterColors = [6]lipgloss.Color{
		"#b36bff", "#ff8f3d", "#25b5a6",
		"#f06292", "#8bc34a", "#4da3ff",
	}
)

// GutterColors returns the theme-local categorical duplicate-group ramp. Its
// colors cycle by ordinal so adjacent groups remain distinguishable.
func GutterColors(palette Palette) [6]lipgloss.Color {
	switch palette {
	case Light:
		return LightGutterColors
	case Amber:
		return AmberGutterColors
	default:
		return DarkGutterColors
	}
}

func (c Context) GutterColors() [6]lipgloss.Color {
	if c.initialized {
		return c.gutter
	}
	return GutterColors(Dark)
}

// IsResolved reports whether the context was resolved at startup. It must not
// be inferred from Palette: mono and no-color intentionally clear every role.
func (c Context) IsResolved() bool { return c.initialized }
