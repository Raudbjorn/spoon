package tui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cli/go-gh/v2/pkg/browser"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/store"
	"github.com/svnbjrn/spoon/internal/topics"
)

// View state
type viewState int

const (
	viewInput viewState = iota
	viewTable
	viewDetail
	viewHelp
	viewExportPath
	viewTopicPicker
)

// ScoredFork holds a fork with its computed heat score.
type ScoredFork struct {
	Fork      forge.T1Data
	Heat      heat.HeatResult
	T2        *forge.T2Data
	Marked    bool
	Enriching bool

	// statID is the fork's key in the scorer's percentile table. Assigned at
	// initial scoring; stable across re-sorts of m.forks.
	statID int64

	// Enriched is true once a real T2 compare has settled for this fork (cache
	// hit or live call). BudgetSkipped is true when enrichment was skipped at
	// the rate-limit reserve floor. The two distinguish "compared, genuinely no
	// divergence" from "never compared" — so the export reports an un-enriched
	// fork as such instead of as zero divergence.
	Enriched      bool
	BudgetSkipped bool
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
	provider forge.Forge
	auth     forge.AuthInfo
	authMsg  string

	// Data
	parent      *forge.ParentData
	forks       []ScoredFork
	scorer      *heat.Scorer // v2 scorer, created in scoreForks and reused for T2 rescoring
	heatWeights map[string]float64
	loading     bool
	loadMsg     string
	errMsg      string

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

	// Global store (mandatory at runtime; nil only in unit tests). cached is
	// the repo's stored snapshot, used to serve fork lists within forkListTTL
	// and to reuse per-fork compares whose pushed_at is unchanged.
	db     *store.Store
	cached *store.RepoSnapshot

	// Buffered updates for batch rendering
	pendingUpdates []tier2ResultMsg

	// Feedback messages
	clipMsg     string
	clipMsgTime time.Time
	errMsgTime  time.Time

	// Export path prompt
	exportPath  string       // editable path shown in prompt
	exportForks []ScoredFork // forks staged for export (nil = export all)

	// Cluster pipeline
	// Topic picker state (topic mode).
	topicName       string
	topicSelections []topics.Selection
	topicCursor     int

	clusterOpts       ClusterOptions
	clusterRan        bool         // true after the pipeline has been kicked off
	clusterStatus     string       // "pending", "running", "skipped: <reason>", "done"
	clusterSkipReason string       // human-readable skip reason when clusters were skipped
	clusterMsgs       chan tea.Msg // shared message channel cluster goroutines push onto

	// lifecycleCtx is cancelled when the TUI quits; the cluster message
	// pump (waitForClusterMsg) honors it so its blocked goroutine exits
	// instead of leaking past program shutdown.
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc

	// Cluster view toggle (T11): when true, the table is rendered with a
	// header row per cluster. Toggled via the "g" key.
	groupByCluster bool
}

// --- Constructor ---

func NewModel(provider forge.Forge, auth forge.AuthInfo, repo string, refresh bool) Model {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	return Model{
		view:            viewInput,
		provider:        provider,
		auth:            auth,
		initRepo:        repo,
		refresh:         refresh,
		sortCol:         "heat",
		sortAsc:         false,
		clusterMsgs:     make(chan tea.Msg, 16),
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
	}
}

// NewModelWithCluster constructs a Model with cluster pipeline options. When
// opts.Enabled is false, the TUI runs the existing T1+T2 flow without any
// embed/cluster work.
func NewModelWithCluster(provider forge.Forge, auth forge.AuthInfo, repo string, refresh bool, opts ClusterOptions) Model {
	m := NewModel(provider, auth, repo, refresh)
	m.clusterOpts = opts
	return m
}

// WithHeatWeights returns a copy of the model using the given per-component
// heat weights (see heat.LoadWeights).
func (m Model) WithHeatWeights(w map[string]float64) Model {
	m.heatWeights = w
	return m
}

// WithStore returns a copy of the model backed by the global store. The store
// is mandatory at runtime — the caller (cmd/spoon) fails hard when it cannot
// be opened — but stays nil-able so unit tests can run modelless.
func (m Model) WithStore(db *store.Store) Model {
	m.db = db
	return m
}

