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
		if f.Compare != nil {
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

	for i := start; i < end; i++ {
		sf := m.forks[i]
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

		name := sf.Fork.FullName
		pushed := relativeTime(sf.Fork.PushedAt)

		// Badges
		badges := renderBadges(sf)

		var row string
		if hasCompare {
			if len(name) > 26 {
				name = name[:23] + "..."
			}
			ahead := "  -"
			behind := "  -"
			if sf.Compare != nil {
				ahead = fmt.Sprintf("%5d", sf.Compare.AheadBy)
				behind = fmt.Sprintf("%6d", sf.Compare.BehindBy)
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
				prefix, scorePrefix, heatBar, scoreStyled, name, sf.Fork.Stars, sf.Fork.Forks, pushed)
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
	} else {
		b.WriteString("\n")
	}
	b.WriteString(helpStyle.Render(" ↑↓ navigate  Enter detail  o open  y yank  / filter  s sort  e export  ? help  q quit"))

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
	if sf.T1Extra != nil && sf.T1Extra.OpenPRCount > 0 {
		badges = append(badges, "📬")
	}

	// Fork of fork badge
	if sf.Fork.Forks > 0 {
		badges = append(badges, "⛓")
	}

	// Side branch badge
	if sf.ActiveBranch != "" && sf.ActiveBranch != sf.Fork.DefaultBranch {
		badges = append(badges, "🌱")
	}

	// Releases badge
	if sf.T1Extra != nil && sf.T1Extra.ReleaseCount > 0 {
		badges = append(badges, "🏷️")
	}

	return strings.Join(badges, " ")
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

	if m.client != nil {
		r := m.client.GetRateLimit()
		if r.Limit > 0 {
			parts = append(parts, fmt.Sprintf("API: %d/%d", r.Remaining, r.Limit))
		}
		if !m.client.IsAuthenticated() {
			parts = append(parts, warnStyle.Render("⚠ Unauthenticated"))
		}
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
			less = m.forks[i].Fork.Forks < m.forks[j].Fork.Forks
		case "ahead":
			ai, aj := 0, 0
			if m.forks[i].Compare != nil {
				ai = m.forks[i].Compare.AheadBy
			}
			if m.forks[j].Compare != nil {
				aj = m.forks[j].Compare.AheadBy
			}
			less = ai < aj
		case "pushed":
			less = m.forks[i].Fork.PushedAt < m.forks[j].Fork.PushedAt
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
