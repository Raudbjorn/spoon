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

// TitledWrappedAlert is the non-truncating counterpart for actionable local
// diagnostics; long output wraps instead of silently dropping its cause.
func TitledWrappedAlert(ctx theme.Context, tone AlertTone, title, message string, width int) string {
	if title != "" {
		message = title + ": " + message
	}
	return WrappedAlert(ctx, tone, message, width)
}