// forkListTTL bounds how long a stored fork enumeration serves as the full
// list: membership can change (new forks) with no push to any cached fork, so
// list freshness is time-based, unlike compare validity which is keyed on each
// fork's pushed_at.
const forkListTTL = 12 * time.Hour

func (m Model) Init() tea.Cmd {
	if m.auth.Authenticated() {
		m.authMsg = fmt.Sprintf("Authenticated (%s, %d req/%s)",
			m.auth.Provider.String(), m.auth.RateLimit, m.auth.RateUnit)
	} else {
		m.authMsg = "Not authenticated. Run `gh auth login` for 5,000 req/hr (currently 60/hr)."
	}
	pump := waitForClusterMsg(m.clusterMsgs, m.lifecycleCtx)
	if m.initRepo != "" {
		return tea.Batch(pump, func() tea.Msg {
			return startFetchMsg{}
		})
	}
	return pump
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

	case startFetchMsg:
		cmd := m.startFetch()
		return m, cmd

	case topicResolvedMsg:
		return m.handleTopicResolved(msg)

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

	case clusterResultMsg:
		return m.handleClusterResult(msg)

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

func (m *Model) handleCachedLoad(msg cachedLoadMsg) (tea.Model, tea.Cmd) {
	m.parent = &msg.parent
	m.cached = msg.snap
	m.loading = false
	m.loadMsg = fmt.Sprintf("Loaded %d forks from cache", len(msg.forks))

	m.scoreForks(msg.forks)
	m.view = viewTable
	m.cursor = 0

	// Apply cached compare data
	m.applyCachedCompares(msg.snap)

	cmd := m.startEnrichment()
	if cmd == nil {
		// No T2 enrichment scheduled (e.g. rate-limited). Still try
		// clustering on whatever T1+cached-T2 data we have.
		if cc := m.maybeStartClusterPipeline(); cc != nil {
			return m, cc
		}
	}
	return m, cmd
}

// cachedT2For returns the stored compare for a fork when its pushed_at matches
// the live listing. A push moves pushed_at, so equality means the compare is
// still describing the fork's current state — no TTL involved.
func cachedT2For(snap *store.RepoSnapshot, t1 forge.T1Data) *forge.T2Data {
	if snap == nil {
		return nil
	}
	for i := range snap.Forks {
		cf := &snap.Forks[i]
		if cf.T1.ID == t1.ID && cf.T2 != nil && cf.T1.PushedAt.Equal(t1.PushedAt) {
			return cf.T2
		}
	}
	return nil
}

func (m *Model) applyCachedCompares(snap *store.RepoSnapshot) {
	if snap == nil {
		return
	}
	for i := range m.forks {
		t2 := cachedT2For(snap, m.forks[i].Fork)
		if t2 == nil {
			continue
		}
		m.forks[i].T2 = t2
		m.forks[i].Enriched = true

		m.recomputeT2Score(i)
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
	m.cached = msg.snap
	m.loadMsg = fmt.Sprintf("Fetching forks of %s...", m.parent.FullName)
	return m, m.fetchForks()
}

func (m *Model) handleForksFetched(msg forksFetchedMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	if msg.err != nil {
		m.errMsg = fmt.Sprintf("Error fetching forks: %s", msg.err)
		return m, nil
	}

	if msg.warn != nil {
		m.errMsg = fmt.Sprintf("Warning: fork list may be incomplete: %s", msg.warn)
		m.errMsgTime = time.Now()
	}

	m.scoreForks(msg.forks)
	m.view = viewTable
	m.cursor = 0

	// Reuse stored compares for forks whose pushed_at is unchanged, then
	// persist the freshly scored list.
	m.applyCachedCompares(m.cached)
	persist := m.persistForkList()

	cmd := m.startEnrichment()
	if cmd == nil {
		if cc := m.maybeStartClusterPipeline(); cc != nil {
			return m, tea.Batch(persist, cc)
		}
	}
	if persist != nil {
		return m, tea.Batch(persist, cmd)
	}
	return m, cmd
}

func (m *Model) processPendingUpdates() (tea.Model, tea.Cmd) {
	if len(m.pendingUpdates) == 0 {
		if m.enriching {
			return m, batchTick()
		}
		return m, nil
	}

	for _, update := range m.pendingUpdates {
		m.enrichDone++

		// Rate-reserve skip: keep the fork, mark it un-enriched/degraded (not
		// failed, not zero divergence) so the export can distinguish it.
		if update.budgetSkipped {
			for i := range m.forks {
				if m.forks[i].Fork.ID == update.forkID {
					m.forks[i].Enriching = false
					m.forks[i].BudgetSkipped = true
					break
				}
			}
			continue
		}

		if update.err != nil {
			for i := range m.forks {
				if m.forks[i].Fork.ID == update.forkID {
					m.forks[i].Enriching = false
					break
				}
			}
			continue
		}

		for i := range m.forks {
			if m.forks[i].Fork.ID == update.forkID {
				t2 := update.t2
				m.forks[i].T2 = &t2
				m.forks[i].Enriching = false
				m.forks[i].Enriched = true

				m.recomputeT2Score(i)

				m.persistCompare(i)

				break
			}
		}
	}
	m.pendingUpdates = m.pendingUpdates[:0]

	if m.enrichDone >= m.enrichTotal && m.enriching {
		m.enriching = false
		// T2 streaming finished; kick off cluster pipeline if enabled.
		if cmd := m.maybeStartClusterPipeline(); cmd != nil {
			return m, cmd
		}
		return m, nil
	}

	if m.enriching {
		return m, batchTick()
	}
	return m, nil
}

// recomputeT2Score recalculates the heat score for a fork after T2 data arrives.
func (m *Model) recomputeT2Score(i int) {
	t2 := m.forks[i].T2
	if t2 == nil || m.parent == nil || m.scorer == nil {
		return
	}

	f := m.forks[i].Fork
	now := time.Now()

	input := buildTUIScoreInput(f, *m.parent, now)
	input.T2 = &heat.Tier2ParamsV2{
		MNA:                t2.MNA,
		AheadBy:            t2.AheadCount,
		BehindBy:           t2.BehindCount,
		FeatureCommitRatio: t2.FeatureCommitRatio,
	}

	// Wire v2 lone wolf when we have commits to analyze.
	if len(t2.Commits) > 0 {
		lw := buildTUILoneWolfInput(f, now, t2)
		input.T3 = &heat.Tier3ParamsV2{
			LoneWolf: heat.DetectLoneWolfV2(lw),
		}
	}

	result := m.scorer.ScoreRaw(input)
	if input.T3 != nil && input.T3.LoneWolf != nil {
		result.LoneWolfV2 = input.T3.LoneWolf
	}
	m.scorer.Finalize(&result, m.forks[i].statID, heat.PenaltyInput{
		AheadKnown:       true,
		AheadAllBranches: t2.AheadCount,
		Archived:         f.IsArchived,
	})
	m.forks[i].Heat = result
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
		m.cancelLifecycle()
		return m, tea.Quit
	}

	switch m.view {
	case viewInput:
		return m.handleInputKey(key)
	case viewTable:
		return m.handleTableKey(key)
	case viewDetail:
		return m.handleDetailKey(key)
	case viewExportPath:
		return m.handleExportPathKey(key)
	case viewTopicPicker:
		return m.handleTopicPickerKey(key)
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
		m.cancelLifecycle()
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.forks)-1 {
			m.cursor++
		}
	case "home":
		m.cursor = 0
	case "G", "end":
		if len(m.forks) > 0 {
			m.cursor = len(m.forks) - 1
		}
	case "g":
		m.toggleGroupByCluster()
	case "enter":
		if m.cursor >= 0 && m.cursor < len(m.forks) {
			m.view = viewDetail
		}
	case "n":
		m.view = viewInput
	case "/":
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
		return m, m.promptExportMarked()
	case "E":
		return m, m.promptExportAll()
	}
	return m, nil
}

