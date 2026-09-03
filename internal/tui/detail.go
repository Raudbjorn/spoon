package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"github.com/svnbjrn/spoon/internal/tui/ui"
)

// detailFooterLines reserves the actual wrapped contextual legend below the
// scroll window, so the footer never spills beyond the viewport at 80×24.
func (m Model) detailFooterLines() int {
	return 2 + strings.Count(m.detailLegend(), "\n") + 1
}

// viewDetail renders the selected fork's detail box, scrolled to
// m.detailOffset, with the action hint pinned below the window. The hint
// stays outside the scrolled region deliberately: scrolling "[b/Esc] Back"
// off the top would leave no visible way out of the view.
func (m Model) viewDetail() string {
	if m.cursor < 0 || m.cursor >= len(m.forks) {
		return "\n  No fork selected.\n"
	}
	body := scrollLines(m.detailBody(), m.detailOffset, m.detailViewHeight())
	if m.fullscreen {
		return body
	}
	return body + "\n\n  " + m.detailLegend()
}

// detailViewHeight is how many body lines fit on screen. Zero (no
// WindowSizeMsg yet, as in every model built directly in a test) makes
// scrollLines a pass-through, so the body renders whole.
func (m Model) detailViewHeight() int {
	if m.height <= 0 {
		return 0
	}
	return m.height - m.detailFooterLines()
}

func (m Model) detailLegend() string {
	return ui.KeyLegend(m.themeContext(), ui.ContentWidth(m.width)-2, keymap.MainDetail)
}

