package tui

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"github.com/svnbjrn/spoon/internal/tui/ui"
)

func (m Model) viewTable() string {
	ctx, styles := m.themeContext(), m.styles()
	if m.parent == nil || len(m.forks) == 0 {
		return m.viewTableWithoutRows()
	}

	// Absolute indices of the rows the active filter admits. Everything below
	// -- the window, the cluster-header lookback, the gutter colouring, the
	// counters -- works in this space; m.forks itself is never resliced.
	vis := m.visibleIdx()

	var b strings.Builder

	if !m.fullscreen {
		b.WriteString(m.renderStatusBar())
		b.WriteString("\n")
	}

	// Column header
	sortInd := func(col string) string {
		if col != m.sortCol {
			return " "
		}
		if m.sortAsc {
			return ctx.Glyph(theme.SortUp)
		}
		return ctx.Glyph(theme.SortDown)
	}

	// Colour duplicate groups by order of appearance so two groups that end up
	// adjacent never share a colour. Computed over the visible forks: two
	// groups the filter brings next to each other must still differ, and a
	// group filtered out entirely should not consume a colour.
	visForks := make([]ScoredFork, 0, len(vis))
	for _, i := range vis {
		visForks = append(visForks, m.forks[i])
	}
	gutterOrd := gutterOrdinals(visForks)

	hasCompare := false
	for _, f := range m.forks {
		if f.T2 != nil {
			hasCompare = true
			break
		}
	}

	// Header field widths mirror the row cell layout exactly: rows lead with
	// gutter(1) + cursor prefix(2) + heat bar(4) + score(2) = 9 cells, matched
	// here by " %-4s %3s"; the name field is %-26s / %-30s in the rows, so the
	// same width is used for REPOSITORY. Any width changed on one side must
	// change on the other, or every column right of it drifts — asserted by
	// TestViewTable_HeaderAndRowsAlign.
	//
	// The glyph labels (★, ⑂) go through padLeftCells rather than a %Ns verb:
	// fmt pads by rune count, but lipgloss measures ★ at two cells, so a
	// rune-padded field is one cell wider on screen than the number columns
	// under it.
	padLeftCells := func(s string, w int) string {
		if n := w - lipgloss.Width(s); n > 0 {
			return strings.Repeat(" ", n) + s
		}
		return s
	}
	// headerStyle carries Padding(0,1), so its left pad is the header's first
	// cell; the format strings therefore start one cell earlier than the rows.
	// The EMB field is one 4-cell run -- a leading separator space plus 3 cells
	// of content -- carried as a single string on BOTH sides rather than as a
	// width verb with literal spaces around it. Written the latter way the
	// spaces survive when the field is empty and the header gains a cell the
	// rows do not, which is precisely the drift TestViewTable_HeaderAndRowsAlign
	// exists to catch. Empty on both sides when neither provider is configured,
	// so a host that will never embed anything does not pay a column for it.
	embHeader := ""
	if m.embedColumnShown() {
		embHeader = " EMB"
	}

	// Linear-history cell: ✓ when the boolean is true (or "~" for the
	// truncated variant), ✗ when false, "-" when unknown. The header
	// carries the same content on both sides of the conditional to keep
	// header/row alignment under TestViewTable_HeaderAndRowsAlign — see
	// the parallel `embHeader` note above.
	linHeader := " LIN"
	if hasCompare {
		header := fmt.Sprintf("%-4s %3s %3s  %-26s  %s%s %6s %7s %7s%s %-10s  %s",
			"HEAT", sortInd("heat"), "P"+sortInd(pscoreSortCol), "REPOSITORY",
			padLeftCells(ctx.Glyph(theme.Star)+sortInd("stars"), 5),
			embHeader,
			"AHEAD"+sortInd("ahead"), "BEHIND",
			"BRANCH"+sortInd("branches"),
			linHeader,
			"PUSHED"+sortInd("pushed"), "STATUS")
		b.WriteString(ui.TableHeader(ctx, " "+header, 0))
	} else {
		header := fmt.Sprintf("%-4s %3s %3s  %-30s %s %s%s %7s%s %-12s",
			"HEAT", sortInd("heat"), "P"+sortInd(pscoreSortCol), "REPOSITORY",
			padLeftCells(ctx.Glyph(theme.Star)+sortInd("stars"), 5),
			padLeftCells(ctx.Glyph(theme.Fork)+sortInd("forks"), 5),
			embHeader,
			"BRANCH"+sortInd("branches"),
			linHeader,
			"PUSHED"+sortInd("pushed"))
		b.WriteString(ui.TableHeader(ctx, " "+header, 0))
	}

	b.WriteString("\n")

	// A filter matching nothing is not the same as having no forks: the
	// early return above only covers len(m.forks) == 0, so without this the
	// frame renders a header, zero rows and no way out.
	if len(vis) == 0 {
		b.WriteString("\n  " + styles.subtitle.Render(fmt.Sprintf("No forks match %q", m.filter)) + "\n")
		b.WriteString(styles.help.Render("  Esc clear filter  /  edit filter  ? help  q quit"))
		return b.String()
	}

	// Rows. The window is computed over VISIBLE positions, not raw fork
	// indices, so a filter that hides rows does not leave gaps in the frame.
	cursorPos := visiblePos(m.cursor, vis)
	if cursorPos < 0 {
		cursorPos = 0
	}
	start, end := rowWindow(cursorPos, m.pageSize(), len(vis))

	prevClusterID := ""
	if m.groupByCluster && start > 0 {
		// Track the cluster that the row immediately above `start` belongs
		// to, so the first header is emitted only when the visible window
		// actually starts a new group. Must read the previous VISIBLE row:
		// reading m.forks[start-1] under a filter would compare against a
		// hidden fork and swallow the header.
		prevClusterID = m.forks[vis[start-1]].Heat.ClusterID
	}

	for p := start; p < end; p++ {
		i := vis[p]
		sf := m.forks[i]

		// Emit a cluster header before the first row of each group when
		// grouping is enabled. Emit at the absolute top of the list, or
		// whenever the cluster ID changes from the previous (visible or
		// scrolled-past) row.
		if m.groupByCluster {
			curID := sf.Heat.ClusterID
			// p, not i: start is a position in visible space, so comparing it
			// against an absolute fork index would emit the first header at
			// the wrong row whenever a filter is active.
			if (start == 0 && p == start) || curID != prevClusterID {
				b.WriteString(m.renderClusterHeader(curID))
				b.WriteString("\n")
			}
			prevClusterID = curID
		}

		isSelected := i == m.cursor

		prefix := " "
		if isSelected {
			prefix = ctx.Glyph(theme.Selected)
		} else if sf.Marked {
			prefix = "*"
		}

		// Score rendering: ~ prefix while enriching, space when settled
		scorePrefix := " "
		if sf.Enriching {
			scorePrefix = "~"
		}

		heatBar := m.renderHeatBar(sf.Heat.Score)
		score := fmt.Sprintf("%2.0f", sf.Heat.Score)
		scoreStyled := lipgloss.NewStyle().Foreground(m.heatColor(sf.Heat.Score)).Render(score)

		// P column: 3 cells, the P-score as a whole percentage, "-" when the
		// row has not been ranked. Header carries "P" under the same width.
		pscore := pscoreCell(sf)

		name := sf.Fork.ID
		pushed := relativeTimeSince(sf.Fork.PushedAt)
		badges := m.renderBadges(sf)

		// Branches carrying commits upstream lacks. Nil means never counted
		// (non-GitHub provider, or the sweep has not landed yet) and renders
		// "-", distinct from a counted 0.
		branches := "      -"
		if n := sf.Fork.DivergentBranches; n != nil {
			branches = fmt.Sprintf("%7d", *n)
		}

		gutter := m.duplicateGutter(sf, gutterOrd)

		// The same 4-cell run as " EMB" above: separator, two glyph cells, and
		// one trailing pad so the field width matches the header's word.
		emb := ""
		if embHeader != "" {
			emb = " " + m.embedCellFor(sf) + " "
		}

		// Linear-history cell mirrors the header's " LIN" four-cell run. The
		// glyph comes from the theme table (theme.Check / theme.Cross) so the
		// ASCII profile degrades cleanly to "+" / "x" rather than baking a
		// literal unicode value into the render. "~" tags a truncated history;
		// "?" is the render when the boolean has not been computed yet.
		linGlyph := "-"
		if sf.Fork.LinearHistory != nil {
			if *sf.Fork.LinearHistory {
				linGlyph = ctx.Glyph(theme.Check)
			} else {
				linGlyph = ctx.Glyph(theme.Cross)
			}
		}
		if sf.Fork.MergeCommitTruncated {
			linGlyph += "~"
		}
		lin := " " + padLeftCells(linGlyph, 2) + " "

		var row string
		if hasCompare {
			if len(name) > 26 {
				name = name[:23] + "..."
			}
			ahead := "  -"
			behind := "  -"
			if sf.T2 != nil {
				ahead = fmt.Sprintf("%5d", sf.T2.AheadCount)
				behind = fmt.Sprintf("%6d", sf.T2.BehindCount)
			} else if sf.Enriching {
				ahead = "   ~"
				behind = "    ~"
			}
			row = fmt.Sprintf("%s%s%s%s%s %s  %-26s  %5d%s %6s %7s %7s%s %-10s  %s",
				gutter, prefix, scorePrefix, heatBar, scoreStyled, pscore, name, sf.Fork.Stars, emb, ahead, behind, branches, lin, pushed, badges)
		} else {
			if len(name) > 30 {
				name = name[:27] + "..."
			}
			row = fmt.Sprintf("%s%s%s%s%s %s  %-30s %5d %5d%s %7s%s %-12s",
				gutter, prefix, scorePrefix, heatBar, scoreStyled, pscore, name, sf.Fork.Stars, sf.Fork.SubForkCount, emb, branches, lin, pushed)
		}

		row = ui.TableRow(ctx, row, isSelected, 0)

		b.WriteString(row)
		b.WriteString("\n")
	}

	if !m.fullscreen {
		if m.clipMsg != "" && time.Since(m.clipMsgTime) < 5*time.Second {
			b.WriteString(" " + styles.subtitle.Render(m.clipMsg) + "\n")
		} else if m.errMsg != "" && time.Since(m.errMsgTime) < 5*time.Second {
			b.WriteString(" " + styles.subtitle.Render(m.errMsg) + "\n")
		} else if rs := m.rankFooter(); rs != "" {
			b.WriteString(" " + styles.subtitle.Render(rs) + "\n")
		} else if cs := m.clusterFooter(); cs != "" {
			b.WriteString(" " + styles.subtitle.Render(cs) + "\n")
		} else {
			b.WriteString("\n")
		}
		if legend := m.tableLegends(); legend != "" {
			b.WriteString(styles.help.Render(" "+legend) + "\n")
		}
		b.WriteString(m.tableKeyLegend())
	}
	return b.String()
}

