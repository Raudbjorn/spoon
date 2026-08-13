package ui

import "github.com/svnbjrn/spoon/internal/tui/theme"

// NavBar is a Spoon-owned status-bar composition absent from molecules.crepus.
// Its contract comes from phase-4b-molecules.md:37,60-64.
func NavBar(ctx theme.Context, content string, width int) string {
	if width <= 0 {
		return ""
	}
	return role(ctx, ctx.Palette.Text).Background(background(ctx, ctx.Palette.Surface2)).Width(width).Padding(0, 1).Render(Text(ctx, TextDefault, content, width-2))
}
