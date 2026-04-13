package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	gh "github.com/svnbjrn/spoon/internal/github"
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
	b.WriteString("│ " + lipgloss.NewStyle().Bold(true).Render("Fork: "+sf.Fork.FullName) + pad(boxWidth-8-len(sf.Fork.FullName), " ") + " │\n")

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
	if sf.ActiveBranch != "" && sf.ActiveBranch != sf.Fork.DefaultBranch {
		b.WriteString("├" + hr + "┤\n")
		b.WriteString(fmt.Sprintf("│ ⚠  Work is on branch: %-28s │\n", sf.ActiveBranch))
		b.WriteString(fmt.Sprintf("│    [y] Yank clone & checkout command%-14s │\n", ""))
	}

	// Metadata
	b.WriteString("├" + hr + "┤\n")
	b.WriteString(fmt.Sprintf("│ ★ %d stars   ⑂ %d forks   Pushed %s",
		sf.Fork.Stars, sf.Fork.Forks, relativeTime(sf.Fork.PushedAt)))
	b.WriteString(pad(boxWidth-45, " ") + " │\n")

	if sf.Compare != nil {
		c := sf.Compare
		totalAdds, totalDels := 0, 0
		for _, f := range c.Files {
			totalAdds += f.Additions
			totalDels += f.Deletions
		}
		b.WriteString(fmt.Sprintf("│ Ahead: %d (+%d/-%d)  Behind: %d  Files: %d",
			c.AheadBy, totalAdds, totalDels, c.BehindBy, len(c.Files)))
		b.WriteString(pad(boxWidth-50, " ") + " │\n")
		b.WriteString(fmt.Sprintf("│ Authors: %d", len(gh.UniqueAuthors(*c))))
		b.WriteString(pad(boxWidth-14, " ") + " │\n")
	}

	// Bottom badges
	var bottomBadges []string
	if sf.T1Extra != nil && sf.T1Extra.OpenPRCount > 0 {
		bottomBadges = append(bottomBadges, "📬 Has open PR to upstream")
	}
	if sf.T1Extra != nil && sf.T1Extra.ReleaseCount > 0 {
		bottomBadges = append(bottomBadges, fmt.Sprintf("🏷️  %d release(s)", sf.T1Extra.ReleaseCount))
	}
	if sf.Fork.Forks > 0 {
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
		return fmt.Sprintf("Stars (%s) — %.1f/%.0f pts", gh.FormatStars(int(raw)), points, max)
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
