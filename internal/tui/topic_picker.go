package tui

// topic_picker.go — the TUI side of topic mode. `spoon topic:NAME` (or
// typing "topic:NAME" at the repo prompt) resolves the best repositories
// representing the GitHub topic and shows them as a picker; Enter loads the
// chosen repo's fork table through the normal fetch flow.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/topics"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"github.com/svnbjrn/spoon/internal/tui/ui"
)

// topicResolvedMsg carries the topic selection (or its failure) back into
// the Update loop.
type topicResolvedMsg struct {
	Topic      string
	Selections []topics.Selection
	Err        error
}

// resolveTopicCmd resolves a topic in the background.
func (m *Model) resolveTopicCmd(topic string) tea.Cmd {
	provider := m.provider
	return func() tea.Msg {
		selections, err := topics.Resolve(m.lifecycleCtx, provider, topic, topics.DefaultRepoCount)
		return topicResolvedMsg{Topic: topic, Selections: selections, Err: err}
	}
}

func (m *Model) handleTopicResolved(msg topicResolvedMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	if msg.Err != nil {
		m.view = viewInput
		m.inputErr = msg.Err.Error()
		return m, nil
	}
	m.topicName = msg.Topic
	m.topicSelections = msg.Selections
	m.topicCursor = 0
	m.view = viewTopicPicker
	return m, nil
}

func (m *Model) handleTopicPickerKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.topicCursor > 0 {
			m.topicCursor--
		}
	case "down", "j":
		if m.topicCursor < len(m.topicSelections)-1 {
			m.topicCursor++
		}
	case "enter":
		if len(m.topicSelections) == 0 {
			return m, nil
		}
		m.input = m.topicSelections[m.topicCursor].FullName
		m.inputCursor = len([]rune(m.input))
		m.view = viewTable
		return m, func() tea.Msg { return startFetchMsg{} }
	case "esc", "q":
		m.view = viewInput
		m.inputErr = ""
	}
	return m, nil
}

func (m Model) viewTopicPicker() string {
	ctx, styles := m.themeContext(), m.styles()
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(ui.Heading(ctx, 1, "  Topic: "+m.topicName+" "+ctx.Glyph(theme.EmDash)+" pick a repository to prospect", ui.ContentWidth(m.width)))
	b.WriteString("\n\n")
	for i, selection := range m.topicSelections {
		cursor := "  "
		line := fmt.Sprintf("%-40s  score %5.1f  %s %-7d  forks %-6d %s",
			selection.FullName, selection.Score, ctx.Glyph(theme.Star), selection.Stars, selection.ForkCount, truncateDesc(selection.Description, 50))
		if i == m.topicCursor {
			cursor = ctx.Glyph(theme.Selected) + " "
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}
		b.WriteString("  " + cursor + line + "\n")
	}
	b.WriteString("\n  ")
	b.WriteString(styles.help.Render(ctx.Glyph(theme.ArrowUp) + "/" + ctx.Glyph(theme.ArrowDown) + " navigate " + ctx.Glyph(theme.Separator) + " Enter prospect forks " + ctx.Glyph(theme.Separator) + " Esc back"))
	b.WriteString("\n")
	return b.String()
}

func truncateDesc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "..."
}
