package tui

import (
	"strings"

	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"github.com/svnbjrn/spoon/internal/tui/ui"
)

// helpFooterLines is how many lines viewHelp reserves for the title above and
// the dismiss hint below the scrolled body.
const helpFooterLines = 3

func (m Model) viewHelp() string {
	ctx, styles := m.themeContext(), m.styles()
	body := scrollLines(helpBody(ctx), m.helpOffset, m.helpViewHeight())
	return ui.Sheet(ctx, "  spoon", ctx.Glyph(theme.EmDash)+" help", ui.ContentWidth(m.width)) + "\n" +
		body + "\n  " + styles.help.Render("PgUp/PgDn scroll "+ctx.Glyph(theme.Separator)+" Press ? or Esc to go back")
}

func (m Model) helpViewHeight() int {
	if m.height <= 0 {
		return 0
	}
	return m.height - helpFooterLines
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
		{"Fork table", []keymap.Scope{keymap.MainTable}},
		{"Fork details", []keymap.Scope{keymap.MainDetail}},
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
