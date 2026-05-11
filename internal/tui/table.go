package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) viewTable() string {
	if m.parent == nil || len(m.forks) == 0 {
		return "\n  No forks found.\n"
	}

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

	hasCompare := false
	for _, f := range m.forks {
		if f.T2 != nil {
			hasCompare = true
			break
		}
	}

	if hasCompare {
		header := fmt.Sprintf(" %-4s %3s  %-28s  %5s %6s %7s  %-10s  %s",
			"HEAT", sortInd("heat"), "REPOSITORY",
			"★"+sortInd("stars"), "AHEAD"+sortInd("ahead"), "BEHIND",
			"PUSHED"+sortInd("pushed"), "STATUS")
		b.WriteString(headerStyle.Render(header))
	} else {
		header := fmt.Sprintf(" %-4s %3s  %-30s %5s %5s  %-12s",
			"HEAT", sortInd("heat"), "REPOSITORY",
			"★"+sortInd("stars"), "⑂"+sortInd("forks"),
			"PUSHED"+sortInd("pushed"))
		b.WriteString(headerStyle.Render(header))
	}
	b.WriteString("\n")

	// Rows
	visibleRows := m.height - 4
	if visibleRows < 1 {
		visibleRows = 10
	}

	start := 0
	if m.cursor >= visibleRows {
		start = m.cursor - visibleRows + 1
	}
	end := start + visibleRows
	if end > len(m.forks) {
		end = len(m.forks)
	}

	prevClusterID := ""
	if m.groupByCluster && start > 0 {
		// Track the cluster that the row immediately above `start` belongs
		// to, so the first header is emitted only when the visible window
		// actually starts a new group.
		prevClusterID = m.forks[start-1].Heat.ClusterID
	}

	for i := start; i < end; i++ {
		sf := m.forks[i]

		// Emit a cluster header before the first row of each group when
		// grouping is enabled.
		if m.groupByCluster {
			curID := sf.Heat.ClusterID
			if i == start || curID != prevClusterID {
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
			row = fmt.Sprintf("%s%s%s%s  %-26s  %5d %6s %7s  %-10s  %s",
				prefix, scorePrefix, heatBar, scoreStyled, name, sf.Fork.Stars, ahead, behind, pushed, badges)
		} else {
			if len(name) > 30 {
				name = name[:27] + "..."
			}
			row = fmt.Sprintf("%s%s%s%s  %-30s %5d %5d  %-12s",
				prefix, scorePrefix, heatBar, scoreStyled, name, sf.Fork.Stars, sf.Fork.SubForkCount, pushed)
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

	// Lone wolf badge
	if sf.Heat.LoneWolf != nil && sf.Heat.LoneWolf.Detected {
		badges = append(badges, lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Render("🐺"))
	}
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

	return strings.Join(badges, " ")
}

// badgeLegend returns a one-line legend for badges visible in the current fork list.
// Only includes badges that actually appear, so the legend stays compact.
func (m Model) badgeLegend() string {
	var hasWolf, hasPR, hasSubFork, hasBranch, hasRelease bool
	for _, sf := range m.forks {
		if (sf.Heat.LoneWolf != nil && sf.Heat.LoneWolf.Detected) ||
			(sf.Heat.LoneWolfV2 != nil && sf.Heat.LoneWolfV2.Detected) {
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
	cols := []string{"heat", "stars", "ahead", "forks", "pushed"}
	for i, c := range cols {
		if c == m.sortCol {
			m.sortCol = cols[(i+1)%len(cols)]
			m.sortAsc = false
			m.sortForks()
			return
		}
	}
	m.sortCol = "heat"
	m.sortForks()
}

func (m *Model) sortForks() {
	sort.SliceStable(m.forks, func(i, j int) bool {
		var less bool
		switch m.sortCol {
		case "heat":
			less = m.forks[i].Heat.Score < m.forks[j].Heat.Score
		case "stars":
			less = m.forks[i].Fork.Stars < m.forks[j].Fork.Stars
		case "forks":
			less = m.forks[i].Fork.SubForkCount < m.forks[j].Fork.SubForkCount
		case "ahead":
			ai, aj := 0, 0
			if m.forks[i].T2 != nil {
				ai = m.forks[i].T2.AheadCount
			}
			if m.forks[j].T2 != nil {
				aj = m.forks[j].T2.AheadCount
			}
			less = ai < aj
		case "pushed":
			less = m.forks[i].Fork.PushedAt.Before(m.forks[j].Fork.PushedAt)
		default:
			less = m.forks[i].Heat.Score < m.forks[j].Heat.Score
		}
		if m.sortAsc {
			return less
		}
		return !less
	})
	if m.cursor >= len(m.forks) {
		m.cursor = len(m.forks) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
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
//   - real clusters (e.g. "c0", "c1", "c2") sort by ID string asc,
//   - the empty-cluster bucket ("") sorts just before "noise",
//   - the "noise" bucket sorts last.
//
// The returned pair (rank, id) is compared lexicographically by callers.
func clusterGroupRank(id string) (int, string) {
	switch id {
	case "noise":
		return 2, ""
	case "":
		return 1, ""
	default:
		return 0, id
	}
}

// sortForksByCluster orders forks by cluster, then by heat descending
// within each group. Cluster order: real clusters first (sorted by
// ClusterID asc), then the ungrouped bucket, then "noise" last.
func (m *Model) sortForksByCluster() {
	sort.SliceStable(m.forks, func(i, j int) bool {
		ri, ki := clusterGroupRank(m.forks[i].Heat.ClusterID)
		rj, kj := clusterGroupRank(m.forks[j].Heat.ClusterID)
		if ri != rj {
			return ri < rj
		}
		if ki != kj {
			return ki < kj
		}
		// Within a cluster, higher heat first.
		return m.forks[i].Heat.Score > m.forks[j].Heat.Score
	})
	if m.cursor >= len(m.forks) {
		m.cursor = len(m.forks) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}
