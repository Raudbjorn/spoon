package ui

import "github.com/svnbjrn/spoon/internal/tui/theme"

// Card is a Spoon-owned detail/settings grouping composition. It is absent
// from molecules.crepus; its contract comes from phase-4b-molecules.md:30-31,60-64.
func Card(ctx theme.Context, title string, width int, parts []BoxPart) string {
	if title == "" {
		return Box(ctx, width, parts)
	}
	content := make([]BoxPart, 0, len(parts)+2)
	content = append(content, BoxPart{Text: Heading(ctx, 2, title, width-4)}, BoxPart{Divider: true})
	content = append(content, parts...)
	return Box(ctx, width, content)
}