func (m Model) renderBadges(sf ScoredFork) string {
	var badges []string
	if sf.Heat.LoneWolfV2 != nil && sf.Heat.LoneWolfV2.Detected {
		badges = append(badges, lipgloss.NewStyle().Foreground(m.themeContext().Palette.AccentRust).Render("WOLF"))
	}
	if sf.Fork.OpenPRCount > 0 {
		badges = append(badges, "PR")
	}
	if sf.Fork.SubForkCount > 0 {
		badges = append(badges, "SUB")
	}
	if sf.T2 != nil && sf.T2.IsBranchWork && sf.T2.ActiveBranch != sf.Fork.DefaultBranch {
		badges = append(badges, "BRANCH")
	}
	if sf.Fork.ReleaseCount > 0 {
		badges = append(badges, "REL")
	}
	if sf.SiblingCount > 1 {
		badges = append(badges, fmt.Sprintf("DUP%d", sf.SiblingCount))
	}
	// TIE: statistically indistinguishable from the neighbouring row in the
	// shortlist order — the order between tied rows is not evidence.
	if sf.Rank != nil && sf.Rank.TieBand {
		badges = append(badges, "TIE")
	}
	return strings.Join(badges, " ")
}

// badgeLegend returns a one-line legend for badges visible in the current fork list.
// Only includes badges that actually appear, so the legend stays compact.
func (m Model) badgeLegend() string {
	var hasWolf, hasPR, hasSubFork, hasBranch, hasRelease, hasDupe bool
	// Over the visible forks only: the legend explains glyphs on screen, so
	// advertising one no rendered row carries is noise -- and it would also
	// cost a frame row that pageSize has budgeted away.
	for _, i := range m.visibleIdx() {
		sf := m.forks[i]
		if sf.Heat.LoneWolfV2 != nil && sf.Heat.LoneWolfV2.Detected {
			hasWolf = true
		}
		if sf.Fork.OpenPRCount > 0 {
			hasPR = true
		}
		if sf.Fork.SubForkCount > 0 {
			hasSubFork = true
		}
		if sf.T2 != nil && sf.T2.IsBranchWork && sf.T2.ActiveBranch != sf.Fork.DefaultBranch {
			hasBranch = true
		}
		if sf.Fork.ReleaseCount > 0 {
			hasRelease = true
		}
		if sf.SiblingCount > 1 {
			hasDupe = true
		}
	}

	var parts []string
	if hasWolf {
		parts = append(parts, "WOLF lone wolf")
	}
	if hasPR {
		parts = append(parts, "PR open PR")
	}
	if hasSubFork {
		parts = append(parts, "SUB has sub-forks")
	}
	if hasBranch {
		parts = append(parts, "BRANCH branch work")
	}
	if hasRelease {
		parts = append(parts, "REL releases")
	}
	if hasDupe {
		parts = append(parts, "DUP duplicate work (same line = same group)")
	}
	return strings.Join(parts, "  ")
}

