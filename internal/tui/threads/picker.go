package threads

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	gh "github.com/svnbjrn/spoon/internal/github"
)

// PickerModel is the Bubble Tea model for choosing a PR from an open-PR list.
// Returned from `NewPicker(prs)`; runs as its own tea.Program. After Run() the
// caller inspects Selected()/Cancelled() to decide whether to proceed.
type PickerModel struct {
	prs       []gh.PullRequest
	cursor    int
	selected  *gh.PullRequest // nil until user hits Enter
	cancelled bool            // true if user pressed Esc/Ctrl+C/q
	err       error

	// styling
	cursorStyle    lipgloss.Style
	highlightStyle lipgloss.Style
	headerStyle    lipgloss.Style
	footerStyle    lipgloss.Style
	authorStyle    lipgloss.Style
	timeStyle      lipgloss.Style

	// now is captured at construction so "updated 2d ago" rendering is
	// deterministic in tests.
	now time.Time
}

// NewPicker constructs a PickerModel for the given list of PRs. Caller is
// responsible for fetching the list before invocation; this constructor does
// no I/O.
func NewPicker(prs []gh.PullRequest) PickerModel {
	return PickerModel{
		prs:            prs,
		cursorStyle:    lipgloss.NewStyle().Foreground(lipgloss.Color("205")),
		highlightStyle: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
		headerStyle:    lipgloss.NewStyle().Bold(true),
		footerStyle:    lipgloss.NewStyle().Faint(true),
		authorStyle:    lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
		timeStyle:      lipgloss.NewStyle().Faint(true),
		now:            time.Now(),
	}
}

// Init satisfies tea.Model; no startup I/O is required.
func (m PickerModel) Init() tea.Cmd { return nil }

// Update handles navigation, selection, and cancellation.
func (m PickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.cancelled = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down", "j":
			if m.cursor < len(m.prs)-1 {
				m.cursor++
			}
			return m, nil
		case "enter":
			if len(m.prs) == 0 {
				m.cancelled = true
				return m, tea.Quit
			}
			p := m.prs[m.cursor]
			m.selected = &p
			return m, tea.Quit
		}
	}
	return m, nil
}

// View renders the picker rows + footer (or the empty-list message).
func (m PickerModel) View() string {
	if len(m.prs) == 0 {
		var b strings.Builder
		b.WriteString(m.headerStyle.Render("No open PRs in this repo"))
		b.WriteString("\n\n")
		b.WriteString(m.footerStyle.Render("press q/Esc to cancel"))
		b.WriteString("\n")
		return b.String()
	}

	var b strings.Builder
	b.WriteString(m.headerStyle.Render(fmt.Sprintf("Open PRs (%d) — select one:", len(m.prs))))
	b.WriteString("\n\n")

	for i, p := range m.prs {
		row := fmt.Sprintf("PR #%d: %s (@%s, updated %s)",
			p.Number, p.Title, p.Author, humanizeDuration(m.now.Sub(p.UpdatedAt)))
		if i == m.cursor {
			b.WriteString(m.cursorStyle.Render("> "))
			b.WriteString(m.highlightStyle.Render(row))
		} else {
			b.WriteString("  ")
			b.WriteString(row)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(m.footerStyle.Render("↑/↓ navigate, Enter select, q/Esc cancel"))
	b.WriteString("\n")
	return b.String()
}

// Selected returns a pointer to the chosen PR, or nil if the user cancelled
// or hasn't selected yet.
func (m PickerModel) Selected() *gh.PullRequest { return m.selected }

// Cancelled reports whether the user pressed Esc/Ctrl+C/q.
func (m PickerModel) Cancelled() bool { return m.cancelled }

// humanizeDuration renders a coarse "Nd ago" / "Nh ago" / "Nm ago" / "just now"
// label suitable for the picker row.
func humanizeDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
