package threads

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	gh "github.com/svnbjrn/spoon/internal/github"
)

// Model is the bubbletea model for the threads view.
type Model struct {
	client  *gh.Client
	owner   string
	repo    string
	number  int
	threads []gh.ReviewThread
	loaded  bool
	cursor  int
	err     error
	width   int
	height  int
}

// New constructs an empty Model.
func New(client *gh.Client, owner, repo string, number int) Model {
	return Model{
		client: client,
		owner:  owner,
		repo:   repo,
		number: number,
	}
}

// loadedMsg is delivered when ListThreads completes.
type loadedMsg struct {
	threads []gh.ReviewThread
	err     error
}

// loadCmd fetches unresolved threads.
func (m Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		ts, err := m.client.ListThreads(context.Background(), m.owner, m.repo, m.number, gh.ThreadStateUnresolved)
		return loadedMsg{threads: ts, err: err}
	}
}

func (m Model) Init() tea.Cmd {
	return m.loadCmd()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case loadedMsg:
		m.loaded = true
		m.threads = msg.threads
		m.err = msg.err
		if m.cursor >= len(m.threads) {
			m.cursor = max(0, len(m.threads)-1)
		}
		return m, nil
	case tea.KeyMsg:
		switch dispatchKey(msg) {
		case actUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case actDown:
			if m.cursor < len(m.threads)-1 {
				m.cursor++
			}
		case actQuit:
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m Model) View() string {
	return renderModel(m)
}
