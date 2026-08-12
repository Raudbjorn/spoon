package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// detailFooterLines is how many lines viewDetail reserves for the pinned
// action hint below the scrolled body (one blank + one hint).
const detailFooterLines = 2

// viewDetail renders the selected fork's detail box, scrolled to
// m.detailOffset, with the action hint pinned below the window. The hint
// stays outside the scrolled region deliberately: scrolling "[b/Esc] Back"
// off the top would leave no visible way out of the view.
func (m Model) viewDetail() string {
	if m.cursor < 0 || m.cursor >= len(m.forks) {
		return "\n  No fork selected.\n"
	}
	body := scrollLines(m.detailBody(), m.detailOffset, m.detailViewHeight())
	return body + "\n\n  " + helpStyle.Render("[o] Open  [c] Compare  [y] Yank  [PgUp/PgDn] Scroll  [b/Esc] Back")
}

// detailViewHeight is how many body lines fit on screen. Zero (no
// WindowSizeMsg yet, as in every model built directly in a test) makes
// scrollLines a pass-through, so the body renders whole.
func (m Model) detailViewHeight() int {
	if m.height <= 0 {
		return 0
	}
	return m.height - detailFooterLines
}

func (m Model) detailBody() string {
	if m.cursor < 0 || m.cursor >= len(m.forks) {
		return ""
	}
	sf := m.forks[m.cursor]
	var b strings.Builder

	boxWidth := 55
	if m.width > 10 {
		boxWidth = m.width - 6
		if boxWidth > 70 {
			boxWidth = 70
		}
	}
	hr := strings.Repeat("─", boxWidth-2)

	b.WriteString("\n")
	b.WriteString("╭" + hr + "╮\n")
	b.WriteString(fitBoxLine("│ "+lipgloss.NewStyle().Bold(true).Render("Fork: "+sf.Fork.ID), boxWidth) + "\n")

	// Heat bar
	scoreColor := HeatColor(sf.Heat.Score)
	scoreStr := lipgloss.NewStyle().Foreground(scoreColor).Bold(true).Render(fmt.Sprintf("%.0f/100", sf.Heat.Score))
	b.WriteString(fitBoxLine(fmt.Sprintf("│ Heat: %s %s", RenderHeatBar(sf.Heat.Score), scoreStr), boxWidth) + "\n")

	b.WriteString("├" + hr + "┤\n")

	// Why it's hot
	b.WriteString(fitBoxLine("│ 🔥 Why it's hot:", boxWidth) + "\n")

	// Component breakdown (v2)
	for _, c := range sf.Heat.Components {
		if c.Points < 0.5 {
			continue
		}
		// Keep the arrow neutral when Max is 0 (undefined ratio); only an
		// actual denominator earns an up/down direction. Otherwise a 0/0
		// component would render "↓" and read as "declining".
		arrow := "→"
		if c.Max > 0 {
			pct := c.Points / c.Max
			if pct > 0.75 {
				arrow = "↑"
			} else if pct < 0.25 {
				arrow = "↓"
			}
		}
		desc := componentDescription(c.Name, c.Raw, c.Points, c.Max)
		line := fmt.Sprintf("│  %s %s", arrow, desc)
		b.WriteString(fitBoxLine(line, boxWidth) + "\n")
	}

	// Penalties
	if len(sf.Heat.Penalties) > 0 {
		for _, p := range sf.Heat.Penalties {
			desc := penaltyDescription(p)
			b.WriteString(fitBoxLine("│  ↓ "+desc, boxWidth) + "\n")
		}
	}

	// Lone wolf section (v2)
	lwV2 := sf.Heat.LoneWolfV2
	if lwV2 != nil && lwV2.Detected {
		b.WriteString("├" + hr + "┤\n")

		archLabel := lwV2.Label
		if lwV2.Archetype.String() != "" {
			archLabel = lwV2.Archetype.String()
		}
		wolfStyle := lipgloss.NewStyle().Foreground(theme.Dark.AccentRust).Bold(true)
		b.WriteString(fitBoxLine("│ "+wolfStyle.Render("🐺 The "+archLabel), boxWidth) + "\n")
		b.WriteString(fitBoxLine(fmt.Sprintf("│  Solo dev, active over %.0f days", lwV2.CommitSpanDays), boxWidth) + "\n")
		b.WriteString(fitBoxLine(fmt.Sprintf("│  %d commits · MNA %d", lwV2.MeaningfulCommits, lwV2.MNA), boxWidth) + "\n")
	}

	// Branch info
	if sf.T2 != nil && sf.T2.IsBranchWork && sf.T2.ActiveBranch != sf.Fork.DefaultBranch {
		b.WriteString("├" + hr + "┤\n")
		b.WriteString(fitBoxLine("│ ⚠  Work is on branch: "+sf.T2.ActiveBranch, boxWidth) + "\n")
		b.WriteString(fitBoxLine("│    [y] Yank clone & checkout command", boxWidth) + "\n")
	}

	// Cluster info (only present after a successful cluster pipeline run).
	if sf.Heat.ClusterID != "" {
		b.WriteString("├" + hr + "┤\n")
		label := sf.Heat.ClusterLabel
		if label == "" {
			label = sf.Heat.ClusterID
		}
		// "noise" gets a minimal block — no label noise, no peers.
		isNoise := sf.Heat.ClusterID == "noise"

		line := fmt.Sprintf("│ Cluster: %s", label)
		if !isNoise && sf.Heat.ClusterMemberCount > 0 {
			line += fmt.Sprintf(" (%d members)", sf.Heat.ClusterMemberCount)
		}
		b.WriteString(fitBoxLine(line, boxWidth) + "\n")
		// R5: cluster isolation context — the "isolation" line surfaces
		// how far this fork is from the rest of its cluster. The novelty
		// score encodes distance from centroid: ~1.0 for noise points
		// (including the 0.5 demotion for empty noise forks from R3) and
		// lower for tight cluster members. The member count gives scale
		// context; noise points deliberately omit it because
		// ClusterMemberCount is 0 for them — a hard-coded "1 member"
		// would assert a value we don't track. Only emitted when
		// ClusterID != "" (clustering ran).
		var iso string
		if isNoise {
			iso = fmt.Sprintf("│  isolation: %.2f/1.0 (cluster noise)", sf.Heat.NoveltyScore)
		} else {
			iso = fmt.Sprintf("│  isolation: %.2f/1.0 (cluster %s, %d members)",
				sf.Heat.NoveltyScore, sf.Heat.ClusterID, sf.Heat.ClusterMemberCount)
		}
		b.WriteString(fitBoxLine(iso, boxWidth) + "\n")
		if sf.Heat.NoveltyScore > 0 {
			nl := fmt.Sprintf("│  Novelty: %.2f", sf.Heat.NoveltyScore)
			b.WriteString(fitBoxLine(nl, boxWidth) + "\n")
		}
		if sf.Heat.ChangeImpact > 0 {
			ci := fmt.Sprintf("│  ChangeImpact: %.2f", sf.Heat.ChangeImpact)
			b.WriteString(fitBoxLine(ci, boxWidth) + "\n")
		}

		if !isNoise {
			peers := m.collectClusterPeers(sf.Heat.ClusterID, sf.Fork.ID)
			if len(peers) > 0 {
				const maxShown = 5
				header := fmt.Sprintf("│  Cluster peers (%d):", len(peers))
				b.WriteString(fitBoxLine(header, boxWidth) + "\n")
				shown := peers
				extra := 0
				if len(shown) > maxShown {
					extra = len(shown) - maxShown
					shown = shown[:maxShown]
				}
				for _, sib := range shown {
					row := "│    " + sib
					b.WriteString(fitBoxLine(row, boxWidth) + "\n")
				}
				if extra > 0 {
					more := fmt.Sprintf("│    ... and %d more", extra)
					b.WriteString(fitBoxLine(more, boxWidth) + "\n")
				}
			}
		}
	}

	// Metadata
	b.WriteString("├" + hr + "┤\n")
	b.WriteString(fitBoxLine(fmt.Sprintf("│ ★ %d stars   ⑂ %d forks   Pushed %s",
		sf.Fork.Stars, sf.Fork.SubForkCount, relativeTimeSince(sf.Fork.PushedAt)), boxWidth) + "\n")

	if sf.T2 != nil {
		t2 := sf.T2
		totalAdds, totalDels := 0, 0
		for _, d := range t2.Diffs {
			totalAdds += d.Additions
			totalDels += d.Deletions
		}
		b.WriteString(fitBoxLine(fmt.Sprintf("│ Ahead: %d (+%d/-%d)  Behind: %d  Files: %d",
			t2.AheadCount, totalAdds, totalDels, t2.BehindCount, len(t2.Diffs)), boxWidth) + "\n")
		b.WriteString(fitBoxLine(fmt.Sprintf("│ Authors: %d", len(forge.UniqueAuthors(t2.Commits))), boxWidth) + "\n")
	}

	// Bottom badges
	var bottomBadges []string
	if sf.Fork.OpenPRCount > 0 {
		bottomBadges = append(bottomBadges, "📬 Has open PR to upstream")
	}
	if sf.Fork.ReleaseCount > 0 {
		bottomBadges = append(bottomBadges, fmt.Sprintf("🏷️  %d release(s)", sf.Fork.ReleaseCount))
	}
	if sf.Fork.SubForkCount > 0 {
		bottomBadges = append(bottomBadges, "⛓ Fork of fork")
	}
	if len(bottomBadges) > 0 {
		b.WriteString(fitBoxLine("│", boxWidth) + "\n")
		for _, badge := range bottomBadges {
			b.WriteString(fitBoxLine("│ "+badge, boxWidth) + "\n")
		}
	}

	if sf.Fork.Description != "" {
		b.WriteString(fitBoxLine("│", boxWidth) + "\n")
		// Truncate by display cells, then quote: %q on an already-truncated
		// string keeps the closing quote inside the box.
		desc := sf.Fork.Description
		if lipgloss.Width(desc) > boxWidth-6 {
			desc = ansi.Truncate(desc, boxWidth-9, "") + "..."
		}
		b.WriteString(fitBoxLine(fmt.Sprintf("│ %q", desc), boxWidth) + "\n")
	}

	b.WriteString("╰" + hr + "╯")

	return b.String()
}