// toggleGroupByCluster flips the cluster-grouping view toggle. When no
// cluster data is available, the toggle still flips but the renderer
// silently falls back to a flat table; a transient footer note signals
// the absence of cluster data so users aren't confused.
//
// The cursor is re-anchored to the same fork across the toggle so the
// user's selection doesn't jump after the resort.
func (m *Model) toggleGroupByCluster() {
	var selectedID string
	if m.cursor >= 0 && m.cursor < len(m.forks) {
		selectedID = m.forks[m.cursor].Fork.ID
	}
	if !m.hasClusterData() {
		m.errMsg = "no clusters available"
		m.errMsgTime = time.Now()
		m.groupByCluster = !m.groupByCluster
		m.reapplySort()
		m.restoreCursorByID(selectedID)
		return
	}
	m.groupByCluster = !m.groupByCluster
	m.reapplySort()
	m.restoreCursorByID(selectedID)
}

// reapplySort routes through either sortForks (flat) or
// sortForksByCluster (grouped) depending on the current toggle.
func (m *Model) reapplySort() {
	if m.groupByCluster {
		m.sortForksByCluster()
	} else {
		m.sortForks()
	}
}

// restoreCursorByID finds the fork with the given ID in m.forks and sets
// m.cursor to its index. If not found, the cursor is clamped to a valid
// position.
func (m *Model) restoreCursorByID(id string) {
	if id == "" {
		return
	}
	for i := range m.forks {
		if m.forks[i].Fork.ID == id {
			m.cursor = i
			return
		}
	}
	if m.cursor >= len(m.forks) {
		m.cursor = len(m.forks) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// hasClusterData reports whether at least one fork carries a populated
// ClusterID (including "noise"). Used to gate the "g" toggle's user
// feedback — the toggle itself always flips so tests can observe state.
func (m *Model) hasClusterData() bool {
	for i := range m.forks {
		if m.forks[i].Heat.ClusterID != "" {
			return true
		}
	}
	return false
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

	repo := strings.TrimSpace(m.input)
	if m.initRepo != "" {
		repo = m.initRepo
		m.input = repo
		m.initRepo = ""
	}

	// Topic mode: resolve the best repos for the topic and show the picker.
	if topicName, isTopic := strings.CutPrefix(repo, "topic:"); isTopic {
		m.loadMsg = fmt.Sprintf("Resolving topic %q...", topicName)
		return m.resolveTopicCmd(topicName)
	}

	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		m.loading = false
		m.inputErr = "Invalid format, use owner/repo"
		return nil
	}
	owner, name := parts[0], parts[1]
	m.loadMsg = fmt.Sprintf("Fetching %s/%s...", owner, name)

	provider := m.provider
	refresh := m.refresh

	db := m.db
	storeProvider, storeHost := m.storeIdentity()

	return func() tea.Msg {
		// The store is consulted for every provider. A refresh skips it
		// entirely — both the fork list and the per-fork compare reuse.
		var snap *store.RepoSnapshot
		if !refresh && db != nil {
			snap, _ = db.LoadRepoSnapshot(context.Background(), storeProvider, storeHost, owner, name)
		}
		if snap != nil && snap.Parent != nil && len(snap.Forks) > 0 && time.Since(snap.ForksSyncedAt) < forkListTTL {
			forks := make([]forge.T1Data, 0, len(snap.Forks))
			for _, cf := range snap.Forks {
				forks = append(forks, cf.T1)
			}
			return cachedLoadMsg{
				parent: *snap.Parent,
				forks:  forks,
				snap:   snap,
			}
		}

		parent, err := provider.Parent(context.Background(), owner, name)
		return parentFetchedMsg{parent: parent, err: err, snap: snap}
	}
}

// storeIdentity returns the provider/host pair used in store repo keys.
func (m *Model) storeIdentity() (string, string) {
	host := m.auth.Host
	if host == "" {
		host = forge.DefaultHost(m.auth.Provider)
	}
	return m.auth.Provider.String(), host
}

// storeRepoRecord builds the RepoRecord for the current upstream. Parent and
// ForksSyncedAt are attached only when requested — a per-fork compare save
// must not overwrite what a full enumeration recorded.
func (m *Model) storeRepoRecord(withParent bool, syncedAt time.Time) (store.RepoRecord, bool) {
	if m.parent == nil {
		return store.RepoRecord{}, false
	}
	parts := strings.SplitN(m.parent.FullName, "/", 2)
	if len(parts) != 2 {
		return store.RepoRecord{}, false
	}
	providerName, host := m.storeIdentity()
	now := time.Now().UTC()
	rec := store.RepoRecord{
		Provider: providerName, Host: host, Owner: parts[0], Name: parts[1],
		FirstSeen: now, LastSeen: now, ForksSyncedAt: syncedAt,
	}
	if withParent {
		p := *m.parent
		rec.Parent = &p
	}
	return rec, true
}

// persistForkList writes the freshly fetched (and scored) fork list to the
// store as a background command, stamping the parent and the sync time.
func (m *Model) persistForkList() tea.Cmd {
	if m.db == nil {
		return nil
	}
	repo, ok := m.storeRepoRecord(true, time.Now().UTC())
	if !ok {
		return nil
	}
	now := time.Now().UTC()
	snaps := make([]store.Snapshot, 0, len(m.forks))
	for i := range m.forks {
		f := &m.forks[i]
		snaps = append(snaps, store.SnapshotFromForge(repo, f.Fork, nil, f.Heat.Score, f.Heat.Tier, now))
	}
	db := m.db
	return func() tea.Msg {
		if err := db.UpsertSnapshots(context.Background(), snaps); err != nil {
			return errMsg{err: fmt.Errorf("persist fork list: %w", err)}
		}
		return nil
	}
}

// persistCompare writes one fork's fresh compare (with its full T1 context and
// updated heat) to the store, synchronously — a single-fork snapshot is a few
// milliseconds and keeps the update loop simple.
func (m *Model) persistCompare(i int) {
	if m.db == nil || i < 0 || i >= len(m.forks) {
		return
	}
	repo, ok := m.storeRepoRecord(false, time.Time{})
	if !ok {
		return
	}
	f := &m.forks[i]
	snap := store.SnapshotFromForge(repo, f.Fork, f.T2, f.Heat.Score, f.Heat.Tier, time.Now().UTC())
	if err := m.db.UpsertSnapshot(context.Background(), snap); err != nil {
		m.errMsg = fmt.Sprintf("Store write failed: %s", err)
		m.errMsgTime = time.Now()
	}
}

func (m *Model) fetchForks() tea.Cmd {
	provider := m.provider
	parent := m.parent
	return func() tea.Msg {
		parts := strings.SplitN(parent.FullName, "/", 2)
		if len(parts) != 2 {
			return forksFetchedMsg{err: fmt.Errorf("invalid parent name: %s", parent.FullName)}
		}
		ch, err := provider.ListForks(context.Background(), parts[0], parts[1])
		if err != nil {
			return forksFetchedMsg{err: err}
		}

		var forks []forge.T1Data
		var streamErr error
		for msg := range ch {
			if msg.Err != nil {
				streamErr = msg.Err // remember; per-fork errors are tolerated below
				continue
			}
			forks = append(forks, msg.Fork)
		}

		// A fatal fetch failure (e.g. the GraphQL forks query erroring out)
		// arrives as a stream error and would otherwise leave us silently
		// showing zero forks. Surface it instead — but only when nothing came
		// through, so partial results from per-fork failures are still kept.
		if len(forks) == 0 && streamErr != nil {
			return forksFetchedMsg{err: streamErr}
		}

		// Partial result: forks arrived but the stream then errored. Keep the
		// list but surface a non-fatal warning so the user knows it was cut short
		// rather than silently trusting an incomplete list.
		if streamErr != nil {
			return forksFetchedMsg{forks: forks, warn: streamErr}
		}

		return forksFetchedMsg{forks: forks}
	}
}

func (m *Model) doRefresh() tea.Cmd {
	m.refresh = true
	m.cancelEnrichment()
	m.forks = nil
	m.parent = nil
	m.cached = nil
	m.enrichDone = 0
	m.enrichTotal = 0
	m.loading = true
	m.loadMsg = "Refreshing..."
	return m.startFetch()
}

// --- Scoring ---

func (m *Model) scoreForks(forks []forge.T1Data) {
	if m.parent == nil {
		return
	}
	now := time.Now()

	// Build per-fork stats for percentile ranking, filtering ghosts first.
	var live []forge.T1Data
	for _, f := range forks {
		if !heat.IsGhostFork(f.PushedAt, m.parent.PushedAt, f.IsArchived) {
			live = append(live, f)
		}
	}

	stats := makeTUIStats(live, now)
	m.scorer = heat.NewScorerWeighted(stats, m.heatWeights)

	m.forks = make([]ScoredFork, 0, len(live))
	for i, f := range live {
		input := buildTUIScoreInput(f, *m.parent, now)
		result := m.scorer.ScoreRaw(input)
		m.scorer.Finalize(&result, int64(i), heat.PenaltyInput{Archived: f.IsArchived})
		sf := ScoredFork{Fork: f, Heat: result, statID: int64(i)}
		m.forks = append(m.forks, sf)
	}
	m.sortForks()
}

// makeTUIStats builds ForkStats for heat.NewScorer from a slice of T1 forks.
func makeTUIStats(forks []forge.T1Data, now time.Time) []heat.ForkStats {
	stats := make([]heat.ForkStats, len(forks))
	for i, f := range forks {
		stats[i] = heat.ForkStats{
			ForkID:     int64(i),
			Stars:      f.Stars,
			SubForks:   f.SubForkCount,
			PushedDays: now.Sub(f.PushedAt).Hours() / 24,
		}
	}
	return stats
}

// buildTUIScoreInput maps T1 fork data to a heat.ScoreInput.
func buildTUIScoreInput(f forge.T1Data, parent forge.ParentData, now time.Time) heat.ScoreInput {
	return heat.ScoreInput{
		T1: heat.Tier1ParamsV2{
			Stars:             f.Stars,
			SubForks:          f.SubForkCount,
			ReleaseCount:      f.ReleaseCount,
			DaysSincePush:     now.Sub(f.PushedAt).Hours() / 24,
			DaysSinceUpstream: now.Sub(parent.PushedAt).Hours() / 24,
		},
	}
}

// buildTUILoneWolfInput adapts T2Data into the heat.LoneWolfInput shape.
func buildTUILoneWolfInput(f forge.T1Data, now time.Time, t2 *forge.T2Data) heat.LoneWolfInput {
	commits := make([]heat.LWCommitInfo, 0, len(t2.Commits))
	authors := make([]string, 0, len(t2.Commits))
	for _, c := range t2.Commits {
		login := c.AuthorLogin
		if login == "" {
			login = c.AuthorEmail
		}
		commits = append(commits, heat.LWCommitInfo{
			AuthorLogin: login,
			Message:     c.Message,
			Date:        c.Timestamp,
		})
		// Only count a real identifier as a contributor. An empty login would be
		// treated as a distinct human by heat.filterBots, producing false-positive
		// lone-wolf detections; the commit still feeds message analysis above.
		if login != "" {
			authors = append(authors, login)
		}
	}
	files := make([]heat.FileChange, 0, len(t2.Diffs))
	for _, d := range t2.Diffs {
		files = append(files, heat.FileChange{
			Filename:  d.Path,
			Additions: d.Additions,
			Deletions: d.Deletions,
		})
	}
	return heat.LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  authors,
		AheadBy:       t2.AheadCount,
		DaysSincePush: now.Sub(f.PushedAt).Hours() / 24,
	}
}

