package tui

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cli/go-gh/v2/pkg/browser"

	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/heat"
)

// View state
type viewState int

const (
	viewInput viewState = iota
	viewTable
	viewDetail
	viewHelp
)

// ScoredFork holds a fork with its computed heat score.
type ScoredFork struct {
	Fork         gh.ForkInfo
	Heat         heat.HeatResult
	Compare      *gh.CompareResult
	T1Extra      *gh.T1Extra
	ActiveBranch string // non-empty if work found on a side branch
	Marked       bool
	Enriching    bool // currently being enriched
}

// Model is the top-level Bubble Tea model.
type Model struct {
	// State
	view     viewState
	width    int
	height   int
	quitting bool

	// Input
	input    string
	inputErr string
	initRepo string // from CLI arg
	refresh  bool   // bypass cache

	// Auth
	client  *gh.Client
	auth    gh.AuthStatus
	authMsg string

	// Data
	parent   *gh.RepoInfo
	forks    []ScoredFork
	t1Extras map[int64]gh.T1Extra
	loading  bool
	loadMsg  string
	errMsg   string

	// Table state
	cursor  int
	sortCol string
	sortAsc bool

	// Enrichment
	enriching    bool
	enrichDone   int
	enrichTotal  int
	enrichCtx    context.Context
	enrichCancel context.CancelFunc

	// Cache
	cache *gh.CacheEntry

	// Buffered updates for batch rendering
	pendingUpdates []tier2ResultMsg

	// Feedback messages
	clipMsg     string
	clipMsgTime time.Time
	errMsgTime  time.Time
}

// --- Constructor ---

func NewModel(repo string, refresh bool) Model {
	return Model{
		view:     viewInput,
		initRepo: repo,
		refresh:  refresh,
		sortCol:  "heat",
		sortAsc:  false,
	}
}

func (m Model) Init() tea.Cmd {
	return checkAuth
}

func checkAuth() tea.Msg {
	client, status, err := gh.CheckAuth()
	return authCheckedMsg{client: client, status: status, err: err}
}

// --- Update ---

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case authCheckedMsg:
		return m.handleAuthChecked(msg)

	case parentFetchedMsg:
		return m.handleParentFetched(msg)

	case cachedLoadMsg:
		return m.handleCachedLoad(msg)

	case forksFetchedMsg:
		return m.handleForksFetched(msg)

	case tier2ResultMsg:
		m.pendingUpdates = append(m.pendingUpdates, msg)
		return m, nil

	case enrichBatchTickMsg:
		return m.processPendingUpdates()

	case enrichmentDoneMsg:
		m.enriching = false
		return m, nil

	case clipboardMsg:
		if msg.success {
			m.clipMsg = "Copied: " + msg.cmd
		} else {
			m.clipMsg = "[Manual copy] " + msg.cmd
		}
		m.clipMsgTime = time.Now()
		return m, nil

	case exportDoneMsg:
		if msg.err != nil {
			m.errMsg = fmt.Sprintf("Export failed: %s", msg.err)
		} else {
			m.errMsg = fmt.Sprintf("Exported to %s", msg.path)
		}
		m.errMsgTime = time.Now()
		return m, nil
	}

	return m, nil
}

func (m *Model) handleAuthChecked(msg authCheckedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.errMsg = fmt.Sprintf("Failed to initialize: %s", msg.err)
		return m, nil
	}
	m.client = msg.client
	m.auth = msg.status
	if msg.status.Authenticated {
		m.authMsg = fmt.Sprintf("Authenticated (%d/%d requests remaining)",
			msg.status.RateLimit.Remaining, msg.status.RateLimit.Limit)
	} else {
		m.authMsg = "Not authenticated. Run `gh auth login` for 5,000 req/hr (currently 60/hr)."
	}
	if m.initRepo != "" {
		m.input = m.initRepo
		m.initRepo = ""
		return m, m.startFetch()
	}
	return m, nil
}

