package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// Tabs composes the phase-4a text and keyboard grammar for an in-TUI section
// selector. It is used by Settings rather than duplicating a screen-local style.
func Tabs(ctx theme.Context, labels []string, selected, width int) string {
	parts := make([]string, 0, len(labels))
	for i, label := range labels {
		variant := TextMuted
		if i == selected {
			variant = TextStrong
			label = "[" + label + "]"
		}
		parts = append(parts, Text(ctx, variant, label, width))
	}
	return truncate(strings.Join(parts, " "+ctx.Glyph(theme.ArrowRight)+" "), width)
}

// TabsWidth is retained for tests and consumers that need terminal-cell width.
func TabsWidth(ctx theme.Context, labels []string, selected, width int) int {
	return lipgloss.Width(Tabs(ctx, labels, selected, width))
}
