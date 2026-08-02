package tui

import "github.com/charmbracelet/lipgloss"

// Heat color gradient: dim gray -> blue -> cyan -> yellow -> red
var heatColors = []lipgloss.Color{
	lipgloss.Color("240"), // 0-19:  dim gray
	lipgloss.Color("33"),  // 20-39: blue
	lipgloss.Color("37"),  // 40-59: cyan
	lipgloss.Color("220"), // 60-79: yellow
	lipgloss.Color("196"), // 80-100: red
}

// gutterColors tint the duplicate-group gutter. Deliberately distinct from
// heatColors so a group line is never mistaken for part of the heat bar, and
// cycled by group so two groups that end up adjacent stay separable.
var gutterColors = []lipgloss.Color{
	lipgloss.Color("170"), // orchid
	lipgloss.Color("214"), // orange
	lipgloss.Color("79"),  // aquamarine
	lipgloss.Color("205"), // pink
	lipgloss.Color("112"), // green
	lipgloss.Color("111"), // periwinkle
}

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
			Foreground(lipgloss.Color("205"))

	subtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))

	warnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).
			Bold(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("255")).
			Background(lipgloss.Color("236"))

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("252")).
			Padding(0, 1)

	cellStyle = lipgloss.NewStyle().
			Padding(0, 1)
)