func componentDescription(name string, raw, points, max float64) string {
	switch name {
	case "recency":
		return fmt.Sprintf("Recency (%.0f days ago) — %.1f/%.0f pts", raw, points, max)
	case "stars":
		return fmt.Sprintf("Stars (%s) — %.1f/%.0f pts", forge.FormatStars(int(raw)), points, max)
	case "sub_forks":
		return fmt.Sprintf("Sub-forks (%.0f) — %.1f/%.0f pts", raw, points, max)
	case "releases":
		return fmt.Sprintf("Releases (%.0f) — %.1f/%.0f pts", raw, points, max)
	case "mna":
		return fmt.Sprintf("Meaningful code (%.0f MNA) — %.1f/%.0f pts", raw, points, max)
	case "sync_ratio":
		return fmt.Sprintf("Sync with upstream (%.0f%%) — %.1f/%.0f pts", raw*100, points, max)
	case "feature_ratio":
		return fmt.Sprintf("Feature commits (%.0f%%) — %.1f/%.0f pts", raw*100, points, max)
	case "lone_wolf":
		return fmt.Sprintf("Lone wolf (strength %.0f%%) — %.1f/%.0f pts", raw*100, points, max)
	case "span":
		return fmt.Sprintf("Commit span (%.0f days) — %.1f/%.0f pts", raw, points, max)
	default:
		return fmt.Sprintf("%s — %.1f/%.0f pts", name, points, max)
	}
}