func (m *Model) handleCachedLoad(msg cachedLoadMsg) (tea.Model, tea.Cmd) {
	m.parent = &msg.parent
	m.cache = msg.cache
	m.t1Extras = msg.extras
	m.loading = false
	m.loadMsg = fmt.Sprintf("Loaded %d forks from cache", len(msg.forks))

	m.scoreForks(msg.forks)
	m.view = viewTable
	m.cursor = 0

	// Apply cached compare data
	if msg.cache != nil && msg.cache.Compares != nil {
		m.applyCachedCompares(msg.cache)
	}

	// Start enrichment for forks without compare data
	cmd := m.startEnrichment()
	return m, cmd
}

func (m *Model) applyCachedCompares(cache *gh.CacheEntry) {
	parentPushed, _ := time.Parse(time.RFC3339, m.parent.PushedAt)
	now := time.Now()

	for i := range m.forks {
		compare, ok := cache.Compares[m.forks[i].Fork.ID]
		if !ok || !cache.CompareValid(m.forks[i].Fork.ID) {
			continue
		}

		m.forks[i].Compare = &compare

		files := make([]heat.FileChange, len(compare.Files))
		for j, f := range compare.Files {
			files[j] = heat.FileChange{
				Filename:  f.Filename,
				Additions: f.Additions,
				Deletions: f.Deletions,
			}
		}
		weightedAdds, _ := heat.WeightedAdditions(files)
		weightedDels, _ := heat.WeightedDeletions(files)

		forkPushed, _ := time.Parse(time.RFC3339, m.forks[i].Fork.PushedAt)
		f := m.forks[i].Fork

		p := heat.Tier2Params{
			Tier1Params: heat.Tier1Params{
				Stars:          f.Stars,
				Forks:          f.Forks,
				OpenIssues:     f.OpenIssues,
				ForkSize:       f.Size,
				ParentSize:     m.parent.Size,
				ForkDesc:       f.Description,
				ParentDesc:     m.parent.Description,
				Archived:       f.Archived,
				PushedAt:       forkPushed,
				ParentPushedAt: parentPushed,
				Now:            now,
			},
			AheadBy:       compare.AheadBy,
			BehindBy:      compare.BehindBy,
			FilesChanged:  len(compare.Files),
			TotalAdds:     int(weightedAdds),
			TotalDels:     int(weightedDels),
			UniqueAuthors: len(gh.UniqueAuthors(compare)),
			Diverged:      compare.Status == "diverged",
		}
		m.forks[i].Heat = heat.ComputeTier2(p)

		commitMsgs := make([]string, 0, len(compare.Commits))
		for _, c := range compare.Commits {
			commitMsgs = append(commitMsgs, c.CommitDet.Message)
		}
		lw := heat.DetectLoneWolf(
			compare.AheadBy,
			len(gh.UniqueAuthors(compare)),
			files,
			commitMsgs,
		)
		if lw != nil && lw.Detected {
			m.forks[i].Heat.LoneWolf = lw
		}
	}
	m.sortForks()
}

func (m *Model) handleParentFetched(msg parentFetchedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.loading = false
		m.errMsg = fmt.Sprintf("Repository not found: %s", msg.err)
		m.view = viewInput
		return m, nil
	}
	m.parent = &msg.parent
	m.loadMsg = fmt.Sprintf("Fetching forks of %s...", m.parent.FullName)
	return m, m.fetchForks()
}

func (m *Model) handleForksFetched(msg forksFetchedMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	if msg.err != nil {
		m.errMsg = fmt.Sprintf("Error fetching forks: %s", msg.err)
		return m, nil
	}

	// Store T1 extras
	m.t1Extras = msg.extras

	// Save to cache
	if m.parent != nil {
		parts := strings.SplitN(m.parent.FullName, "/", 2)
		if len(parts) == 2 {
			_ = gh.SaveForkList(parts[0], parts[1], *m.parent, msg.forks, msg.extras)
		}
	}

	m.scoreForks(msg.forks)
	m.view = viewTable
	m.cursor = 0

	// Start Tier 2 enrichment
	cmd := m.startEnrichment()
	return m, cmd
}

