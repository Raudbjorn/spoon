package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// Heat color gradient: text-faint -> info -> accent -> warning -> error.
var heatColors = []lipgloss.Color{
	theme.Dark.TextFaint, // 0-19: dim
	theme.Dark.Info,      // 20-39: informational
	theme.Dark.Accent,    // 40-59: accent
	theme.Dark.Warning,   // 60-79: warning
	theme.Dark.Error,     // 80-100: error
}

// gutterColors tint the duplicate-group gutter. Deliberately distinct from
// heatColors so a group line is never mistaken for part of the heat bar, and
// cycled by group so two groups that end up adjacent stay separable.
var gutterColors = theme.GutterColors(theme.Dark)

// gutterStyleFor returns the colour for the ordinal-th duplicate group in the
// rendered list.
//
// Colours are assigned by order of appearance rather than by hashing the group
// key: hashing cannot guarantee that two groups landing next to each other get
// different colours, and when they collide the two lines read as one group —
// exactly the confusion the gutter exists to prevent. Cycling by position makes
// adjacent groups always differ.
func gutterStyleFor(ordinal int) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(gutterColour(ordinal))
}

// gutterColour is the colour choice alone, separated from rendering so it stays
// assertable: lipgloss strips colour with no TTY attached, which would make any
// test over rendered output pass vacuously.
func gutterColour(ordinal int) lipgloss.Color {
	if ordinal < 0 {
		ordinal = 0
	}
	return gutterColors[ordinal%len(gutterColors)]
}

// HeatBarChars are the block characters used for the heat bar.
var heatBarChars = [4]rune{'░', '▒', '▓', '█'}

// HeatColor returns the color for a given heat score (0-100).
func HeatColor(score float64) lipgloss.Color {
	idx := int(score / 20)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(heatColors) {
		idx = len(heatColors) - 1
	}
	return heatColors[idx]
}

// HeatBar renders a 4-character heat bar for the given score (0-100).
func HeatBar(score float64) string {
	filled := int(score / 25)
	if filled > 4 {
		filled = 4
	}
	bar := make([]rune, 4)
	for i := range 4 {
		if i < filled {
			bar[i] = heatBarChars[3] // █
		} else {
			bar[i] = heatBarChars[0] // ░
		}
	}
	return string(bar)
}

// RenderHeatBar renders a colored heat bar.
func RenderHeatBar(score float64) string {
	bar := HeatBar(score)
	color := HeatColor(score)
	return lipgloss.NewStyle().Foreground(color).Render(bar)
}

// Common styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(theme.Dark.Accent)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(theme.Dark.TextMuted)

	warnStyle = lipgloss.NewStyle().
			Foreground(theme.Dark.Warning).
			Bold(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(theme.Dark.Error).
			Bold(true)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(theme.Dark.Text).
			Background(theme.Dark.Surface2).
			Padding(0, 1)

	helpStyle = lipgloss.NewStyle().
			Foreground(theme.Dark.TextFaint)

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(theme.Dark.TextStrong).
			Background(theme.Dark.Surface3)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(theme.Dark.TextStrong).
			Padding(0, 1)

	cellStyle = lipgloss.NewStyle().
			Foreground(theme.Dark.Text).
			Padding(0, 1)
)
