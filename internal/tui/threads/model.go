package threads

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cli/browser"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

// Model is the bubbletea model for the threads view.
type Model struct {
	client  *gh.Client
	owner   string
	repo    string
	number  int
	prStatus gh.PullRequestStatus
	threads  []gh.ReviewThread
	loaded   bool
	cursor  int
	err     error
	width   int
	height  int

	composing  bool
	composeBuf []rune
	composeFor string // "reply" or "resolve"
	mutating   bool   // true while a mutation command is in flight
	confirm    string // non-empty while waiting for y/n on a bulk action: "resolve-all" or "unresolve-all"
	status     string // last status line

	includeResolved bool
	filter          threadsops.FilterMode
	showHelp        bool
}

// New constructs an empty Model. The includeResolved flag is mapped to a
// FilterMode (`all` when true, `unresolved` when false). For finer-grained
// filtering use NewWithFilter.
func New(client *gh.Client, owner, repo string, number int, includeResolved bool) Model {
	mode := threadsops.FilterUnresolved
	if includeResolved {
		mode = threadsops.FilterAll
	}
	return NewWithFilter(client, owner, repo, number, mode)
}

// NewWithFilter constructs an empty Model with an explicit FilterMode. The
// model fetches the broadest required state from GitHub and applies the
// FilterMode locally so the visible thread list always matches `mode`.
func NewWithFilter(client *gh.Client, owner, repo string, number int, mode threadsops.FilterMode) Model {
	return Model{
		client:          client,
		owner:           owner,
		repo:            repo,
		number:          number,
		includeResolved: mode.NeedsResolvedFetch(),
		filter:          mode,
	}
}

// loadedMsg is delivered when FetchPR completes.
type loadedMsg struct {
	status  gh.PullRequestStatus
	threads []gh.ReviewThread
	err     error
}

// loadCmd fetches threads (unresolved by default, or all if includeResolved is set).
func (m Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		state := gh.ThreadStateUnresolved
		if m.includeResolved {
			state = gh.ThreadStateAll
		}
		status, ts, err := m.client.FetchPR(context.Background(), m.owner, m.repo, m.number, state)
		return loadedMsg{status: status, threads: ts, err: err}
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
		m.prStatus = msg.status
		// Apply the FilterMode so the TUI never displays threads the user
		// asked to hide. The default (empty FilterMode) is treated as
		// FilterUnresolved by includeThread, matching legacy behavior.
		mode := m.filter
		if mode == "" {
			mode = threadsops.FilterUnresolved
			if m.includeResolved {
				mode = threadsops.FilterAll
			}
		}
		annotated := threadsops.AnnotateWithPolicy(msg.threads)
		filtered := threadsops.Filter(annotated, mode)
		threadsops.SortThreadsForList(filtered)
		m.threads = make([]gh.ReviewThread, len(filtered))
		for i, t := range filtered {
			m.threads[i] = t.ReviewThread
		}
		m.err = msg.err
		if m.cursor >= len(m.threads) {
			m.cursor = max(0, len(m.threads)-1)
		}
		return m, nil
	case mutationDoneMsg:
		m.mutating = false
		if msg.err != nil {
			m.status = "❌ error: " + msg.err.Error()
		} else {
			m.status = "✅ " + msg.what + " ok"
		}
		// Refresh the thread list (skip for browser open — it's fire-and-forget).
		if msg.what == "open" {
			return m, nil
		}
		return m, m.loadCmd()
	case tea.KeyMsg:
		if m.confirm != "" {
			// Awaiting y/n on a bulk action.
			s := msg.String()
			pending := m.confirm
			m.confirm = ""
			if s == "y" || s == "Y" {
				m.mutating = true
				switch pending {
				case "resolve-all":
					return m, m.resolveAllCmd()
				case "unresolve-all":
					return m, m.unresolveAllCmd()
				}
			}
			// Anything else cancels.
			return m, nil
		}
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
				m.mutating = true
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
			if m.mutating {
				return m, nil
			}
			if len(m.threads) > 0 {
				m.composing = true
				m.composeFor = "reply"
				m.composeBuf = nil
			}
		case actResolve:
			if m.mutating {
				return m, nil
			}
			if len(m.threads) > 0 {
				cur := m.threads[m.cursor]
				if cur.RequiresBody() {
					m.composing = true
					m.composeFor = "resolve"
					m.composeBuf = nil
				} else {
					m.mutating = true
					return m, m.resolveCmd(cur.ID)
				}
			}
		case actResolveAll:
			if m.mutating || m.confirm != "" {
				return m, nil
			}
			m.confirm = "resolve-all"
		case actUnresolveAll:
			if m.mutating || m.confirm != "" {
				return m, nil
			}
			m.confirm = "unresolve-all"
		case actOpen:
			if len(m.threads) > 0 {
				return m, m.openCmd()
			}
		case actHelp:
			m.showHelp = !m.showHelp
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

func (m Model) openCmd() tea.Cmd {
	url := fmt.Sprintf("https://github.com/%s/%s/pull/%d", m.owner, m.repo, m.number)
	return func() tea.Msg {
		err := browser.OpenURL(url)
		return mutationDoneMsg{what: "open", err: err}
	}
}

func (m Model) View() string {
	return renderModel(m)
}