func (m Model) detailBody() string {
	if m.cursor < 0 || m.cursor >= len(m.forks) {
		return ""
	}
	sf := m.forks[m.cursor]
	ctx, styles := m.themeContext(), m.styles()
	var parts []ui.BoxPart

	boxWidth := 55
	if m.width > 10 {
		boxWidth = m.width - 6
		if boxWidth > 70 {
			boxWidth = 70
		}
	}
	writeLine := func(content string) {
		parts = append(parts, ui.BoxPart{Text: content})
	}
	divider := func() {
		parts = append(parts, ui.BoxPart{Divider: true})
	}

	writeLine(lipgloss.NewStyle().Bold(true).Render("Fork: " + sf.Fork.ID))

	scoreStr := lipgloss.NewStyle().Foreground(m.heatColor(sf.Heat.Score)).Bold(true).Render(fmt.Sprintf("%.0f/100", sf.Heat.Score))
	writeLine(ui.StatCard(ctx, "heat "+m.renderHeatBar(sf.Heat.Score), scoreStr, true, boxWidth-4))
	divider()

	// Shortlist rank: where this fork sits under uncertainty, relative to the
	// ranked pool (docs/ranking.md). P-score is the probability of beating a
	// random other fork; P(top k) says whether it belongs in a shortlist of
	// that size; the interval is the 95% range of its true rank.
	if r := sf.Rank; r != nil {
		writeLine(styles.warn.Render(fmt.Sprintf("Rank (of %d ranked):", m.shortlistPoolSize())))
		writeLine(fmt.Sprintf(" expected rank %.1f   P-score %.0f%%   P(top %d) %.0f%%", r.ExpectedRank, r.PScore*100, m.shortlistTopK(), r.PTopK*100))
		band := ""
		if r.TieBand {
			band = "   tied with a neighbour"
		}
		writeLine(fmt.Sprintf(" 95%% rank interval %d-%d%s", r.Lo, r.Hi, band))
		divider()
	}

	// The former width-two fire pictograph becomes a text label in both glyph
	// profiles, so string composition can never leave an unrecoverable cell.
	writeLine(styles.warn.Render("Why it is hot:"))

	for _, c := range sf.Heat.Components {
		if c.Points < 0.5 {
			continue
		}
		arrow := ctx.Glyph(theme.ArrowRight)
		if c.Max > 0 {
			pct := c.Points / c.Max
			if pct > 0.75 {
				arrow = ctx.Glyph(theme.ArrowUp)
			} else if pct < 0.25 {
				arrow = ctx.Glyph(theme.ArrowDown)
			}
		}
		writeLine(fmt.Sprintf(" %s %s", arrow, componentDescription(ctx, c.Name, c.Raw, c.Points, c.Max)))
	}

	for _, penalty := range sf.Heat.Penalties {
		writeLine(" " + ctx.Glyph(theme.ArrowDown) + " " + penaltyDescription(ctx, penalty))
	}

	lwV2 := sf.Heat.LoneWolfV2
	if lwV2 != nil && lwV2.Detected {
		divider()
		archLabel := lwV2.Label
		if lwV2.Archetype.String() != "" {
			archLabel = lwV2.Archetype.String()
		}
		writeLine(lipgloss.NewStyle().Foreground(ctx.Palette.AccentRust).Bold(true).Render("Lone wolf: " + archLabel))
		writeLine(fmt.Sprintf(" Solo dev, active over %.0f days", lwV2.CommitSpanDays))
		writeLine(fmt.Sprintf(" %d commits %s MNA %d", lwV2.MeaningfulCommits, ctx.Glyph(theme.Separator), lwV2.MNA))
	}

	if sf.T2 != nil && sf.T2.IsBranchWork && sf.T2.ActiveBranch != sf.Fork.DefaultBranch {
		divider()
		writeLine(styles.warn.Render(ctx.Glyph(theme.Warning) + " Work is on branch: " + sf.T2.ActiveBranch))
		writeLine("   [y] Yank clone & checkout command")
	}

	if sf.Heat.ClusterID != "" {
		divider()
		label := sf.Heat.ClusterLabel
		if label == "" {
			label = sf.Heat.ClusterID
		}
		isNoise := sf.Heat.ClusterID == "noise"
		clusterLine := fmt.Sprintf("Cluster: %s", label)
		if !isNoise && sf.Heat.ClusterMemberCount > 0 {
			clusterLine += fmt.Sprintf(" (%d members)", sf.Heat.ClusterMemberCount)
		}
		writeLine(clusterLine)
		if isNoise {
			writeLine(fmt.Sprintf(" isolation: %.2f/1.0 (cluster noise)", sf.Heat.NoveltyScore))
		} else {
			writeLine(fmt.Sprintf(" isolation: %.2f/1.0 (cluster %s, %d members)", sf.Heat.NoveltyScore, sf.Heat.ClusterID, sf.Heat.ClusterMemberCount))
		}
		if sf.Heat.NoveltyScore > 0 {
			writeLine(fmt.Sprintf(" Novelty: %.2f", sf.Heat.NoveltyScore))
		}
		if sf.Heat.ChangeImpact > 0 {
			writeLine(fmt.Sprintf(" ChangeImpact: %.2f", sf.Heat.ChangeImpact))
		}
		if !isNoise {
			peers := m.collectClusterPeers(sf.Heat.ClusterID, sf.Fork.ID)
			if len(peers) > 0 {
				writeLine(fmt.Sprintf(" Cluster peers (%d):", len(peers)))
				const maxShown = 5
				shown := peers
				extra := 0
				if len(shown) > maxShown {
					extra = len(shown) - maxShown
					shown = shown[:maxShown]
				}
				for _, sibling := range shown {
					writeLine("   " + sibling)
				}
				if extra > 0 {
					writeLine(fmt.Sprintf("   ... and %d more", extra))
				}
			}
		}
	}

	divider()
	writeLine(fmt.Sprintf("%s %d stars   %s %d forks   Pushed %s", ctx.Glyph(theme.Star), sf.Fork.Stars, ctx.Glyph(theme.Fork), sf.Fork.SubForkCount, relativeTimeSince(sf.Fork.PushedAt)))
	if sf.T2 != nil {
		totalAdds, totalDels := 0, 0
		for _, diff := range sf.T2.Diffs {
			totalAdds += diff.Additions
			totalDels += diff.Deletions
		}
		filesLine := fmt.Sprintf("Ahead: %d (+%d/-%d)  Behind: %d  Files: %d", sf.T2.AheadCount, totalAdds, totalDels, sf.T2.BehindCount, len(sf.T2.Diffs))
		if sf.T2.FilesTruncated || len(sf.T2.Diffs) >= forge.CompareFilesCap {
			filesLine += fmt.Sprintf(" (capped at %d)", forge.CompareFilesCap)
		}
		writeLine(filesLine)
		writeLine(fmt.Sprintf("Authors: %d", len(forge.UniqueAuthors(sf.T2.Commits))))
		if sf.T2.FilesTruncated || len(sf.T2.Diffs) >= forge.CompareFilesCap {
			writeLine(styles.warn.Render(fmt.Sprintf(" file list capped at %d by the provider; counts are lower bounds", forge.CompareFilesCap)))
		}
		if m.pathFilter != nil {
			var touched []forge.FileDiff
			for _, d := range sf.T2.Diffs {
				if diffMatches(d, *m.pathFilter) {
					touched = append(touched, d)
				}
			}
			if len(touched) > 0 {
				divider()
				writeLine(styles.warn.Render("Touches " + strings.Join(m.pathFilter.Patterns(), ", ") + ":"))
				const maxShown = 20
				for i, d := range touched {
					if i == maxShown {
						writeLine(fmt.Sprintf("   ... and %d more", len(touched)-maxShown))
						break
					}
					writeLine(fmt.Sprintf(" %s %s (+%d/-%d)", d.Status, d.Path, d.Additions, d.Deletions))
				}
				writeLine("   [p] View patch for these files")
			}
		}
	}

	var badges []string
	if sf.Fork.OpenPRCount > 0 {
		badges = append(badges, "Has open PR to upstream")
	}
	if sf.Fork.ReleaseCount > 0 {
		badges = append(badges, fmt.Sprintf("%d release(s)", sf.Fork.ReleaseCount))
	}
	if sf.Fork.SubForkCount > 0 {
		badges = append(badges, "Fork of fork")
	}
	if len(badges) > 0 {
		writeLine("")
		for _, badge := range badges {
			writeLine(badge)
		}
	}

	if sf.Fork.Description != "" {
		writeLine("")
		desc := sf.Fork.Description
		if lipgloss.Width(desc) > boxWidth-6 {
			desc = ansi.Truncate(desc, boxWidth-9, "") + "..."
		}
		writeLine(fmt.Sprintf("%q", desc))
	}

	return "\n" + ui.Card(ctx, "Fork details", boxWidth, parts)
}

