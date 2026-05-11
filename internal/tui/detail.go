package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/forge"
)

func (m Model) viewDetail() string {
	if m.cursor < 0 || m.cursor >= len(m.forks) {
		return "\n  No fork selected.\n"
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
	b.WriteString("│ " + lipgloss.NewStyle().Bold(true).Render("Fork: "+sf.Fork.ID) + pad(boxWidth-8-len(sf.Fork.ID), " ") + " │\n")

	// Heat bar
	scoreColor := HeatColor(sf.Heat.Score)
	scoreStr := lipgloss.NewStyle().Foreground(scoreColor).Bold(true).Render(fmt.Sprintf("%.0f/100", sf.Heat.Score))
	b.WriteString(fmt.Sprintf("│ Heat: %s %s  %s", RenderHeatBar(sf.Heat.Score), scoreStr,
		pad(boxWidth-30, " ")+"│\n"))

	b.WriteString("├" + hr + "┤\n")

	// Why it's hot
	b.WriteString("│ 🔥 Why it's hot:" + pad(boxWidth-20, " ") + " │\n")

	// Component breakdown
	if len(sf.Heat.Components) > 0 {
		for _, c := range sf.Heat.Components {
			if c.Points < 0.5 {
				continue
			}
			pct := c.Points / c.Max
			arrow := "→"
			if pct > 0.75 {
				arrow = "↑"
			} else if pct < 0.25 {
				arrow = "↓"
			}
			desc := componentDescription(c.Name, c.Raw, c.Points, c.Max)
			line := fmt.Sprintf("│  %s %-48s │", arrow, desc)
			if len(line) > boxWidth+2 {
				line = line[:boxWidth+1] + "│"
			}
			b.WriteString(line + "\n")
		}
	} else if len(sf.Heat.Signals) > 0 {
		// Legacy signal breakdown
		for _, sig := range sf.Heat.Signals {
			contribution := sig.Value * sig.Weight * 100
			if contribution < 0.5 {
				continue
			}
			desc := fmt.Sprintf("%-15s +%.0f", sig.Name, contribution)
			b.WriteString(fmt.Sprintf("│  → %-48s │\n", desc))
		}
	}

	// Penalties
	if len(sf.Heat.Penalties) > 0 {
		for _, p := range sf.Heat.Penalties {
			desc := penaltyDescription(p)
			b.WriteString(fmt.Sprintf("│  ↓ %-48s │\n", desc))
		}
	}

	// Lone wolf section
	lwV2 := sf.Heat.LoneWolfV2
	lwV1 := sf.Heat.LoneWolf
	if (lwV2 != nil && lwV2.Detected) || (lwV1 != nil && lwV1.Detected) {
		b.WriteString("├" + hr + "┤\n")

		if lwV2 != nil && lwV2.Detected {
			archLabel := lwV2.Label
			if lwV2.Archetype.String() != "" {
				archLabel = lwV2.Archetype.String()
			}
			wolfStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true)
			b.WriteString(fmt.Sprintf("│ %s", wolfStyle.Render("🐺 The "+archLabel)))
			b.WriteString(pad(boxWidth-8-len(archLabel), " ") + " │\n")
			b.WriteString(fmt.Sprintf("│  Solo dev, active over %.0f days%s │\n",
				lwV2.CommitSpanDays, pad(boxWidth-32-len(fmt.Sprintf("%.0f", lwV2.CommitSpanDays)), " ")))
			b.WriteString(fmt.Sprintf("│  %d commits · MNA %d%s │\n",
				lwV2.MeaningfulCommits, lwV2.MNA,
				pad(boxWidth-22-len(fmt.Sprintf("%d", lwV2.MeaningfulCommits))-len(fmt.Sprintf("%d", lwV2.MNA)), " ")))
		} else if lwV1 != nil && lwV1.Detected {
			wolfStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true)
			b.WriteString(fmt.Sprintf("│ %s (strength %.0f%%)\n",
				wolfStyle.Render("["+lwV1.Label+"]"), lwV1.Strength*100))
		}
	}

	// Branch info
	if sf.T2 != nil && sf.T2.IsBranchWork && sf.T2.ActiveBranch != sf.Fork.DefaultBranch {
		b.WriteString("├" + hr + "┤\n")
		b.WriteString(fmt.Sprintf("│ ⚠  Work is on branch: %-28s │\n", sf.T2.ActiveBranch))
		b.WriteString(fmt.Sprintf("│    [y] Yank clone & checkout command%-14s │\n", ""))
	}

	// Cluster info (only present after a successful cluster pipeline run).
	if sf.Heat.ClusterID != "" {
		b.WriteString("├" + hr + "┤\n")
		label := sf.Heat.ClusterLabel
		if label == "" {
			label = sf.Heat.ClusterID
		}
		// "noise" gets a minimal block — no label noise, no siblings.
		isNoise := sf.Heat.ClusterID == "noise"

		line := fmt.Sprintf("│ Cluster: %s", label)
		if !isNoise && sf.Heat.ClusterMemberCount > 0 {
			line += fmt.Sprintf(" (%d members)", sf.Heat.ClusterMemberCount)
		}
		b.WriteString(fitBoxLine(line, boxWidth) + "\n")

		if sf.Heat.NoveltyScore > 0 {
			nl := fmt.Sprintf("│  Novelty: %.2f", sf.Heat.NoveltyScore)
			b.WriteString(fitBoxLine(nl, boxWidth) + "\n")
		}
		if sf.Heat.ChangeImpact > 0 {
			ci := fmt.Sprintf("│  ChangeImpact: %.2f", sf.Heat.ChangeImpact)
			b.WriteString(fitBoxLine(ci, boxWidth) + "\n")
		}

		if !isNoise {
			siblings := m.collectSiblings(sf.Heat.ClusterID, sf.Fork.ID)
			if len(siblings) > 0 {
				const maxShown = 5
				header := fmt.Sprintf("│  Siblings (%d):", len(siblings))
				b.WriteString(fitBoxLine(header, boxWidth) + "\n")
				shown := siblings
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
	b.WriteString(fmt.Sprintf("│ ★ %d stars   ⑂ %d forks   Pushed %s",
		sf.Fork.Stars, sf.Fork.SubForkCount, relativeTimeSince(sf.Fork.PushedAt)))
	b.WriteString(pad(boxWidth-45, " ") + " │\n")

	if sf.T2 != nil {
		t2 := sf.T2
		totalAdds, totalDels := 0, 0
		for _, d := range t2.Diffs {
			totalAdds += d.Additions
			totalDels += d.Deletions
		}
		b.WriteString(fmt.Sprintf("│ Ahead: %d (+%d/-%d)  Behind: %d  Files: %d",
			t2.AheadCount, totalAdds, totalDels, t2.BehindCount, len(t2.Diffs)))
		b.WriteString(pad(boxWidth-50, " ") + " │\n")
		b.WriteString(fmt.Sprintf("│ Authors: %d", len(forge.UniqueAuthors(t2.Commits))))
		b.WriteString(pad(boxWidth-14, " ") + " │\n")
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
		b.WriteString("│" + pad(boxWidth-1, " ") + "│\n")
		for _, badge := range bottomBadges {
			b.WriteString(fmt.Sprintf("│ %s", badge))
			b.WriteString(pad(boxWidth-4-len(badge), " ") + " │\n")
		}
	}

	if sf.Fork.Description != "" {
		b.WriteString("│" + pad(boxWidth-1, " ") + "│\n")
		desc := sf.Fork.Description
		if len(desc) > boxWidth-6 {
			desc = desc[:boxWidth-9] + "..."
		}
		b.WriteString(fmt.Sprintf("│ %q", desc))
		b.WriteString(pad(boxWidth-4-len(desc), " ") + " │\n")
	}

	b.WriteString("╰" + hr + "╯\n")

	b.WriteString("\n  " + helpStyle.Render("[o] Open in browser  [c] Compare  [y] Yank clone  [b/Esc] Back"))

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

// collectSiblings returns the fork IDs of other members of the given
// cluster, sorted by Heat.Score descending. The caller's own fork (by
// ID) is excluded.
func (m Model) collectSiblings(clusterID, selfID string) []string {
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
// Limitation: width is computed by byte length, not rune width. Multi-byte
// runes (CJK, emoji) will under-count and produce a slightly wider visual
// line than the nominal box width. For v1 this is acceptable; a future
// revision can switch to runewidth.StringWidth + runewidth.Truncate.
func fitBoxLine(line string, boxWidth int) string {
	// boxWidth is the total width of the box including borders, so the
	// content area between the two "│" characters is boxWidth-2 wide.
	// The closing "│" is appended at column boxWidth-1 (0-indexed) so
	// the line as-is must occupy boxWidth-1 columns before the trailing
	// "│" is added.
	target := boxWidth - 1
	if len(line) > target {
		// Truncate, leaving room for the closing border.
		line = line[:target]
	} else {
		line += pad(target-len(line), " ")
	}
	return line + "│"
}
