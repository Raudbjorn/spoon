package main

import (
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func resolveTUIContext(cfg *config.Config) (theme.Context, error) {
	if cfg == nil {
		return theme.ResolveStartupContext("", "", "")
	}
	return theme.ResolveStartupContext(cfg.UI.Theme, cfg.UI.Color, cfg.UI.Glyphs)
}
