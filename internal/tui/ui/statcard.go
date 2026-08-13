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
	renderedValue := Text(ctx, variant, value, width)
	available := width - 1
	if available > 0 {
		available -= lipgloss.Width(value)
	}
	return renderedValue + " " + Text(ctx, TextMuted, label, available)
}
