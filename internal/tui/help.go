package tui

import (
	"strings"

	"github.com/svnbjrn/spoon/internal/tui/theme"
	"github.com/svnbjrn/spoon/internal/tui/ui"
)

// helpFooterLines is how many lines viewHelp reserves for the title above and
// the dismiss hint below the scrolled body.
const helpFooterLines = 3

func (m Model) viewHelp() string {
	ctx, styles := m.themeContext(), m.styles()
	body := scrollLines(helpBody(ctx), m.helpOffset, m.helpViewHeight())
	return ui.Heading(ctx, 1, "  spoon", ui.ContentWidth(m.width)) + styles.subtitle.Render(" "+ctx.Glyph(theme.EmDash)+" help") + "\n" +
		body + "\n  " + styles.help.Render("PgUp/PgDn scroll "+ctx.Glyph(theme.Separator)+" Press ? or Esc to go back")
}

func (m Model) helpViewHeight() int {
	if m.height <= 0 {
		return 0
	}
	return m.height - helpFooterLines
}

func helpBody(ctx theme.Context) string {
	return "\n  Keybindings\n  " + strings.Repeat(ctx.Glyph(theme.BoxHorizontal), 11) + "\n" +
		"  " + ctx.Glyph(theme.ArrowUp) + "/" + ctx.Glyph(theme.ArrowDown) + `, j/k      Navigate table
  PgUp/PgDn     Page up/down (table, detail, help)
  Home/G        Go to top/bottom
  g             Toggle cluster grouping (when clusters are available)
  Enter         View fork details
  n             Search new repository
  /             Filter forks by owner/name
  R             Rank filtered forks by intent (relevance, not a filter)
  Esc           Clear the active filter
  s             Cycle sort column (heat/stars/ahead/branches/forks/pushed)
  S             Reverse sort order
  o             Open selected fork in browser
  c             Open compare view in browser
  y             Yank clone command to clipboard
  t             Cycle enrichment ceiling - T3 full, T2 no lone-wolf, T1 no compares
  r             Refresh (bypass cache, restart)
  Space         Mark/unmark fork for export
  e             Export marked forks to JSON
  E             Export all forks to JSON
  ?             Toggle this help
  q, Ctrl+C     Quit
`
}