// --- Enrichment ---

func (m *Model) startEnrichment() tea.Cmd {
	if m.parent == nil || len(m.forks) == 0 {
		return nil
	}
	// No early headroom return: cached compares still serve for free below, and
	// uncached forks are marked degraded per-fork at the reserve floor rather
	// than silently skipped.

	ctx, cancel := context.WithCancel(context.Background())
	m.enrichCtx = ctx
	m.enrichCancel = cancel
	m.enriching = true
	m.enrichDone = 0

	if len(m.forks) == 0 {
		m.enriching = false
		return nil
	}

	// Enrich best-first: order by EVPR (DispatchPriority over the cheap
	// divergence signal), so when the rate-limit reserve floor cuts the pass
	// short, the forks that DID get compared are the ones most likely to have
	// real divergence — not the most popular or a random sample.
	parentPushed := m.parent.PushedAt
	toEnrich := make([]int, len(m.forks))
	for i := range toEnrich {
		toEnrich[i] = i
	}
	sort.SliceStable(toEnrich, func(a, b int) bool {
		fa, fb := m.forks[toEnrich[a]], m.forks[toEnrich[b]]
		return forksops.DispatchPriority(fa.Fork, parentPushed, fa.Heat.Score) >
			forksops.DispatchPriority(fb.Fork, parentPushed, fb.Heat.Score)
	})

	m.enrichTotal = len(toEnrich)
	for _, idx := range toEnrich {
		m.forks[idx].Enriching = true
	}

	// Concurrency from auth info
	concurrency := m.auth.Concurrency
	if concurrency <= 0 {
		concurrency = 2
	}

	provider := m.provider
	refresh := m.refresh
	cached := m.cached
	sem := make(chan struct{}, concurrency)

	cmds := make([]tea.Cmd, 0, len(toEnrich)+1)
	cmds = append(cmds, batchTick())

	for _, idx := range toEnrich {
		f := m.forks[idx].Fork
		forkID := f.ID

		cmds = append(cmds, func() tea.Msg {
			// Serve the compare from the store when the fork hasn't been pushed
			// since it was recorded.
			if !refresh {
				if t2 := cachedT2For(cached, f); t2 != nil {
					return tier2ResultMsg{forkID: forkID, t2: *t2}
				}
			}

			// Acquire semaphore
			select {
			case <-ctx.Done():
				return tier2ResultMsg{forkID: forkID, err: ctx.Err()}
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			// Auto-budget reserve: stop spending the rate window once headroom
			// hits the floor. Best-first ordering means the forks already
			// compared are the most promising; this one is marked degraded
			// (not failed, not zeroed) so the export can say so.
			if provider.Headroom() < forksops.ReserveHeadroom {
				return tier2ResultMsg{forkID: forkID, budgetSkipped: true}
			}

			t2, err := provider.Compare(ctx, f, f.DefaultBranch)
			return tier2ResultMsg{forkID: forkID, t2: t2, err: err}
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

// cancelLifecycle cancels the model's lifecycle context, signalling the
// long-running cluster message-pump goroutine to exit. Safe to call
// multiple times.
func (m *Model) cancelLifecycle() {
	if m.lifecycleCancel != nil {
		m.lifecycleCancel()
		m.lifecycleCancel = nil
	}
}

// --- Browser ---

func (m *Model) openInBrowser() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.forks) {
		return nil
	}
	url := m.forks[m.cursor].Fork.URL
	if url == "" {
		return nil
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
	url := forge.CompareURL(m.auth.Provider, m.auth.Host,
		m.parent.FullName, m.parent.DefaultBranch,
		fork.Owner, fork.DefaultBranch)
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
	case viewExportPath:
		return m.viewExportPath()
	case viewTopicPicker:
		return m.viewTopicPicker()
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
		if m.auth.Authenticated() {
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
		b.WriteString("\n  " + helpStyle.Render("Enter a GitHub or GitLab repository (e.g., golang/go)") + "\n")
		b.WriteString("  " + helpStyle.Render("Press Enter to search, Ctrl+C to quit") + "\n")
	}

	return b.String()
}

func relativeTime(isoTime string) string {
	t, err := time.Parse(time.RFC3339, isoTime)
	if err != nil {
		return "unknown"
	}
	return relativeTimeSince(t)
}

func relativeTimeSince(t time.Time) string {
	if t.IsZero() {
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