func (m *Model) processPendingUpdates() (tea.Model, tea.Cmd) {
	if len(m.pendingUpdates) == 0 {
		if m.enriching {
			return m, batchTick()
		}
		return m, nil
	}

	parentPushed, _ := time.Parse(time.RFC3339, m.parent.PushedAt)
	now := time.Now()

	for _, update := range m.pendingUpdates {
		m.enrichDone++

		if update.err != nil {
			continue
		}

		// Find the fork and update its score
		for i := range m.forks {
			if m.forks[i].Fork.ID == update.forkID {
				compare := update.compare
				m.forks[i].Compare = &compare
				m.forks[i].Enriching = false

				// Track active branch
				if update.activeBranch != "" {
					m.forks[i].ActiveBranch = update.activeBranch
				}

				// Compute file-type-weighted impact
				files := make([]heat.FileChange, len(compare.Files))
				for j, f := range compare.Files {
					files[j] = heat.FileChange{
						Filename:  f.Filename,
						Additions: f.Additions,
						Deletions: f.Deletions,
					}
				}
				weightedAdds, _ := heat.WeightedAdditions(files)
				weightedDels, _ := heat.WeightedDeletions(files)

				forkPushed, _ := time.Parse(time.RFC3339, m.forks[i].Fork.PushedAt)
				f := m.forks[i].Fork

				p := heat.Tier2Params{
					Tier1Params: heat.Tier1Params{
						Stars:          f.Stars,
						Forks:          f.Forks,
						OpenIssues:     f.OpenIssues,
						ForkSize:       f.Size,
						ParentSize:     m.parent.Size,
						ForkDesc:       f.Description,
						ParentDesc:     m.parent.Description,
						Archived:       f.Archived,
						PushedAt:       forkPushed,
						ParentPushedAt: parentPushed,
						Now:            now,
					},
					AheadBy:       compare.AheadBy,
					BehindBy:      compare.BehindBy,
					FilesChanged:  len(compare.Files),
					TotalAdds:     int(weightedAdds),
					TotalDels:     int(weightedDels),
					UniqueAuthors: len(gh.UniqueAuthors(compare)),
					Diverged:      compare.Status == "diverged",
				}
				m.forks[i].Heat = heat.ComputeTier2(p)

				// Run lone wolf detection
				commitMsgs := make([]string, 0, len(compare.Commits))
				for _, c := range compare.Commits {
					commitMsgs = append(commitMsgs, c.CommitDet.Message)
				}
				lw := heat.DetectLoneWolf(
					compare.AheadBy,
					len(gh.UniqueAuthors(compare)),
					files,
					commitMsgs,
				)
				if lw != nil && lw.Detected {
					m.forks[i].Heat.LoneWolf = lw
				}
				break
			}
		}
	}
	m.pendingUpdates = m.pendingUpdates[:0]

	// Check if enrichment is complete
	if m.enrichDone >= m.enrichTotal && m.enriching {
		m.enriching = false
		return m, nil
	}

	if m.enriching {
		return m, batchTick()
	}
	return m, nil
}

// batchTick returns a command that fires after 150ms for batched UI updates.
func batchTick() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
		return enrichBatchTickMsg{}
	})
}

// --- Key handling ---

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "ctrl+c":
		m.quitting = true
		m.cancelEnrichment()
		return m, tea.Quit
	}

	switch m.view {
	case viewInput:
		return m.handleInputKey(key)
	case viewTable:
		return m.handleTableKey(key)
	case viewDetail:
		return m.handleDetailKey(key)
	case viewHelp:
		if key == "?" || key == "esc" || key == "q" {
			m.view = viewTable
		}
		return m, nil
	}

	return m, nil
}

