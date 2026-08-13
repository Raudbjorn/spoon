package tui

import (
	"strings"

	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"github.com/svnbjrn/spoon/internal/tui/ui"
)

// helpFooterLines reserves the actual wrapped contextual legend below the
// scrolled help body.
func (m Model) helpFooterLines() int {
	return 2 + strings.Count(m.helpLegend(), "\n") + 1
}

func (m Model) viewHelp() string {
	ctx := m.themeContext()
	body := scrollLines(helpBody(ctx), m.helpOffset, m.helpViewHeight())
	return ui.Sheet(ctx, "  spoon", ctx.Glyph(theme.EmDash)+" help", ui.ContentWidth(m.width)) + "\n" +
		body + "\n  " + m.helpLegend()
}

func (m Model) helpViewHeight() int {
	if m.height <= 0 {
		return 0
	}
	return m.height - m.helpFooterLines()
}

func (m Model) helpLegend() string {
	return ui.KeyLegend(m.themeContext(), ui.ContentWidth(m.width)-2, keymap.MainHelp)
}

func helpBody(ctx theme.Context) string {
	var b strings.Builder
	b.WriteString("\n  Keybindings\n  ")
	b.WriteString(strings.Repeat(ctx.Glyph(theme.BoxHorizontal), 11))
	b.WriteString("\n\n  Transition notice (one release)\n")
	b.WriteString("  t: enrichment ceiling -> dark/light theme\n")
	b.WriteString("  c: open compare -> enrichment ceiling\n")
	b.WriteString("  d: unbound -> open compare\n")
	b.WriteString("\n")
	for _, section := range []struct {
		title  string
		scopes []keymap.Scope
	}{
		{"Global", []keymap.Scope{keymap.Global}},
		{"Repository input", []keymap.Scope{keymap.MainInput}},
		{"Fork table", []keymap.Scope{keymap.MainTable}},
		{"Fork details", []keymap.Scope{keymap.MainDetail}},
		{"Export path", []keymap.Scope{keymap.MainExport}},
		{"Topic picker", []keymap.Scope{keymap.MainTopics}},
		{"Filter prompt", []keymap.Scope{keymap.MainFilter}},
		{"Rank prompt", []keymap.Scope{keymap.MainRank}},
		{"Help", []keymap.Scope{keymap.MainHelp}},
	} {
		b.WriteString("\n  " + section.title + "\n")
		for _, binding := range keymap.ForScopes(section.scopes...) {
			b.WriteString("  " + keymap.KeyLabel(binding.Keys))
			padding := 16 - len([]rune(keymap.KeyLabel(binding.Keys)))
			if padding < 1 {
				padding = 1
			}
			b.WriteString(strings.Repeat(" ", padding))
			b.WriteString(binding.Label)
			b.WriteString("\n")
		}
	}

	return b.String()
}
