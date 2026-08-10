package tui

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) viewTable() string {
	if m.parent == nil || len(m.forks) == 0 {
		return "\n  No forks found.\n"
	}

	// Absolute indices of the rows the active filter admits. Everything below
	// -- the window, the cluster-header lookback, the gutter colouring, the
	// counters -- works in this space; m.forks itself is never resliced.
	vis := m.visibleIdx()

	var b strings.Builder

	b.WriteString(m.renderStatusBar())
	b.WriteString("\n")

	// Column header
	sortInd := func(col string) string {
		if col == m.sortCol {
			if m.sortAsc {
				return "▲"
			}
			return "▼"
		}
		return " "
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
	if hasCompare {
		header := fmt.Sprintf("%-4s %3s  %-26s  %s %6s %7s %7s  %-10s  %s",
			"HEAT", sortInd("heat"), "REPOSITORY",
			padLeftCells("★"+sortInd("stars"), 5),
			"AHEAD"+sortInd("ahead"), "BEHIND",
			"BRANCH"+sortInd("branches"),
			"PUSHED"+sortInd("pushed"), "STATUS")
		b.WriteString(headerStyle.Render(header))
	} else {
		header := fmt.Sprintf("%-4s %3s  %-30s %s %s %7s  %-12s",
			"HEAT", sortInd("heat"), "REPOSITORY",
			padLeftCells("★"+sortInd("stars"), 5),
			padLeftCells("⑂"+sortInd("forks"), 5),
			"BRANCH"+sortInd("branches"),
			"PUSHED"+sortInd("pushed"))
		b.WriteString(headerStyle.Render(header))
	}
	b.WriteString("\n")

	// A filter matching nothing is not the same as having no forks: the
	// early return above only covers len(m.forks) == 0, so without this the
	// frame renders a header, zero rows and no way out.
	if len(vis) == 0 {
		b.WriteString("\n  " + subtitleStyle.Render(fmt.Sprintf("No forks match %q", m.filter)) + "\n")
		b.WriteString(helpStyle.Render("  Esc clear filter  /  edit filter  ? help  q quit"))
		return b.String()
	}

	// Rows. The window is computed over VISIBLE positions, not raw fork
	// indices, so a filter that hides rows does not leave gaps in the frame.
	visibleRows := m.pageSize()

	cursorPos := visiblePos(m.cursor, vis)
	if cursorPos < 0 {
		cursorPos = 0
	}
	start := 0
	if cursorPos >= visibleRows {
		start = cursorPos - visibleRows + 1
	}
	end := start + visibleRows
	if end > len(vis) {
		end = len(vis)
	}

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
			prefix = "▸"
		} else if sf.Marked {
			prefix = "*"
		}

		// Score rendering: ~ prefix while enriching, space when settled
		scorePrefix := " "
		if sf.Enriching {
			scorePrefix = "~"
		}

		heatBar := RenderHeatBar(sf.Heat.Score)
		score := fmt.Sprintf("%2.0f", sf.Heat.Score)
		scoreColor := HeatColor(sf.Heat.Score)
		scoreStyled := lipgloss.NewStyle().Foreground(scoreColor).Render(score)

		name := sf.Fork.ID
		pushed := relativeTimeSince(sf.Fork.PushedAt)

		// Badges
		badges := renderBadges(sf)

		// Branches carrying commits upstream lacks. Nil means never counted
		// (non-GitHub provider, or the sweep has not landed yet) and renders
		// "-", distinct from a counted 0.
		branches := "      -"
		if n := sf.Fork.DivergentBranches; n != nil {
			branches = fmt.Sprintf("%7d", *n)
		}

		// Duplicate-group gutter. Members of a contiguous group all draw the
		// same coloured bar, so the group reads as one unbroken line.
		gutter := duplicateGutter(sf, gutterOrd)

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
			row = fmt.Sprintf("%s%s%s%s%s  %-26s  %5d %6s %7s %7s  %-10s  %s",
				gutter, prefix, scorePrefix, heatBar, scoreStyled, name, sf.Fork.Stars, ahead, behind, branches, pushed, badges)
		} else {
			if len(name) > 30 {
				name = name[:27] + "..."
			}
			row = fmt.Sprintf("%s%s%s%s%s  %-30s %5d %5d %7s  %-12s",
				gutter, prefix, scorePrefix, heatBar, scoreStyled, name, sf.Fork.Stars, sf.Fork.SubForkCount, branches, pushed)
		}

		if isSelected {
			row = selectedStyle.Render(row)
		}

		b.WriteString(row)
		b.WriteString("\n")
	}

	// Feedback messages (export, clipboard)
	if m.clipMsg != "" && time.Since(m.clipMsgTime) < 5*time.Second {
		b.WriteString(" " + subtitleStyle.Render(m.clipMsg) + "\n")
	} else if m.errMsg != "" && time.Since(m.errMsgTime) < 5*time.Second {
		b.WriteString(" " + subtitleStyle.Render(m.errMsg) + "\n")
	} else if cs := m.clusterFooter(); cs != "" {
		b.WriteString(" " + subtitleStyle.Render(cs) + "\n")
	} else {
		b.WriteString("\n")
	}
	if legend := m.badgeLegend(); legend != "" {
		b.WriteString(helpStyle.Render(" "+legend) + "\n")
	}
	b.WriteString(helpStyle.Render(" ↑↓ navigate  Enter detail  Space mark  e export marked  E export all  o open  y yank  s sort  g cluster  ? help  q quit"))

	return b.String()
}

