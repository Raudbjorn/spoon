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

	composing      bool
	composeBuf     []rune
	composeFor     string // "reply" or "resolve"
	pendingResolve bool   // bot-only resolve queued for the next tick
	status         string // last status line
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
	case mutationDoneMsg:
		m.pendingResolve = false
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
		} else {
			m.status = msg.what + " ok"
		}
		// Refresh the thread list.
		return m, m.loadCmd()
	case tea.KeyMsg:
		if m.composing {
			switch msg.Type {
			case tea.KeyEsc:
				m.composing = false
				m.composeBuf = nil
			case tea.KeyCtrlS:
				body := string(m.composeBuf)
				m.composing = false
				m.composeBuf = nil
				if len(m.threads) == 0 {
					return m, nil
				}
				targetID := m.threads[m.cursor].ID
				switch m.composeFor {
				case "reply":
					return m, m.replyCmd(targetID, body)
				case "resolve":
					return m, m.replyThenResolveCmd(targetID, body)
				}
			case tea.KeyBackspace:
				if len(m.composeBuf) > 0 {
					m.composeBuf = m.composeBuf[:len(m.composeBuf)-1]
				}
			case tea.KeyRunes:
				m.composeBuf = append(m.composeBuf, msg.Runes...)
			case tea.KeyEnter:
				m.composeBuf = append(m.composeBuf, '\n')
			case tea.KeySpace:
				m.composeBuf = append(m.composeBuf, ' ')
			}
			return m, nil
		}
		switch dispatchKey(msg) {
		case actUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case actDown:
			if m.cursor < len(m.threads)-1 {
				m.cursor++
			}
		case actReply:
			if len(m.threads) > 0 {
				m.composing = true
				m.composeFor = "reply"
				m.composeBuf = nil
			}
		case actResolve:
			if len(m.threads) > 0 {
				cur := m.threads[m.cursor]
				if cur.RequiresBody() {
					m.composing = true
					m.composeFor = "resolve"
					m.composeBuf = nil
				} else {
					m.pendingResolve = true
					return m, m.resolveCmd(cur.ID)
				}
			}
		case actResolveAll:
			return m, m.resolveAllCmd()
		case actUnresolveAll:
			return m, m.unresolveAllCmd()
		case actQuit:
			return m, tea.Quit
		}
	}
	return m, nil
}

type mutationDoneMsg struct {
	what string // "reply", "resolve", "bulk-resolve", "bulk-unresolve"
	err  error
}

func (m Model) replyCmd(threadID, body string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.ReplyToThread(context.Background(), threadID, body)
		return mutationDoneMsg{what: "reply", err: err}
	}
}

func (m Model) resolveCmd(threadID string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.ResolveThread(context.Background(), threadID)
		return mutationDoneMsg{what: "resolve", err: err}
	}
}

func (m Model) replyThenResolveCmd(threadID, body string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.ReplyToThread(context.Background(), threadID, body); err != nil {
			return mutationDoneMsg{what: "reply", err: err}
		}
		err := m.client.ResolveThread(context.Background(), threadID)
		return mutationDoneMsg{what: "resolve", err: err}
	}
}

func (m Model) resolveAllCmd() tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.ResolveAllThreads(context.Background(), m.owner, m.repo, m.number, 4)
		return mutationDoneMsg{what: "bulk-resolve", err: err}
	}
}

func (m Model) unresolveAllCmd() tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.UnresolveAllThreads(context.Background(), m.owner, m.repo, m.number, 4)
		return mutationDoneMsg{what: "bulk-unresolve", err: err}
	}
}

func (m Model) View() string {
	return renderModel(m)
}
