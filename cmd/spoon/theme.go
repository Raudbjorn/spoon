package main

import (
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func resolveTUIContext(cfg *config.Config) (theme.Context, error) {
	return resolveTUIContextWithNoColor(cfg, false)
}

func resolveTUIContextWithNoColor(cfg *config.Config, noColor bool) (theme.Context, error) {
	if cfg == nil {
		return theme.ResolveStartupContextWithNoColor("", "", "", noColor)
	}
	return theme.ResolveStartupContextWithNoColor(cfg.UI.Theme, cfg.UI.Color, cfg.UI.Glyphs, noColor)
}