// viewTableWithoutRows renders the table frame when there are no rows to draw:
// a fetch is still in flight, or one settled with nothing in it.
//
// Both used to collapse to the bare string "No forks found.". That was wrong in
// two directions at once. Pressing `r` nils the fork list to re-fetch it, so the
// very next frame told the user their 113 forks were gone; and a fetch that came
// back empty because it was rate-limited said the same thing as a repository
// that genuinely has no forks, because this branch returned before the feedback
// line further down was ever written.
func (m Model) viewTableWithoutRows() string {
	ctx, styles := m.themeContext(), m.styles()
	width := ui.ContentWidth(m.width) - 2

	var b strings.Builder
	if !m.fullscreen {
		b.WriteString(m.renderStatusBar())
		b.WriteString("\n")
	}
	b.WriteString("\n")

	switch {
	case m.loading:
		msg := m.loadMsg
		if msg == "" {
			msg = "Loading..."
		}
		b.WriteString("  " + ui.Alert(ctx, ui.AlertInfo, msg, width))
	case m.errMsg != "":
		// Deliberately not gated on errMsgTime the way the footer is: an empty
		// table has nothing else to say, so the reason it is empty should not
		// time out and leave the user with a bare "No forks found."
		b.WriteString("  " + ui.TitledAlert(ctx, ui.AlertError, "Forks", m.errMsg, width))
	default:
		b.WriteString("  " + ui.Text(ctx, ui.TextMuted, "No forks found.", width))
	}
	b.WriteString("\n")

	if !m.fullscreen {
		b.WriteString(styles.help.Render("  r refresh  n new repository  ? help  q quit"))
	}
	return b.String()
}

