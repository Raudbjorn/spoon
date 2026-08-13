package ui

import "github.com/svnbjrn/spoon/internal/tui/theme"

// TitledAlert derives from molecules.crepus:164-175. The atom owns tone and
// profile handling; this molecule adds the validation or operation title.
func TitledAlert(ctx theme.Context, tone AlertTone, title, message string, width int) string {
	if title != "" {
		message = title + ": " + message
	}
	return Alert(ctx, tone, message, width)
}
