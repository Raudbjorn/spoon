// Package ui contains shared terminal UI rendering helpers.
package ui

import (
	"fmt"

	"github.com/svnbjrn/spoon/internal/tui/theme"
)

const (
	MinWidth        = 80
	MinHeight       = 24
	MaxContentWidth = 120
)

// TooSmall reports whether a terminal cannot render Spoon's full TUI views.
func TooSmall(width, height int) bool {
	return width < MinWidth || height < MinHeight
}

// FallbackMessage returns the Unicode viewport-floor diagnostic.
func FallbackMessage(width, height int) string {
	return fallbackMessage(theme.DefaultContext().Glyph(theme.EmDash), width, height)
}

// FallbackMessageFor returns the viewport-floor diagnostic with ctx's glyph
// profile. It is intentionally unstyled so the fallback remains legible under
// every color profile.
func FallbackMessageFor(ctx theme.Context, width, height int) string {
	return fallbackMessage(ctx.Glyph(theme.EmDash), width, height)
}

func fallbackMessage(dash string, width, height int) string {
	return fmt.Sprintf("Terminal too small %s requires 80x24, current %dx%d", dash, width, height)
}

// ContentWidth caps content that would otherwise stretch across the terminal.
func ContentWidth(width int) int {
	if width > MaxContentWidth {
		return MaxContentWidth
	}
	return width
}