// tableLegendActions are the bindings that earn a permanent slot in the fork
// table's footer. The scope holds 28 of them, which ui.KeyLegend wraps to nine
// lines at the 80-column floor -- more of a 24-line terminal than the fork rows
// were getting. These are the ones a reader needs at hand; `?` reaches the rest,
// and every omitted key still works.
var tableLegendActions = []keymap.Action{
	keymap.OpenDetail,
	keymap.Filter,
	keymap.CycleSort,
	keymap.ToggleMark,
	keymap.ExportMarked,
	keymap.EmbedMarked,
	keymap.Refresh,
	keymap.OpenSettings,
	keymap.ToggleHelp,
	keymap.Quit,
}

// tableKeyLegend renders the table footer. chromeHeight measures this exact
// string, so the two cannot disagree about how many lines the footer costs.
func (m Model) tableKeyLegend() string {
	return ui.KeyLegendActions(m.themeContext(), ui.ContentWidth(m.width), keymap.MainTable, tableLegendActions...)
}

// tableLegends is the single legend line beneath the rows: badges, then the EMB
// column key.
//
// One line, not two, and routed through chromeHeight like everything else --
// the badge legend was already budgeted separately, and adding a second
// hardcoded row for the embed key is how a frame quietly grows past the
// terminal again.
func (m Model) tableLegends() string {
	parts := make([]string, 0, 2)
	if badges := m.badgeLegend(); badges != "" {
		parts = append(parts, badges)
	}
	if emb := m.embedLegend(); emb != "" {
		parts = append(parts, emb)
	}
	return strings.Join(parts, "  ")
}