func (m *Model) handleInputKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		m.inputErr = ""
		m.errMsg = ""
		repo := strings.TrimSpace(m.input)
		if repo == "" || !strings.Contains(repo, "/") {
			m.inputErr = "Enter a valid repository (e.g., golang/go)"
			return m, nil
		}
		return m, m.startFetch()
	case "backspace":
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	case "esc":
		if m.parent != nil {
			m.view = viewTable
		}
	default:
		if len(key) == 1 {
			m.input += key
		}
	}
	return m, nil
}

func (m *Model) handleTableKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		m.quitting = true
		m.cancelEnrichment()
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.forks)-1 {
			m.cursor++
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		if len(m.forks) > 0 {
			m.cursor = len(m.forks) - 1
		}
	case "enter":
		if m.cursor >= 0 && m.cursor < len(m.forks) {
			m.view = viewDetail
		}
	case "n":
		m.view = viewInput
	case "/":
		// TODO: filter mode
		m.view = viewInput
	case "?":
		m.view = viewHelp
	case "s":
		m.cycleSortColumn()
	case "S":
		m.sortAsc = !m.sortAsc
		m.sortForks()
	case "o":
		return m, m.openInBrowser()
	case "c":
		return m, m.openCompare()
	case "y":
		return m, m.yankCloneCommand()
	case "r":
		return m, m.doRefresh()
	case " ":
		if m.cursor >= 0 && m.cursor < len(m.forks) {
			m.forks[m.cursor].Marked = !m.forks[m.cursor].Marked
			if m.cursor < len(m.forks)-1 {
				m.cursor++
			}
		}
	case "e":
		return m, m.exportMarked()
	case "E":
		return m, m.exportAll()
	}
	return m, nil
}

func (m *Model) handleDetailKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "b", "q":
		m.view = viewTable
	case "o":
		return m, m.openInBrowser()
	case "c":
		return m, m.openCompare()
	case "y":
		return m, m.yankCloneCommand()
	}
	return m, nil
}

// --- Fetch logic ---

func (m *Model) startFetch() tea.Cmd {
	m.cancelEnrichment()
	m.loading = true
	m.errMsg = ""
	m.forks = nil
	m.parent = nil
	m.t1Extras = nil

	repo := strings.TrimSpace(m.input)
	parts := strings.SplitN(repo, "/", 2)
	owner, name := parts[0], parts[1]
	m.loadMsg = fmt.Sprintf("Fetching %s/%s...", owner, name)

	client := m.client
	refresh := m.refresh

	return func() tea.Msg {
		// Try cache first
		if !refresh {
			cache := gh.LoadCache(owner, name)
			if cache != nil && cache.ForkListValid() {
				return cachedLoadMsg{
					parent: *cache.Parent,
					forks:  cache.Forks,
					extras: cache.T1Extras,
					cache:  cache,
				}
			}
		}

		parent, err := client.FetchParent(context.Background(), owner, name)
		return parentFetchedMsg{parent: parent, err: err}
	}
}

func (m *Model) fetchForks() tea.Cmd {
	client := m.client
	parent := m.parent
	return func() tea.Msg {
		parts := strings.SplitN(parent.FullName, "/", 2)
		forks, extras, err := client.FetchForksAuto(context.Background(), parts[0], parts[1], nil)
		return forksFetchedMsg{forks: forks, extras: extras, err: err}
	}
}

func (m *Model) doRefresh() tea.Cmd {
	m.refresh = true
	m.cancelEnrichment()
	m.forks = nil
	m.parent = nil
	m.t1Extras = nil
	m.cache = nil
	m.enrichDone = 0
	m.enrichTotal = 0
	m.loading = true
	m.loadMsg = "Refreshing..."
	return m.startFetch()
}

// --- Scoring ---

