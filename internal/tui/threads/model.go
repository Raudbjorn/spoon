package threads

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cli/browser"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

// Model is the bubbletea model for the threads view.
type Model struct {
	client   *gh.Client
	owner    string
	repo     string
	number   int
	prStatus gh.PullRequestStatus
	threads  []gh.ReviewThread
	loaded   bool
	cursor   int
	err      error
	width    int
	height   int

	composing  bool
	composeBuf []rune
	composeFor string // "reply" or "resolve"
	mutating   bool   // true while a mutation command is in flight
	confirm    string // non-empty while waiting for y/n on a bulk action: "resolve-all" or "unresolve-all"
	status     string // last status line

	includeResolved bool
	filter          threadsops.FilterMode
	showHelp        bool

	pendingSuggestion threadsops.Suggestion // set while m.confirm == "apply-suggestion"

	// ShowCodeLines, when > 0, triggers a code-context fetch per thread on
	// load and renders the result in the detail pane. Set by the caller via
	// the exported field; zero (default) disables the fetch entirely.
	ShowCodeLines int

	// Verbose, when true, surfaces per-comment timestamps in the detail pane
	// (matching gh-pr-display --verbose). Default off keeps the compact view.
	Verbose bool

	// codeContexts caches per-thread CodeContext blocks indexed by thread ID.
	// Populated by codeContextLoadedMsg events; consumed by view rendering.
	codeContexts map[string]*threadsops.CodeContext

	// launchEditor is overridable for tests. Production default is
	// defaultEditorLauncher, which opens $EDITOR (or $VISUAL or vi) with a
	// temp file and returns the edited content.
	launchEditor func(ctx context.Context, threadID, prRef string) (string, error)

	// replyFunc is overridable for tests. Production default calls
	// m.client.ReplyToThread. Replaced in unit tests so no real HTTP is made.
	replyFunc func(ctx context.Context, threadID, body string) (gh.ThreadComment, error)
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
	m := Model{
		client:          client,
		owner:           owner,
		repo:            repo,
		number:          number,
		includeResolved: mode.NeedsResolvedFetch(),
		filter:          mode,
		// launchEditor is intentionally left nil in production. The counter-propose
		// flow uses tea.ExecProcess directly. Tests set launchEditor to a non-nil
		// stub to bypass the real editor invocation (Option B test seam).
	}
	m.replyFunc = func(ctx context.Context, threadID, body string) (gh.ThreadComment, error) {
		return m.client.ReplyToThread(ctx, threadID, body)
	}
	return m
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
		// When --show-code is in play, schedule per-thread fetches. Fire
		// them off as a batch of commands so the TUI stays responsive.
		if m.ShowCodeLines > 0 && m.client != nil && len(filtered) > 0 {
			m.codeContexts = map[string]*threadsops.CodeContext{}
			cmds := make([]tea.Cmd, 0, len(filtered))
			for _, t := range filtered {
				cmds = append(cmds, m.codeContextCmd(t, msg.status.HeadSHA))
			}
			return m, tea.Batch(cmds...)
		}
		return m, nil
	case codeContextLoadedMsg:
		if msg.err != nil {
			m.status = "code context fetch failed: " + msg.err.Error()
			return m, nil
		}
		if m.codeContexts == nil {
			m.codeContexts = map[string]*threadsops.CodeContext{}
		}
		if msg.cc != nil {
			m.codeContexts[msg.threadID] = msg.cc
		}
		return m, nil
	case counterProposeResultMsg:
		switch {
		case msg.cancelled:
			m.status = "counter-propose cancelled"
		case msg.err != nil:
			m.status = "counter-propose failed: " + msg.err.Error()
		default:
			m.status = "counter-propose posted (comment " + msg.commentID + ")"
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
				case "apply-suggestion":
					return m, m.applySuggestionCmd(m.pendingSuggestion)
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
		case actApplySuggestion:
			if m.mutating || m.confirm != "" {
				return m, nil
			}
			if len(m.threads) == 0 {
				return m, nil
			}
			cur := m.threads[m.cursor]
			// Find the first suggestion across all comments.
			var sug *threadsops.Suggestion
			for _, c := range cur.Comments {
				for _, s := range threadsops.ParseSuggestions(c.ID, c.Body) {
					s := s
					sug = &s
					break
				}
				if sug != nil {
					break
				}
			}
			if sug == nil {
				// No suggestion on this thread — show a status message and do nothing.
				m.status = "no suggestion on this thread"
				return m, nil
			}
			m.confirm = "apply-suggestion"
			m.pendingSuggestion = *sug
		case actCounterPropose:
			if m.mutating || m.confirm != "" {
				return m, nil
			}
			if len(m.threads) == 0 {
				return m, nil
			}
			threadID := m.threads[m.cursor].ID
			prRef := fmt.Sprintf("%s/%s#%d", m.owner, m.repo, m.number)

			// Test seam: if launchEditor is set (non-nil), use the legacy all-in-one
			// path so tests can inject a fake body without needing tea.ExecProcess.
			if m.launchEditor != nil {
				launch := m.launchEditor
				reply := m.replyFunc
				return m, func() tea.Msg {
					body, err := launch(context.Background(), threadID, prRef)
					if err != nil {
						return counterProposeResultMsg{err: err, threadID: threadID}
					}
					if body == "" {
						return counterProposeResultMsg{cancelled: true, threadID: threadID}
					}
					wrapped := threadsops.WrapSuggestionBody("", body)
					comment, replyErr := reply(context.Background(), threadID, wrapped)
					if replyErr != nil {
						return counterProposeResultMsg{err: replyErr, threadID: threadID}
					}
					return counterProposeResultMsg{commentID: comment.ID, threadID: threadID}
				}
			}

			// Production path: use tea.ExecProcess so the TUI suspends cleanly
			// while the editor runs, avoiding terminal contention with the render loop.

			// Step 1: prepare temp file synchronously (fast, no terminal I/O).
			tmpPath, prepErr := m.prepareEditorFile(threadID, prRef)
			if prepErr != nil {
				return m, func() tea.Msg {
					return counterProposeResultMsg{err: prepErr, threadID: threadID}
				}
			}

			// Step 2: resolve the editor binary. EDITOR / VISUAL frequently
			// embed flags ("code --wait", "nvim -f"), so split on whitespace
			// and treat the first token as the binary, the rest as args.
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = os.Getenv("VISUAL")
			}
			if editor == "" {
				editor = "vi"
			}
			editorParts := strings.Fields(editor)
			execCmd := exec.Command(editorParts[0], append(editorParts[1:], tmpPath)...)

			// Step 3: return tea.ExecProcess which suspends the TUI, runs the editor,
			// then dispatches the returned tea.Msg when the editor exits.
			reply := m.replyFunc
			return m, tea.ExecProcess(execCmd, func(editorErr error) tea.Msg {
				if editorErr != nil {
					// Preserve the temp file on error so the user can recover work.
					return counterProposeResultMsg{
						err:      fmt.Errorf("editor: %w (work preserved at %s)", editorErr, tmpPath),
						threadID: threadID,
					}
				}
				body, readErr := readAndStripEditorOutput(tmpPath)
				if readErr != nil {
					// Preserve the temp file on read error as well.
					return counterProposeResultMsg{
						err:      fmt.Errorf("read editor output: %w (work preserved at %s)", readErr, tmpPath),
						threadID: threadID,
					}
				}
				// Clean up the temp file only on success or clean cancel.
				os.Remove(tmpPath)
				if body == "" {
					return counterProposeResultMsg{cancelled: true, threadID: threadID}
				}
				wrapped := threadsops.WrapSuggestionBody("", body)
				comment, opErr := reply(context.Background(), threadID, wrapped)
				if opErr != nil {
					return counterProposeResultMsg{err: opErr, threadID: threadID}
				}
				return counterProposeResultMsg{commentID: comment.ID, threadID: threadID}
			})
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

// counterProposeResultMsg is delivered after the editor-based counter-propose
// flow completes (successfully, cancelled, or with error).
type counterProposeResultMsg struct {
	threadID  string
	commentID string
	cancelled bool
	err       error
}

// codeContextLoadedMsg delivers a single per-thread code-context fetch result.
type codeContextLoadedMsg struct {
	threadID string
	cc       *threadsops.CodeContext
	err      error
}

// codeContextCmd issues a single code-context fetch for one thread.
func (m Model) codeContextCmd(t threadsops.ReviewThreadWithPolicy, headSHA string) tea.Cmd {
	return func() tea.Msg {
		cc, err := threadsops.FetchCodeContext(context.Background(), m.client, headSHA, m.owner, m.repo, t, m.ShowCodeLines)
		return codeContextLoadedMsg{threadID: t.ID, cc: cc, err: err}
	}
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

func (m Model) applySuggestionCmd(sug threadsops.Suggestion) tea.Cmd {
	return func() tea.Msg {
		if m.cursor >= len(m.threads) {
			return mutationDoneMsg{what: "apply-suggestion", err: fmt.Errorf("no thread selected")}
		}
		t := threadsops.ReviewThreadWithPolicy{ReviewThread: m.threads[m.cursor]}
		_, opErr := threadsops.ApplySuggestion(context.Background(), t, sug, threadsops.ApplyOptions{})
		if opErr != nil {
			return mutationDoneMsg{what: "apply-suggestion", err: fmt.Errorf("%s", opErr.Message)}
		}
		return mutationDoneMsg{what: "apply-suggestion", err: nil}
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

// prepareEditorFile creates a temp file with a 3-line comment header and
// returns the path. Used as the synchronous first half of the counter-propose
// flow; the second half runs after tea.ExecProcess returns.
func (m Model) prepareEditorFile(threadID, prRef string) (string, error) {
	f, err := os.CreateTemp("", "spoon-counter-propose-*.md")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()
	tmpPath := f.Name()
	header := fmt.Sprintf("# Counter-propose for thread %s on %s\n# Lines beginning with # are stripped.\n# Empty content cancels.\n", threadID, prRef)
	if _, err := f.WriteString(header); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("write header: %w", err)
	}
	return tmpPath, nil
}

// readAndStripEditorOutput reads the temp file at tmpPath, strips '#'-prefixed
// lines and trailing whitespace, and returns the cleaned body. The caller is
// responsible for deleting the file.
func readAndStripEditorOutput(tmpPath string) (string, error) {
	body, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(body), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimRight(strings.Join(out, "\n"), " \t\n"), nil
}
