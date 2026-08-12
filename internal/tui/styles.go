package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// gutterColors and duplicateGutter preserve deterministic default helpers for
// existing tests; production rendering receives its colors from Model.styles.
var gutterColors = theme.GutterColors(theme.Dark)

type styleSet struct {
	title, subtitle, warn, error, statusBar, help, selected, header, cell lipgloss.Style
	heat                                                                  []lipgloss.Color
	gutter                                                                [6]lipgloss.Color
}

func (m Model) themeContext() theme.Context {
	if !m.theme.IsResolved() {
		return theme.DefaultContext()
	}
	return m.theme
}

func stylesFor(ctx theme.Context) styleSet {
	palette := ctx.Palette
	return styleSet{
		title:     lipgloss.NewStyle().Bold(true).Foreground(palette.Accent),
		subtitle:  lipgloss.NewStyle().Foreground(palette.TextMuted),
		warn:      lipgloss.NewStyle().Foreground(palette.Warning).Bold(true),
		error:     lipgloss.NewStyle().Foreground(palette.Error).Bold(true),
		statusBar: lipgloss.NewStyle().Foreground(palette.Text).Background(palette.Surface2).Padding(0, 1),
		help:      lipgloss.NewStyle().Foreground(palette.TextFaint),
		selected:  lipgloss.NewStyle().Bold(true).Foreground(palette.TextStrong).Background(palette.Surface3),
		header:    lipgloss.NewStyle().Bold(true).Foreground(palette.TextStrong).Padding(0, 1),
		cell:      lipgloss.NewStyle().Foreground(palette.Text).Padding(0, 1),
		heat:      []lipgloss.Color{palette.TextFaint, palette.Info, palette.Accent, palette.Warning, palette.Error},
		gutter:    ctx.GutterColors(),
	}
}

func (m Model) styles() styleSet { return stylesFor(m.themeContext()) }

func heatColor(colors []lipgloss.Color, score float64) lipgloss.Color {
	idx := int(score / 20)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(colors) {
		idx = len(colors) - 1
	}
	return colors[idx]
}

// HeatColor retains the deterministic default-context helper used by legacy
// tests. Renderers use Model.heatColor so selected startup context wins.
func HeatColor(score float64) lipgloss.Color {
	return heatColor(stylesFor(theme.DefaultContext()).heat, score)
}

func (m Model) heatColor(score float64) lipgloss.Color { return heatColor(m.styles().heat, score) }

func heatBar(ctx theme.Context, score float64) string {
	filled := int(score / 25)
	if filled > 4 {
		filled = 4
	}
	out := make([]string, 4)
	for i := range out {
		if i < filled {
			out[i] = ctx.Glyph(theme.HeatFull)
		} else {
			out[i] = ctx.Glyph(theme.HeatEmpty)
		}
	}
	return out[0] + out[1] + out[2] + out[3]
}

// HeatBar retains a Unicode default for legacy callers. Model renderers use
// Model.heatBar and therefore follow their immutable glyph context.
func HeatBar(score float64) string { return heatBar(theme.DefaultContext(), score) }

func (m Model) heatBar(score float64) string { return heatBar(m.themeContext(), score) }

// RenderHeatBar retains a deterministic default-context helper for callers
// outside a Model. Main views use Model.renderHeatBar.
func RenderHeatBar(score float64) string {
	return lipgloss.NewStyle().Foreground(HeatColor(score)).Render(HeatBar(score))
}

func (m Model) renderHeatBar(score float64) string {
	return lipgloss.NewStyle().Foreground(m.heatColor(score)).Render(m.heatBar(score))
}

func (m Model) gutterStyleFor(ordinal int) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(m.gutterColour(ordinal))
}

func (m Model) gutterColour(ordinal int) lipgloss.Color {
	if ordinal < 0 {
		ordinal = 0
	}
	colors := m.styles().gutter
	return colors[ordinal%len(colors)]
}

// gutterColour retains the dark default for existing color-choice tests.
func gutterColour(ordinal int) lipgloss.Color { return Model{}.gutterColour(ordinal) }

func duplicateGutter(sf ScoredFork, ordinals map[string]int) string {
	return Model{}.duplicateGutter(sf, ordinals)
}
