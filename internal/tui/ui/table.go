package ui

import "github.com/svnbjrn/spoon/internal/tui/theme"

// TableHeader derives from molecules.crepus:187-198. The fork renderer keeps
// column calculation; this molecule owns only semantic table chrome.
func TableHeader(ctx theme.Context, header string, width int) string {
	return role(ctx, ctx.Palette.TextStrong).Bold(true).Render(truncate(header, width))
}

// TableRow derives from molecules.crepus:187-198. Selection remains visible
// without color through the caller's glyph marker and this bold treatment.
func TableRow(ctx theme.Context, row string, selected bool, width int) string {
	if !selected {
		return Text(ctx, TextDefault, row, width)
	}
	return role(ctx, ctx.Palette.TextStrong).Background(background(ctx, ctx.Palette.Surface3)).Bold(true).Render(truncate(row, width))
}