func penaltyDescription(name string) string {
	switch name {
	case "no_ahead":
		return "No commits ahead — score zeroed"
	case "archived":
		return "Archived — score capped at 30"
	case "low_recency":
		return "Low recency — score reduced 30%"
	default:
		return name + " penalty applied"
	}
}

func pad(n int, ch string) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(ch, n)
}

// collectClusterPeers returns the fork IDs of other members of the given
// cluster, sorted by Heat.Score descending. The caller's own fork (by
// ID) is excluded.
func (m Model) collectClusterPeers(clusterID, selfID string) []string {
	type sib struct {
		id    string
		score float64
	}
	var sibs []sib
	for i := range m.forks {
		if m.forks[i].Heat.ClusterID != clusterID {
			continue
		}
		if m.forks[i].Fork.ID == selfID {
			continue
		}
		sibs = append(sibs, sib{
			id:    m.forks[i].Fork.ID,
			score: m.forks[i].Heat.Score,
		})
	}
	sort.SliceStable(sibs, func(i, j int) bool {
		return sibs[i].score > sibs[j].score
	})
	out := make([]string, 0, len(sibs))
	for _, s := range sibs {
		out = append(out, s.id)
	}
	return out
}

// fitBoxLine pads or truncates a line so it fits inside the detail box
// of width boxWidth, and appends the closing "│". The input string is
// expected to start with the opening "│ ".
//
// Width is computed in runes via utf8.RuneCountInString, and truncation
// happens on rune boundaries, so multi-byte box-drawing characters
// (e.g. "│") and other non-ASCII runes are handled correctly. Note that
// "rune count" is not the same as terminal display width — CJK and wide
// emoji will under-count, but the detail view does not contain such
// content, so rune count is sufficient here.
func fitBoxLine(line string, boxWidth int) string {
	// boxWidth is the total width of the box including borders, so the
	// content area between the two "│" characters is boxWidth-2 wide.
	// The closing "│" is appended at column boxWidth-1 (0-indexed) so
	// the line as-is must occupy boxWidth-1 columns (runes) before the
	// trailing "│" is added.
	target := boxWidth - 1
	// Terminal cells, not runes or bytes: 🔥 and ★ are one rune but two cells,
	// and a lipgloss-styled string carries ANSI sequences that occupy no cells
	// at all. Measuring with len() or utf8.RuneCountInString put the closing
	// border in the wrong column for any line containing either.
	width := lipgloss.Width(line)
	if width > target {
		// ansi.Truncate is width-aware and will not cut a rune in half or
		// strand an unterminated escape sequence.
		line = ansi.Truncate(line, target, "")
		width = lipgloss.Width(line)
	}
	return line + pad(target-width, " ") + "│"
}
