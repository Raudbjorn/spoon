package tui

func (m Model) viewHelp() string {
	help := `
  Keybindings
  ───────────
  ↑/↓, j/k     Navigate table
  Home/G        Go to top/bottom
  g             Toggle cluster grouping (when clusters are available)
  Enter         View fork details
  n             Search new repository
  /             Filter forks
  s             Cycle sort column (heat/stars/ahead/forks/pushed)
  S             Reverse sort order
  o             Open selected fork in browser
  c             Open compare view in browser
  y             Yank clone command to clipboard
  t             Cycle max enrichment tier (T1/T2/T3)
  r             Refresh (bypass cache, restart)
  Space         Mark/unmark fork for export
  e             Export marked forks to JSON
  E             Export all forks to JSON
  ?             Toggle this help
  q, Ctrl+C     Quit
`
	return titleStyle.Render("  spoon") + subtitleStyle.Render(" — help") + "\n" + help + "\n  " + helpStyle.Render("Press ? or Esc to go back")
}
