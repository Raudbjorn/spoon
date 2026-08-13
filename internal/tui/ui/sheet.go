package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// Sheet is a Spoon-owned contextual-help composition absent from
// molecules.crepus. Its contract comes from phase-4b-molecules.md:32-33,65-71.
func Sheet(ctx theme.Context, title, subtitle string, width int) string {
	subtitleWidth := width - lipgloss.Width(title) - 1
	return Heading(ctx, 1, title, width) + " " + Text(ctx, TextMuted, subtitle, subtitleWidth)
}