func (m *Model) scoreForks(forks []gh.ForkInfo) {
	parentPushed, _ := time.Parse(time.RFC3339, m.parent.PushedAt)
	now := time.Now()

	m.forks = make([]ScoredFork, 0, len(forks))
	for _, f := range forks {
		if heat.IsGhostFork(f.PushedAt, m.parent.PushedAt, f.Archived, f.Disabled) {
			continue
		}

		forkPushed, _ := time.Parse(time.RFC3339, f.PushedAt)
		params := heat.Tier1Params{
			Stars:          f.Stars,
			Forks:          f.Forks,
			OpenIssues:     f.OpenIssues,
			ForkSize:       f.Size,
			ParentSize:     m.parent.Size,
			ForkDesc:       f.Description,
			ParentDesc:     m.parent.Description,
			Archived:       f.Archived,
			PushedAt:       forkPushed,
			ParentPushedAt: parentPushed,
			Now:            now,
		}
		result := heat.ComputeTier1(params)

		sf := ScoredFork{Fork: f, Heat: result}

		// Attach T1 extras if available
		if m.t1Extras != nil {
			if extra, ok := m.t1Extras[f.ID]; ok {
				sf.T1Extra = &extra
			}
		}

		m.forks = append(m.forks, sf)
	}
	m.sortForks()
}

// --- Enrichment ---

func (m *Model) startEnrichment() tea.Cmd {
	if m.parent == nil || len(m.forks) == 0 || !m.client.HasBudget() {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.enrichCtx = ctx
	m.enrichCancel = cancel
	m.enriching = true
	m.enrichDone = 0

	maxEnrich := len(m.forks)
	rl := m.client.GetRateLimit()
	if rl.Limit > 0 {
		budget := rl.Remaining - (rl.Limit / 10)
		if budget < maxEnrich {
			maxEnrich = budget
		}
	}
	if maxEnrich <= 0 {
		m.enriching = false
		return nil
	}
	if maxEnrich > len(m.forks) {
		maxEnrich = len(m.forks)
	}

	// Split: top 70% by heat (exploit), random 30% (explore)
	exploitCount := maxEnrich * 7 / 10
	if exploitCount > len(m.forks) {
		exploitCount = len(m.forks)
	}
	exploreCount := maxEnrich - exploitCount

	var toEnrich []int
	for i := 0; i < exploitCount && i < len(m.forks); i++ {
		toEnrich = append(toEnrich, i)
	}
	if exploreCount > 0 && exploitCount < len(m.forks) {
		remaining := make([]int, 0, len(m.forks)-exploitCount)
		for i := exploitCount; i < len(m.forks); i++ {
			remaining = append(remaining, i)
		}
		rand.Shuffle(len(remaining), func(i, j int) {
			remaining[i], remaining[j] = remaining[j], remaining[i]
		})
		for i := 0; i < exploreCount && i < len(remaining); i++ {
			toEnrich = append(toEnrich, remaining[i])
		}
	}

	m.enrichTotal = len(toEnrich)
	for _, idx := range toEnrich {
		m.forks[idx].Enriching = true
	}

	// Concurrency: 10 authed, 2 unauthed
	concurrency := 10
	if !m.client.IsAuthenticated() {
		concurrency = 2
	}

	client := m.client
	parent := m.parent
	cache := m.cache
	refresh := m.refresh
	t1Extras := m.t1Extras
	sem := make(chan struct{}, concurrency)

	cmds := make([]tea.Cmd, 0, len(toEnrich)+1)
	cmds = append(cmds, batchTick())

	parentParts := strings.SplitN(parent.FullName, "/", 2)
	for _, idx := range toEnrich {
		f := m.forks[idx].Fork
		forkID := f.ID
		forkOwner := f.Owner.Login
		forkBranch := f.DefaultBranch

		// Get branch info for this fork
		var branches []gh.BranchInfo
		if t1Extras != nil {
			if extra, ok := t1Extras[forkID]; ok {
				branches = extra.TopBranches
			}
		}

		cmds = append(cmds, func() tea.Msg {
			// Check cache first
			if !refresh && cache != nil && cache.CompareValid(forkID) {
				return tier2ResultMsg{forkID: forkID, compare: cache.Compares[forkID]}
			}

			// Acquire semaphore
			select {
			case <-ctx.Done():
				return tier2ResultMsg{forkID: forkID, err: ctx.Err()}
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			if !client.HasBudget() {
				return tier2ResultMsg{forkID: forkID, err: fmt.Errorf("rate limit exhausted")}
			}

			// Use branch-scanning compare if we have branch info
			var compare gh.CompareResult
			var activeBranch string
			var err error

			if len(branches) > 0 {
				compare, activeBranch, err = client.FetchCompareWithBranchScan(
					ctx, parentParts[0], parent.Name, parent.DefaultBranch,
					gh.ForkInfo{Owner: gh.OwnerInfo{Login: forkOwner}, DefaultBranch: forkBranch, Name: f.Name},
					branches,
				)
			} else {
				compare, err = client.FetchCompare(
					ctx, parentParts[0], parent.Name, parent.DefaultBranch,
					forkOwner, forkBranch,
				)
				activeBranch = forkBranch
			}

			if err == nil && len(parentParts) == 2 {
				_ = gh.SaveCompare(parentParts[0], parentParts[1], forkID, compare)
			}
			return tier2ResultMsg{forkID: forkID, compare: compare, activeBranch: activeBranch, err: err}
		})
	}

	return tea.Batch(cmds...)
}

func (m *Model) cancelEnrichment() {
	if m.enrichCancel != nil {
		m.enrichCancel()
		m.enrichCancel = nil
	}
	m.enriching = false
}

// --- Browser ---

func (m *Model) openInBrowser() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.forks) {
		return nil
	}
	url := m.forks[m.cursor].Fork.HTMLURL
	if url == "" {
		url = "https://github.com/" + m.forks[m.cursor].Fork.FullName
	}
	return func() tea.Msg {
		b := browser.New("", os.Stdout, os.Stderr)
		_ = b.Browse(url)
		return nil
	}
}

