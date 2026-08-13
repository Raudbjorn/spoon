package main

import (
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// resolveTUIContextWithNoColor consumes startup-resolved values so rendering
// cannot independently reread environment or choose a different config layer.
func resolveTUIContextWithNoColor(effective config.EffectiveConfig, noColor bool) (theme.Context, error) {
	color := effective.Appearance.Color.Value
	if noColor {
		color = "no-color"
	}
	return theme.ResolveConfiguredContext(
		"", effective.Appearance.Theme.Value, color, "", "", effective.Appearance.Glyphs.Value, "",
	)
}
