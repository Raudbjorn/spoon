package tui

// helpFooterLines is how many lines viewHelp reserves for the title above and
// the dismiss hint below the scrolled body.
const helpFooterLines = 3

// viewHelp renders the keybinding list scrolled to m.helpOffset. The title and
// the dismiss hint sit outside the scrolled window so the way out of the
// overlay is always on screen.
func (m Model) viewHelp() string {
	body := scrollLines(helpBody(), m.helpOffset, m.helpViewHeight())
	return titleStyle.Render("  spoon") + subtitleStyle.Render(" — help") + "\n" +
		body + "\n  " + helpStyle.Render("PgUp/PgDn scroll · Press ? or Esc to go back")
}

// helpViewHeight is how many body lines fit on screen; zero when no
// WindowSizeMsg has arrived, which makes scrollLines a pass-through.
func (m Model) helpViewHeight() int {
	if m.height <= 0 {
		return 0
	}
	return m.height - helpFooterLines
}

// helpBody is the keybinding list. Every entry here must have a matching case
// in handleTableKey or handleDetailKey — `t` and `/` were advertised for a
// long time with no handler behind them, and the `s` list was missing a
// column the sort cycle had gained.
func helpBody() string {
	return `
  Keybindings
  ───────────
  ↑/↓, j/k      Navigate table
  PgUp/PgDn     Page up/down (table, detail, help)
  Home/G        Go to top/bottom
  g             Toggle cluster grouping (when clusters are available)
  Enter         View fork details
  n             Search new repository
  /             Filter forks by owner/name
  Esc           Clear the active filter
  s             Cycle sort column (heat/stars/ahead/branches/forks/pushed)
  S             Reverse sort order
  o             Open selected fork in browser
  c             Open compare view in browser
  y             Yank clone command to clipboard
  t             Cycle enrichment ceiling — T3 full, T2 no lone-wolf, T1 no compares
  r             Refresh (bypass cache, restart)
  Space         Mark/unmark fork for export
  e             Export marked forks to JSON
  E             Export all forks to JSON
  ?             Toggle this help
  q, Ctrl+C     Quit
`
}