func (m Model) renderStatusBar() string {
	type statusSegment struct {
		text     string
		priority int
	}

	ctx, styles := m.themeContext(), m.styles()
	separator := " " + ctx.Glyph(theme.BoxVertical) + " "
	var variable []string
	tail := make([]statusSegment, 0, 6)
	appendTail := func(priority int, text string) {
		tail = append(tail, statusSegment{text: text, priority: priority})
	}

	// Refreshing: the user pressed `r` and the existing fork slice stays
	// visible during the network round-trip (startFetch gates the wipe on
	// !m.refresh). Without this segment the user sees the same fork count
	// and cannot tell whether a refresh is in flight or the network is
	// hung. Priority 0 puts it ahead of the fork-count segment so the
	// way around.
	if m.loading && m.refresh {
		appendTail(0, "Refreshing...")
	}

	if m.parent != nil {
		variable = append(variable, m.parent.FullName)
		if m.filter != "" {
			// Both numbers, so a filter can never quietly shrink the fork
			// count into looking like the repo has fewer forks than it does.
			appendTail(1, fmt.Sprintf("%d/%d forks", m.visibleCount(), len(m.forks)))
			variable = append(variable, fmt.Sprintf("filter: %q", m.filter))
		} else if a := m.acquisition; a != nil && a.ExpectedRows > len(m.forks) {
			// Say when the list itself is short: forks missing from it can
			// never be ranked, and nothing else on screen would show it.
			appendTail(1, fmt.Sprintf("%d of %d forks listed", len(m.forks), a.ExpectedRows))
		} else {
			appendTail(1, fmt.Sprintf("%d forks", len(m.forks)))
		}
		if m.unreachable > 0 {
			appendTail(1, fmt.Sprintf("%d gone", m.unreachable))
		}
	}

	if m.batchResolving {
		// One GraphQL sweep over the whole list, no per-fork progress: say
		// so, or a multi-minute pause reads as a hang.
		appendTail(2, fmt.Sprintf("T2: resolving divergence for %d forks (GraphQL)...", m.enrichTotal))
	} else if m.enriching {
		appendTail(2, fmt.Sprintf("T2: %d/%d", m.enrichDone, m.enrichTotal))
	}

	// Enrichment ceiling. Shown only when it is actually capping something --
	// and with the skipped count, because lowering 2→1 changes nothing visible
	// for forks that are already enriched, which reads as "t does nothing".
	if ceiling := m.maxTier(); ceiling < defaultMaxTier {
		skipped := 0
		for i := range m.forks {
			if m.forks[i].TierSkipped {
				skipped++
			}
		}
		seg := fmt.Sprintf("T<=%d", ceiling)
		if skipped > 0 {
			seg += fmt.Sprintf(" (%d skipped)", skipped)
		}
		appendTail(3, seg)
	}
	// Below the fork count but above the API budget: it answers a question the
	// user cannot otherwise answer from inside the session, but it is static,
	// so it yields to anything actionable when the terminal is narrow.
	// A run in progress outranks the static provider summary and replaces it:
	// while work is happening, what it is doing matters more than what is
	// configured, and both would not fit.
	if running := m.embedRunFooter(); running != "" {
		appendTail(2, running)
	} else if line := m.embedStatusLine(); line != "" {
		appendTail(2, line)
	}
	// Hierarchy precision is informational: it yields to everything
	// actionable, like the fork count it sits beside.
	if seg := m.shortlistStatus(); seg != "" {
		appendTail(1, seg)
	}
	if m.auth.RateLimit > 0 {
		headroom := m.provider.Headroom()
		remaining := int(headroom * float64(m.auth.RateLimit))
		appendTail(4, fmt.Sprintf("API: %d/%d", remaining, m.auth.RateLimit))
	}
	if !m.auth.Authenticated() {
		// Keep the authentication warning in the fixed tail: long repository
		// names and filters are informative, but this warning is actionable.
		appendTail(6, styles.warn.Render(ctx.Glyph(theme.Warning)+" Unauthenticated"))
	}
	marked := 0
	for _, fork := range m.forks {
		if fork.Marked {
			marked++
		}
	}
	if marked > 0 {
		appendTail(5, fmt.Sprintf("%d marked", marked))
	}

	contentWidth := ui.ContentWidth(m.width)
	if contentWidth <= 0 {
		return ""
	}
	contentLimit := contentWidth - 2 // statusBar supplies one cell of padding on either side.
	if contentLimit < 0 {
		contentLimit = 0
	}

	render := func(prefix []string, selected []bool) string {
		parts := append([]string(nil), prefix...)
		for i, segment := range tail {
			if selected[i] {
				parts = append(parts, segment.text)
			}
		}
		return strings.Join(parts, separator)
	}

	prefix := []string{"spoon"}
	if len(variable) > 0 {
		// Reserve a visible omission marker before allocating status fields.
		// At narrow widths the parent/filter give way to actionable state.
		prefix = []string{ctx.Glyph(theme.EmDash)}
	}
	// Keep status fields in this documented priority order when the terminal is
	// narrow: authentication warning, marked-work count, API budget, requested
	// tier ceiling, enrichment progress, then the informational fork count.
	// Rendering still preserves their normal left-to-right order below.
	selected := make([]bool, len(tail))
	for priority := 6; priority >= 1; priority-- {
		for i, segment := range tail {
			if segment.priority != priority {
				continue
			}
			selected[i] = true
			if lipgloss.Width(render(prefix, selected)) > contentLimit {
				selected[i] = false
			}
		}
	}

	if len(variable) > 0 {
		selectedContent := render(prefix, selected)
		available := contentLimit - lipgloss.Width(selectedContent) - lipgloss.Width(separator)
		if available > 0 {
			elided := ansi.Truncate(strings.Join(variable, separator), available, ctx.Glyph(theme.EmDash))
			candidatePrefix := []string{"spoon", elided}
			if lipgloss.Width(render(candidatePrefix, selected)) <= contentLimit {
				prefix = candidatePrefix
			}
		}
	}
	content := render(prefix, selected)
	return ui.NavBar(ctx, content, contentWidth)
}