func (m *Model) openCompare() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.forks) || m.parent == nil {
		return nil
	}
	fork := m.forks[m.cursor].Fork
	url := fmt.Sprintf("https://github.com/%s/compare/%s...%s:%s",
		m.parent.FullName, m.parent.DefaultBranch, fork.Owner.Login, fork.DefaultBranch)
	return func() tea.Msg {
		b := browser.New("", os.Stdout, os.Stderr)
		_ = b.Browse(url)
		return nil
	}
}

// --- View ---

func (m Model) View() string {
	if m.quitting {
		return ""
	}

	switch m.view {
	case viewInput:
		return m.viewInput()
	case viewTable:
		return m.viewTable()
	case viewDetail:
		return m.viewDetail()
	case viewHelp:
		return m.viewHelp()
	}
	return ""
}

func (m Model) viewInput() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(titleStyle.Render("  spoon"))
	b.WriteString(subtitleStyle.Render(" — find useful forks"))
	b.WriteString("\n\n")

	if m.authMsg != "" {
		if m.auth.Authenticated {
			b.WriteString("  " + subtitleStyle.Render(m.authMsg) + "\n\n")
		} else {
			b.WriteString("  " + warnStyle.Render("! ") + m.authMsg + "\n\n")
		}
	}

	b.WriteString("  Repository: " + m.input)
	b.WriteString("█\n")

	if m.inputErr != "" {
		b.WriteString("  " + errorStyle.Render(m.inputErr) + "\n")
	}
	if m.errMsg != "" {
		b.WriteString("  " + errorStyle.Render(m.errMsg) + "\n")
	}

	if m.loading {
		b.WriteString("\n  " + m.loadMsg + "\n")
	} else {
		b.WriteString("\n  " + helpStyle.Render("Enter a GitHub repository (e.g., golang/go)") + "\n")
		b.WriteString("  " + helpStyle.Render("Press Enter to search, Ctrl+C to quit") + "\n")
	}

	return b.String()
}

func relativeTime(isoTime string) string {
	t, err := time.Parse(time.RFC3339, isoTime)
	if err != nil {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/(24*365)))
	}
}
