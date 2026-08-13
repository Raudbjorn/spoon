package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// StatCard derives from molecules.crepus:51-70. Detail owns the surrounding
// card and width, while this composition preserves the value/label hierarchy.
func StatCard(ctx theme.Context, label, value string, accent bool, width int) string {
	variant := TextStrong
	if accent {
		variant = TextDefault
	}
	if width <= 0 {
		return Text(ctx, variant, value, width) + " " + Text(ctx, TextMuted, label, width)
	}
	renderedValue := Text(ctx, variant, value, width)
	remaining := width - lipgloss.Width(renderedValue) - 1
	if remaining <= 0 {
		return renderedValue
	}
	return renderedValue + " " + Text(ctx, TextMuted, label, remaining)
}