func (m *Model) cycleSortColumn() {
	cols := []string{"heat", pscoreSortCol, "stars", "ahead", "branches", "forks", "pushed", "linear"}
	for i, c := range cols {
		if c == m.sortCol {
			m.sortCol = cols[(i+1)%len(cols)]
			m.sortAsc = false
			m.reapplySort()
			return
		}
	}
	m.sortCol = "heat"
	m.reapplySort()
}

func (m *Model) sortForks() {
	sort.SliceStable(m.forks, m.forkLess())
	// Duplicates share a score, so they usually land adjacent — but ties are
	// not ordered by group, so an unrelated fork with identical stats could
	// sort between two members and fall inside their gutter line. Gather makes
	// the grouping exact rather than incidental.
	m.gatherDuplicateGroups(0, len(m.forks))
	m.cursor = clampCursorVisible(m.cursor, m.visibleIdx())
}

// forkLess returns the row comparator for the active sort column. Extracted so
// the strict-weak-ordering property can be asserted directly rather than
// inferred from sorted output.
func (m *Model) forkLess() func(i, j int) bool {
	return func(i, j int) bool {
		var less, equal bool
		switch m.sortCol {
		case "linear":
			// nil sorts below known: the user pressed `s` to see linear forks
			// first, and a row that hasn't been measured yet shouldn't jump
			// ahead of one that was.
			ki, kj := -1, -1
			if b := m.forks[i].Fork.LinearHistory; b != nil {
				if *b {
					ki = 2
				} else {
					ki = 1
				}
			}
			if b := m.forks[j].Fork.LinearHistory; b != nil {
				if *b {
					kj = 2
				} else {
					kj = 1
				}
			}
			less, equal = ki < kj, ki == kj
		case "heat":
			a, b := m.forks[i].Heat.Score, m.forks[j].Heat.Score
			less, equal = a < b, a == b
		case "stars":
			a, b := m.forks[i].Fork.Stars, m.forks[j].Fork.Stars
			less, equal = a < b, a == b
		case "forks":
			a, b := m.forks[i].Fork.SubForkCount, m.forks[j].Fork.SubForkCount
			less, equal = a < b, a == b
		case "ahead":
			ai, aj := 0, 0
			if m.forks[i].T2 != nil {
				ai = m.forks[i].T2.AheadCount
			}
			if m.forks[j].T2 != nil {
				aj = m.forks[j].T2.AheadCount
			}
			less, equal = ai < aj, ai == aj
		case "branches":
			// Unknown (nil) sorts as -1 so it lands below a genuine 0 rather
			// than tying with it.
			bi, bj := -1, -1
			if n := m.forks[i].Fork.DivergentBranches; n != nil {
				bi = *n
			}
			if n := m.forks[j].Fork.DivergentBranches; n != nil {
				bj = *n
			}
			less, equal = bi < bj, bi == bj
		case "pushed":
			a, b := m.forks[i].Fork.PushedAt, m.forks[j].Fork.PushedAt
			less, equal = a.Before(b), a.Equal(b)
		case relevanceSortCol:
			a, b := m.relevanceScore(i), m.relevanceScore(j)
			less, equal = a < b, a == b
		case pscoreSortCol:
			// Unranked rows (nil Rank) take −1 so they sit below every real
			// P-score in the default descending order.
			a, b := -1.0, -1.0
			if r := m.forks[i].Rank; r != nil {
				a = r.PScore
			}
			if r := m.forks[j].Rank; r != nil {
				b = r.PScore
			}
			less, equal = a < b, a == b
		default:
			a, b := m.forks[i].Heat.Score, m.forks[j].Heat.Score
			less, equal = a < b, a == b
		}
		if equal {
			return false
		}
		if m.sortAsc {
			return less
		}
		return !less
	}
}