func renderBadges(sf ScoredFork) string {
	var badges []string

	// Lone wolf badge (v2)
	if sf.Heat.LoneWolfV2 != nil && sf.Heat.LoneWolfV2.Detected {
		badges = append(badges, lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Render("🐺"))
	}

	// Open PR badge
	if sf.Fork.OpenPRCount > 0 {
		badges = append(badges, "📬")
	}

	// Fork of fork badge
	if sf.Fork.SubForkCount > 0 {
		badges = append(badges, "⛓")
	}

	// Side branch badge
	if sf.T2 != nil && sf.T2.IsBranchWork && sf.T2.ActiveBranch != sf.Fork.DefaultBranch {
		badges = append(badges, "🌱")
	}

	// Releases badge
	if sf.Fork.ReleaseCount > 0 {
		badges = append(badges, "🏷️")
	}

	// Duplicate work: this fork is one of N carrying identical changes.
	if sf.SiblingCount > 1 {
		badges = append(badges, fmt.Sprintf("👯%d", sf.SiblingCount))
	}

	return strings.Join(badges, " ")
}

// badgeLegend returns a one-line legend for badges visible in the current fork list.
// Only includes badges that actually appear, so the legend stays compact.
func (m Model) badgeLegend() string {
	var hasWolf, hasPR, hasSubFork, hasBranch, hasRelease, hasDupe bool
	for _, sf := range m.forks {
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
		parts = append(parts, "🐺 lone wolf")
	}
	if hasPR {
		parts = append(parts, "📬 open PR")
	}
	if hasSubFork {
		parts = append(parts, "⛓ has sub-forks")
	}
	if hasBranch {
		parts = append(parts, "🌱 branch work")
	}
	if hasRelease {
		parts = append(parts, "🏷️ releases")
	}
	if hasDupe {
		parts = append(parts, "👯 duplicate work (same line = same group)")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "  ")
}

func (m Model) renderStatusBar() string {
	parts := []string{"spoon"}

	if m.parent != nil {
		parts = append(parts, m.parent.FullName)
		parts = append(parts, fmt.Sprintf("%d forks", len(m.forks)))
	}

	if m.enriching {
		parts = append(parts, fmt.Sprintf("T2: %d/%d", m.enrichDone, m.enrichTotal))
	}

	if m.auth.RateLimit > 0 {
		headroom := m.provider.Headroom()
		remaining := int(headroom * float64(m.auth.RateLimit))
		parts = append(parts, fmt.Sprintf("API: %d/%d", remaining, m.auth.RateLimit))
	}
	if !m.auth.Authenticated() {
		parts = append(parts, warnStyle.Render("⚠ Unauthenticated"))
	}

	marked := 0
	for _, f := range m.forks {
		if f.Marked {
			marked++
		}
	}
	if marked > 0 {
		parts = append(parts, fmt.Sprintf("%d marked", marked))
	}

	bar := strings.Join(parts, " │ ")
	return statusBarStyle.Width(m.width).Render(bar)
}

func (m *Model) cycleSortColumn() {
	cols := []string{"heat", "stars", "ahead", "branches", "forks", "pushed"}
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
		// less and equal are computed separately so ties can be reported as
		// "neither less nor greater". Deriving the descending case as !less
		// alone would return true for both (i,j) and (j,i) on a tie, which is
		// not a strict weak ordering: sort is then free to reorder equal
		// elements, defeating SliceStable and making the row order that
		// gatherDuplicateGroups anchors to non-deterministic.
		var less, equal bool
		switch m.sortCol {
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

// renderClusterHeader returns a one-line styled header for a cluster
// group. The label and member-count are pulled from any fork in the
// group that has them populated (cluster fields are written uniformly
// per-cluster by the pipeline, so any member's copy suffices).
//
// Empty ClusterID renders as "(ungrouped)"; "noise" renders as "noise"
// with no label. Real clusters render as "── <id>: <label> (N members) ──"
// in the dim help-text style so they don't visually compete with rows.
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
	line := "── " + inner + " ──"
	return helpStyle.Render(" " + line)
}

// clusterLabelAndCount returns the label and member-count for a cluster
// ID by scanning m.forks. The label is read from the first fork that
// has a non-empty ClusterLabel; member-count is computed by tallying
// matching ClusterID entries.
func (m Model) clusterLabelAndCount(clusterID string) (string, int) {
	label := ""
	count := 0
	for i := range m.forks {
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