func componentDescription(ctx theme.Context, name string, raw, points, max float64) string {
	switch name {
	case "recency":
		return fmt.Sprintf("Recency (%.0f days ago) %s %.1f/%.0f pts", raw, ctx.Glyph(theme.EmDash), points, max)
	case "stars":
		return fmt.Sprintf("Stars (%s) %s %.1f/%.0f pts", forge.FormatStars(int(raw)), ctx.Glyph(theme.EmDash), points, max)
	case "sub_forks":
		return fmt.Sprintf("Sub-forks (%.0f) %s %.1f/%.0f pts", raw, ctx.Glyph(theme.EmDash), points, max)
	case "releases":
		return fmt.Sprintf("Releases (%.0f) %s %.1f/%.0f pts", raw, ctx.Glyph(theme.EmDash), points, max)
	case "mna":
		return fmt.Sprintf("Meaningful code (%.0f MNA) %s %.1f/%.0f pts", raw, ctx.Glyph(theme.EmDash), points, max)
	case "sync_ratio":
		return fmt.Sprintf("Sync with upstream (%.0f%%) %s %.1f/%.0f pts", raw*100, ctx.Glyph(theme.EmDash), points, max)
	case "feature_ratio":
		return fmt.Sprintf("Feature commits (%.0f%%) %s %.1f/%.0f pts", raw*100, ctx.Glyph(theme.EmDash), points, max)
	case "lone_wolf":
		return fmt.Sprintf("Lone wolf (strength %.0f%%) %s %.1f/%.0f pts", raw*100, ctx.Glyph(theme.EmDash), points, max)
	case "span":
		return fmt.Sprintf("Commit span (%.0f days) %s %.1f/%.0f pts", raw, ctx.Glyph(theme.EmDash), points, max)
	default:
		return fmt.Sprintf("%s %s %.1f/%.0f pts", name, ctx.Glyph(theme.EmDash), points, max)
	}
}

func penaltyDescription(ctx theme.Context, name string) string {
	switch name {
	case "no_ahead":
		return "No commits ahead " + ctx.Glyph(theme.EmDash) + " score zeroed"
	case "archived":
		return "Archived " + ctx.Glyph(theme.EmDash) + " score capped at 30"
	case "low_recency":
		return "Low recency " + ctx.Glyph(theme.EmDash) + " score reduced 30%"
	default:
		return name + " penalty applied"
	}
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