// renderClusterHeader returns a one-line styled header for a cluster group.
func (m Model) renderClusterHeader(clusterID string) string {
	label, count := m.clusterLabelAndCount(clusterID)
	var inner string
	switch clusterID {
	case "":
		inner = fmt.Sprintf("(ungrouped) (%d members)", count)
	case "noise":
		inner = fmt.Sprintf("noise (%d members)", count)
	default:
		if label == "" {
			inner = fmt.Sprintf("%s (%d members)", clusterID, count)
		} else {
			inner = fmt.Sprintf("%s: %s (%d members)", clusterID, label, count)
		}
	}
	ctx := m.themeContext()
	line := strings.Repeat(ctx.Glyph(theme.BoxHorizontal), 2) + " " + inner + " " + strings.Repeat(ctx.Glyph(theme.BoxHorizontal), 2)
	return m.styles().help.Render(" " + line)
}

// clusterLabelAndCount returns the label and member-count for a cluster
// ID by scanning m.forks. The label is read from the first fork that
// has a non-empty ClusterLabel; member-count is computed by tallying
// matching ClusterID entries.
func (m Model) clusterLabelAndCount(clusterID string) (string, int) {
	label := ""
	count := 0
	// Counted over the visible forks: with a filter active, a header claiming
	// "(12 members)" above two rendered rows is simply wrong.
	for _, i := range m.visibleIdx() {
		if m.forks[i].Heat.ClusterID != clusterID {
			continue
		}
		count++
		if label == "" && m.forks[i].Heat.ClusterLabel != "" {
			label = m.forks[i].Heat.ClusterLabel
		}
	}
	return label, count
}

// clusterGroupRank returns a sort key for a cluster ID such that:
//   - real clusters (e.g. "c0", "c1", "c2") sort by their numeric suffix asc,
//   - the empty-cluster bucket ("") sorts just before "noise",
//   - the "noise" bucket sorts last.
//
// The returned pair (rank, numeric-or-id) is compared by callers: equal
// ranks fall through to a stable numeric comparator for real clusters
// (so "c10" sorts after "c2"), or a string comparator otherwise.
func clusterGroupRank(id string) (int, int, string) {
	switch id {
	case "noise":
		return 2, 0, ""
	case "":
		return 1, 0, ""
	default:
		return 0, clusterNumericKey(id), id
	}
}

// clusterNumericKey parses the numeric suffix of a "c<N>" cluster ID.
// Returns math.MaxInt for IDs that don't match the canonical form, so
// they sort after all numbered clusters but in a stable order driven by
// the string fallback.
func clusterNumericKey(id string) int {
	if len(id) > 1 && id[0] == 'c' {
		if n, err := strconv.Atoi(id[1:]); err == nil {
			return n
		}
	}
	return math.MaxInt
}

// sortForksByCluster orders forks by cluster, then by the active sort column
// and direction within each group. Cluster order: real clusters first (sorted
// by numeric suffix asc so "c10" follows "c2"), then the ungrouped bucket,
// then "noise" last.
//
// Within-cluster ordering delegates to forkLess rather than hard-coding heat
// descending: the header renders ▲/▼ from sortCol/sortAsc regardless of
// grouping, so an ordering that ignored them would make the indicator claim a
// sort the rows do not have, and the s/S keys would silently do nothing in
// grouped mode. The default (heat, descending) is unchanged.
func (m *Model) sortForksByCluster() {
	less := m.forkLess()
	sort.SliceStable(m.forks, func(i, j int) bool {
		ri, ni, ki := clusterGroupRank(m.forks[i].Heat.ClusterID)
		rj, nj, kj := clusterGroupRank(m.forks[j].Heat.ClusterID)
		if ri != rj {
			return ri < rj
		}
		if ni != nj {
			return ni < nj
		}
		if ki != kj {
			return ki < kj
		}
		return less(i, j)
	})
	// Gather inside each cluster block so cluster grouping stays the outer
	// structure; a duplicate group spanning two clusters is left split rather
	// than breaking the cluster headers.
	for lo := 0; lo < len(m.forks); {
		hi := lo + 1
		for hi < len(m.forks) && m.forks[hi].Heat.ClusterID == m.forks[lo].Heat.ClusterID {
			hi++
		}
		m.gatherDuplicateGroups(lo, hi)
		lo = hi
	}
	m.cursor = clampCursorVisible(m.cursor, m.visibleIdx())
}
