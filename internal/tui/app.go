package tui

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cli/go-gh/v2/pkg/browser"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/pathmatch"
	"github.com/svnbjrn/spoon/internal/store"
	"github.com/svnbjrn/spoon/internal/topics"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/settings"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"github.com/svnbjrn/spoon/internal/tui/ui"
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
	// Appended rather than inserted: the enum is positional, and renumbering
	// the existing values would make every diff below this line noise.
	viewFilter
	viewRank
	// Appended rather than inserted: the enum is positional.
	viewSettings
	// Appended rather than inserted: the enum is positional.
	viewPatch
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

	// TierSkipped is true when the compare was not attempted because the
	// user's enrichment ceiling (see Model.tierCeiling) sat below T2. It is
	// deliberately distinct from BudgetSkipped: this one is undone by raising
	// the ceiling, that one by waiting for the rate window. Heat.Tier cannot
	// serve as this marker -- it is an output ("highest tier whose params
	// were non-nil"), so a fork whose compare legitimately returned no
	// commits is indistinguishable from one that was never compared.
	TierSkipped bool

	// Duplicate-group membership: forks carrying identical work. SiblingGroup
	// is the shared identity key (empty when this fork is unique), SiblingCount
	// the group size, and SiblingPrimary marks the highest-scoring member. The
	// trio is set only on commit-identity proof (branch fingerprint or head
	// SHA); SiblingCandidate carries the weaker same-diff-shape signal, which
	// never folds. Assigned by assignDuplicateGroups; see duplicates.go.
	SiblingGroup     string
	SiblingCount     int
	SiblingPrimary   bool
	SiblingCandidate string

	// Rank is the shortlist rank summary from the last recomputeShortlist
	// (P-score, P(rank ≤ k), interval, tie band); nil until ranked or when
	// the row fell outside the rank pool. See shortlist.go.
	Rank *forksops.RankStats
}

// Model is the top-level Bubble Tea model.
type Model struct {
	// State
	view viewState
	// shortlist is the pool-level rank report (POTH, cPOTH_k) from the last
	// recomputeShortlist; nil before the first scoring pass.
	shortlist *forksops.RankReport
	// shortlistHash is the signature (fork IDs + heat scores) recomputeShortlist
	// last ranked. A resort that doesn't change any heat score matches this
	// hash and skips the O(n³) rank pass instead of repeating it.
	shortlistHash string
	width         int
	height        int
	quitting      bool

	// fullscreen hides table/detail chrome without changing selection,
	// scroll offsets, or paging geometry.
	fullscreen bool

	// theme is resolved once by command startup. Bare test literals use
	// themeContext's deterministic default instead.
	theme theme.Context

	// overlay is intentionally independent of view. Settings can place a
	// confirmation over any view without allowing its key handler to run.
	overlay      ui.Overlay
	overlayFocus overlayFocus
	overlayTitle string

	// Input
	input string
	// inputCursor is the insertion point as a rune offset into input.
	inputCursor int
	inputErr    string
	initRepo    string // from CLI arg
	refresh     bool   // bypass cache

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
	// acquisition holds the terminal acquisition metadata from the most recent
	// ListForks run. It is nil when the provider does not supply a report.
	acquisition *forge.AcquisitionReport

	// Table state
	cursor  int
	sortCol string
	sortAsc bool

	// filter is the active row filter ("" = show everything). It narrows what
	// the table renders and what the cursor may land on; it never reslices
	// m.forks, which stays the canonical, complete list. See filter.go.
	filter string
	// pathFilter is the compiled matcher for a "path:<glob>" filter query, or
	// nil otherwise. Compiled once in applyFilter rather than per row.
	pathFilter *pathmatch.Matcher
	// filterInput is the in-progress prompt text, applied to filter on Enter;
	// filterCursor is its insertion point as a rune offset, matching the
	// input/export prompts.
	filterInput  string
	filterCursor int

	// Scroll offsets for the two views that render a body taller than the
	// terminal. Kept separate so opening help from the detail view does not
	// inherit the detail view's scroll position.
	detailOffset int
	helpOffset   int

	// Patch view (viewPatch): a live provider.Compare fetched on demand from
	// the detail view's `p` key, since cached compares carry no patch text.
	// Not persisted -- view-only, unlike the enrichment sweep's T2 cache.
	patchBody    string
	patchOffset  int
	patchLoading bool
	// patchSeq numbers patch fetches; a patchResultMsg whose seq is not the
	// latest is stale (the user pressed p again, possibly on the same fork)
	// and is dropped before it can clear the loading flag or overwrite the
	// newer result.
	patchSeq int

	// Enrichment
	enriching    bool
	enrichDone   int
	enrichTotal  int
	enrichCtx    context.Context
	enrichCancel context.CancelFunc

	// tierCeiling is the user's maximum enrichment tier, cycled by `c`.
	// It is a pointer to an atomic rather than a plain int because
	// startEnrichment hands every per-fork closure to tea.Batch up front:
	// those closures run on bubbletea's goroutines and must read the ceiling
	// at execution time, not the value captured when they were built. A plain
	// field would also be copied by value on every Update and never observed
	// by an in-flight closure at all. nil means "unset" -- read it through
	// maxTier(), never directly, since tests build bare Model literals.
	tierCeiling *atomic.Int32

	// enrichSem bounds concurrent compares. Hoisted out of startEnrichment so
	// a re-enrichment pass shares one limiter with the original run instead of
	// doubling effective concurrency against the rate limit.
	enrichSem chan struct{}
	// enrichQueue orders live compares for the current pass (see
	// enrich_queue.go); nil between passes.
	enrichQueue *enrichQueue
	// batchResolving is true while the GraphQL divergence batch runs, before
	// any live compare is dispatched; batchStats/batchErr describe its result.
	batchResolving bool
	batchStats     *forge.BatchStats
	batchErr       string
	// unreachable counts forks dropped from the list because their
	// repository no longer exists (see tier2ResultMsg.unreachable).
	unreachable int

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
	exportPath string // editable path shown in prompt
	// exportCursor is the insertion point as a rune offset into exportPath,
	// in [0, len([]rune(exportPath))]. Without it the prompt was append-only,
	// so a suggested filename could not be corrected — every keystroke landed
	// after ".json".
	exportCursor int
	exportForks  []ScoredFork // forks staged for export (nil = export all)

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

	// Intent ranking ("R"), distinct from the `/` filter above and composing with
	// it: `/` narrows by owner/name, `R` orders the survivors by relevance.
	// rankQuery is the prompt buffer and rankCursor its rune offset; rankApplied
	// is the query the table is currently ranked by ("" = no ranking). rankScores
	// maps fork ID to relevance and rankMethod names the scorer that produced it.
	// rankSeq stamps each request so a slow result landing after a newer query is
	// discarded. queryScorer is nil for the built-in lexical scorer. See rank.go.
	rankQuery   string
	rankCursor  int
	rankApplied string
	rankMethod  string
	rankScores  map[string]float64
	rankSeq     int
	rankPending bool
	queryScorer embed.QueryScorer
	settings    settings.Model

	// embedProbe holds what the session knows about the two embedding
	// providers. Probed from the resolved configuration rather than assumed,
	// because both of fastembed's prerequisites live outside the binary. See
	// embedstatus.go.
	embedProbe embedStatus

	// embedModels are the index partitions the EMB column reports on, and
	// embedCoverage is the per-fork answer keyed by store fork key. Coverage is
	// loaded through a command, never in View: View runs on every keystroke and
	// must not touch the database. See embedcolumn.go.
	embedModels   embedModels
	embedCoverage map[string]store.ForkCoverage

	// An embedding run in flight. embedMsgs carries per-batch progress from the
	// indexing goroutine onto the update loop; it is buffered and lossy, since
	// a UI that is not draining must never stall billed work. See embedrun.go.
	embedRunning        bool
	embedRunID          int
	embedRunTotal       int
	embedRunNote        string
	embedMsgs           chan tea.Msg
	embedProgressCtx    context.Context
	embedProgressCancel context.CancelFunc

	// autoIndexDone latches the automatic pass to once per fork list. Raising
	// the tier ceiling re-enriches and produces a second enrichmentDoneMsg,
	// which must not silently start another billed run.
	autoIndexDone bool
}

// overlayFocus is the main model's active focus identity. Fork views retain
// their selected fork ID, not merely the transient list index, so an async
// resort cannot make Esc restore focus to a different fork. Control is the
// extensible settings-focus identity for future non-fork overlays.
type overlayFocus struct {
	view    viewState
	cursor  int
	forkID  string
	control string
}

func (m *Model) openOverlay(kind ui.OverlayKind, title string) {
	focus := overlayFocus{view: m.view, cursor: m.cursor}
	if (m.view == viewTable || m.view == viewDetail) && m.cursor >= 0 && m.cursor < len(m.forks) {
		focus.forkID = m.forks[m.cursor].Fork.ID
	}
	m.overlayFocus = focus
	m.overlayTitle = title
	m.overlay.Open(kind, fmt.Sprintf("%d:%s", m.view, focus.forkID))
}

func (m *Model) restoreOverlayFocus(token string) {
	if token == "" {
		return
	}
	m.view = m.overlayFocus.view
	if m.overlayFocus.forkID != "" {
		m.restoreCursorByID(m.overlayFocus.forkID)
	} else {
		m.cursor = m.overlayFocus.cursor
	}
	m.overlayTitle = ""
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
		embedMsgs:       make(chan tea.Msg, 16),
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
		theme:           theme.DefaultContext(),
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

// defaultMaxTier is the enrichment ceiling when none is set: full enrichment,
// matching the behavior that predates the ceiling entirely.
const defaultMaxTier = 3

// maxTier is the active enrichment ceiling, in [1,3]. Every read of the
// ceiling goes through here rather than touching tierCeiling directly: the
// field is nil on any Model built as a bare literal (which most tests in this
// package do), and a direct Load would panic in all of them.
func (m *Model) maxTier() int {
	if m.tierCeiling == nil {
		return defaultMaxTier
	}
	return clampInt(int(m.tierCeiling.Load()), 1, 3)
}

// setMaxTier stores a new ceiling, allocating the atomic on first use.
func (m *Model) setMaxTier(n int) {
	if m.tierCeiling == nil {
		m.tierCeiling = &atomic.Int32{}
	}
	m.tierCeiling.Store(int32(clampInt(n, 1, 3)))
}

// WithMaxTier returns a copy of the model with the given enrichment ceiling,
// for the `spoon --tier` flag. Mirrors `spn --tier`.
func (m Model) WithMaxTier(n int) Model {
	m.setMaxTier(n)
	return m
}

// WithStore returns a copy of the model backed by the global store. The store
// is mandatory at runtime — the caller (cmd/spoon) fails hard when it cannot
// be opened — but stays nil-able so unit tests can run modelless.
func (m Model) WithStore(db *store.Store) Model {
	m.db = db
	return m
}

// WithTheme returns a copy of the model with an immutable startup-resolved
// rendering context.
func (m Model) WithTheme(ctx theme.Context) Model {
	m.theme = ctx
	m.settings = m.settings.WithTheme(ctx)
	return m
}

// WithSettings attaches the complete in-TUI settings surface after startup has
// resolved the active configuration layer.
func (m Model) WithSettings(settingsModel settings.Model) Model {
	m.settings = settingsModel.WithTheme(m.themeContext())
	// The settings model carries the resolved configuration, so this is the
	// first point at which either provider can be probed or its index
	// partition named.
	m.refreshEmbedStatus()
	m.resolveEmbedModels()
	return m
}

// forkListTTL bounds how long a stored fork enumeration serves as the full
// list: membership can change (new forks with no push to any cached fork), so
// list freshness is time-based, unlike compare validity keyed on each push.
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
	var settingsCmd tea.Cmd
	if m.view == viewSettings {
		if window, ok := msg.(tea.WindowSizeMsg); ok {
			m.width, m.height = window.Width, window.Height
		}
		updated, cmd := m.settings.Update(msg)
		m.settings = updated.(settings.Model)
		settingsCmd = cmd
		if _, closing := msg.(settings.CloseRequested); closing {
			// Re-probe on the way out. Setting embedder.voyage.apiKeyFile in
			// this panel is the whole remedy for "my key file is never read",
			// so a status bar that kept its startup answer until the next
			// restart would report the problem as unfixed right after fixing
			// it. Closing is the hook rather than every keystroke: the panel
			// covers the status bar anyway, and this touches the filesystem.
			m.refreshEmbedStatus()
			m.resolveEmbedModels()
			m.view = viewTable
			if len(m.forks) == 0 {
				m.view = viewInput
			}
			return m, m.loadEmbedCoverage()
		}
		switch msg.(type) {
		case tea.KeyMsg, tea.WindowSizeMsg:
			return m, settingsCmd
		}
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		if handled, restoredFocus := m.overlay.HandleKey(msg.String()); handled {
			m.restoreOverlayFocus(restoredFocus)
			return m, nil
		}
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

	case batchDivergenceMsg:
		return m.handleBatchDivergence(msg)

	case patchResultMsg:
		// Only the most recent fetch may clear the loading flag or apply a
		// body: an older request (same fork or not) resolving after a newer
		// one must not hide the newer request's loading state or overwrite
		// its result. The fork check below is the second line of defence.
		if msg.seq != m.patchSeq {
			return m, nil
		}
		m.patchLoading = false
		if m.cursor < 0 || m.cursor >= len(m.forks) || m.forks[m.cursor].Fork.ID != msg.forkID {
			return m, nil
		}
		if msg.err != nil {
			m.patchBody = "Patch fetch failed: " + msg.err.Error()
		} else {
			m.patchBody = renderPatch(m.themeContext(), msg.t2, m.pathFilter, maxPatchChars)
		}
		return m, nil

	case enrichBatchTickMsg:
		return m.processPendingUpdates()

	case branchDivergenceMsg:
		return m.handleBranchDivergence(msg)

	case linearHistoryMsg:
		return m.handleLinearHistory(msg)

	case enrichmentDoneMsg:
		m.enriching = false
		// Auto-indexing fires here rather than on load, and the difference is
		// not cosmetic. A fork's document is built from its T1 data before
		// enrichment and from T1+T2 after, so embedding at load time would
		// index every fork against a body that enrichment then invalidates --
		// paying twice under a provider billed per token. Waiting for the
		// compares to settle means one pass over the final content.
		return m, m.maybeAutoIndex()

	case clusterResultMsg:
		return m.handleClusterResult(msg)

	case rankResultMsg:
		return m.handleRankResult(msg)

	case clipboardMsg:
		if msg.success {
			m.clipMsg = "Copied: " + msg.cmd
		} else {
			m.clipMsg = "[Manual copy] " + msg.cmd
		}
		m.clipMsgTime = time.Now()
		return m, nil

	case embedRunStartedMsg:
		if !m.embedRunning || msg.run != m.embedRunID {
			return m, nil
		}
		m.embedRunTotal = msg.forks
		m.embedRunNote = fmt.Sprintf("embedding %d forks...", msg.forks)
		// Arm the progress pump only now, so a session that never embeds
		// anything never spawns the goroutine.
		return m, waitForClusterMsg(m.embedMsgs, m.embedProgressCtx)

	case embedProgressMsg:
		if !m.embedRunning || msg.run != m.embedRunID {
			return m, nil
		}
		m.embedRunNote = fmt.Sprintf("%s %d/%d", msg.provider, msg.indexed, msg.pending)
		return m, waitForClusterMsg(m.embedMsgs, m.embedProgressCtx)

	case embedRunDoneMsg:
		if msg.run != m.embedRunID {
			return m, nil
		}
		m.embedRunning = false
		if m.embedProgressCancel != nil {
			m.embedProgressCancel()
			m.embedProgressCancel = nil
		}
		m.embedRunNote = ""
		m.errMsg = describeEmbedResult(msg)
		m.errMsgTime = time.Now()
		// Re-read coverage so the column reflects what the run just wrote.
		return m, m.loadEmbedCoverage()

	case embedCoverageMsg:
		if msg.err != nil {
			m.errMsg = fmt.Sprintf("Embedding coverage unavailable: %s", msg.err)
			m.errMsgTime = time.Now()
			return m, nil
		}
		m.embedCoverage = msg.coverage
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

	return m, settingsCmd
}

func (m *Model) handleCachedLoad(msg cachedLoadMsg) (tea.Model, tea.Cmd) {
	m.parent = &msg.parent
	m.cached = msg.snap
	m.loading = false
	m.loadMsg = fmt.Sprintf("Loaded %d forks from cache", len(msg.forks))

	m.scoreForks(msg.forks)
	m.view = viewTable
	// Snap rather than assign 0: a filter carried across a refresh may hide
	// the first fork, and the cursor must never land on a hidden row.
	m.cursor = clampCursorVisible(0, m.visibleIdx())

	// Apply cached compare data
	m.applyCachedCompares(msg.snap)

	// Group immediately. Cached forks already carry everything the fingerprint
	// and head-SHA keys need, so waiting for the first enrichment batch would
	// render the initial table without badges or gutters for no reason.
	// applyCachedCompares sorts, so gather has to follow it, not precede it.
	m.assignDuplicateGroups()
	m.gatherDuplicateGroups(0, len(m.forks))

	cmds := []tea.Cmd{}
	// The cache path writes no snapshots, so the coverage read can go straight
	// out rather than waiting behind a persist.
	if cov := m.loadEmbedCoverage(); cov != nil {
		cmds = append(cmds, cov)
	}
	if bc := m.startBranchDivergenceSweep(); bc != nil {
		cmds = append(cmds, bc)
	}

	// Local derivation: if a cached fork carries a MergeCommitHistory vector
	// but no LinearHistory boolean, derive it now without a network call.
	m.deriveLinearHistoryFromCached()

	// If the provider supports linear-history, schedule a sweep for forks
	// that still have no data at all (both vector and boolean missing).
	if lh := m.startLinearHistorySweep(); lh != nil {
		cmds = append(cmds, lh)
	}

	cmd := m.startEnrichment()
	if cmd == nil {
		// No T2 enrichment scheduled (e.g. rate-limited). Still try
		// clustering and automatic indexing on whatever T1+cached-T2 data we have.
		if cc := m.maybeStartClusterPipeline(); cc != nil {
			cmds = append(cmds, cc)
		}
		if auto := m.maybeAutoIndex(); auto != nil {
			cmds = append(cmds, auto)
		}
	}
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *Model) applyCachedCompares(snap *store.RepoSnapshot) {
	if snap == nil {
		return
	}
	for i := range m.forks {
		t2 := snap.ValidT2(m.forks[i].Fork)
		if t2 == nil {
			continue
		}
		m.forks[i].T2 = t2
		m.forks[i].Enriched = true

		m.recomputeT2Score(i)
	}
	m.reapplySort()
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
		// The footer renders errMsg only while errMsgTime is within five
		// seconds, so leaving the timestamp zero -- as this branch did, unlike
		// the warn branch below -- made the message permanently unrenderable.
		m.errMsgTime = time.Now()
		return m, nil
	}

	if msg.warn != nil {
		m.errMsg = fmt.Sprintf("Warning: fork list may be incomplete: %s", msg.warn)
		m.errMsgTime = time.Now()
	}

	// Store the terminal acquisition report if the provider supplied one.
	m.acquisition = msg.Report
	m.unreachable = 0

	m.scoreForks(msg.forks)
	m.view = viewTable
	// Snap rather than assign 0: a filter carried across a refresh may hide
	// the first fork, and the cursor must never land on a hidden row.
	m.cursor = clampCursorVisible(0, m.visibleIdx())

	// Reuse stored compares for forks whose pushed_at is unchanged, then
	// persist the freshly scored list.
	m.applyCachedCompares(m.cached)

	cmds := []tea.Cmd{}
	persist := m.persistForkList()
	coverage := m.loadEmbedCoverage()
	lh := m.startLinearHistorySweep()
	if persist != nil {
		// Sequenced, not batched: the coverage read has to see the documents
		// the persist writes, or the EMB column reports nothing on first paint
		// and only corrects itself after some later reload.
		var tail tea.Cmd
		switch {
		case coverage != nil && lh != nil:
			tail = tea.Batch(coverage, lh)
		case coverage != nil:
			tail = coverage
		case lh != nil:
			// Linear-history sweep sequenced after the persist itself so the
			// relational merge_commit_history rows land in the second persist
			// handleLinearHistory triggers. Folding it into the same
			// sequence is the only way to avoid the race where the
			// LinearHistory boolean is reported before its underlying
			// vector rows hit the table.
			tail = lh
		}
		cmds = append(cmds, tea.Sequence(persist, tail))
	} else if lh != nil {
		cmds = append(cmds, lh)
	}
	if bc := m.startBranchDivergenceSweep(); bc != nil {
		cmds = append(cmds, bc)
	}

	cmd := m.startEnrichment()
	if cmd == nil {
		if cc := m.maybeStartClusterPipeline(); cc != nil {
			cmds = append(cmds, cc)
		}
		if auto := m.maybeAutoIndex(); auto != nil {
			cmds = append(cmds, auto)
		}
	}
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *Model) processPendingUpdates() (tea.Model, tea.Cmd) {
	if len(m.pendingUpdates) == 0 {
		if m.enriching {
			return m, batchTick()
		}
		return m, nil
	}
	coverageDirty := false
	// Captured before any row is dropped below, so the cursor stays on the
	// same fork rather than the same index.
	var selectedID string
	if m.cursor >= 0 && m.cursor < len(m.forks) {
		selectedID = m.forks[m.cursor].Fork.ID
	}
	gone := map[string]bool{}

	for _, update := range m.pendingUpdates {
		m.enrichDone++

		if update.unreachable {
			gone[update.forkID] = true
			continue
		}

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

		// Ceiling skip: same shape as the reserve skip, different cause and
		// different remedy — raising the ceiling re-enriches exactly these.
		if update.tierSkipped {
			for i := range m.forks {
				if m.forks[i].Fork.ID == update.forkID {
					m.forks[i].Enriching = false
					m.forks[i].TierSkipped = true
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

				// A compare that never ran carries no divergence to show,
				// score or persist. Leaving T2 nil renders AHEAD/BEHIND as
				// "-" and keeps the T1 heat, rather than reporting the fork
				// as verified-stagnant and hard-zeroing it via no_ahead.
				if !t2.Performed {
					m.forks[i].Enriching = false
					break
				}

				m.forks[i].T2 = &t2
				m.forks[i].Enriching = false
				m.forks[i].Enriched = true

				m.recomputeT2Score(i)

				// Cache-served compares are already in the store, verbatim
				// minus patch text — re-persisting would degrade the rows.
				if !update.fromCache {
					m.persistCompare(i)
					coverageDirty = true
				}

				break
			}
		}
	}
	m.pendingUpdates = m.pendingUpdates[:0]
	if len(gone) > 0 {
		kept := m.forks[:0]
		for _, sf := range m.forks {
			if !gone[sf.Fork.ID] {
				kept = append(kept, sf)
			}
		}
		m.unreachable += len(m.forks) - len(kept)
		m.forks = kept
	}

	// Re-group after each settled batch: T2 landing can sharpen a fork's key
	// from the diff-shape fallback to an exact head SHA. Gather rather than
	// re-sort: the rows are already in the user's chosen order, and gathering
	// only moves the members of a freshly-formed group next to each other
	// instead of reordering everything. It can still move rows, though -- so
	// the fork under the cursor (captured above) is restored afterward, same
	// as the sweep handler does for its own re-sort.
	m.assignDuplicateGroups()
	m.gatherDuplicateGroups(0, len(m.forks))
	m.restoreCursorByID(selectedID)
	m.cursor = clampCursorVisible(m.cursor, m.visibleIdx())

	cmds := make([]tea.Cmd, 0, 3)
	if coverageDirty {
		cmds = append(cmds, m.loadEmbedCoverage())
	}
	if m.enrichDone >= m.enrichTotal && m.enriching {
		m.enriching = false
		// T2 streaming finished; kick off cluster pipeline and automatic
		// indexing against the final document bodies.
		if cmd := m.maybeStartClusterPipeline(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.maybeAutoIndex(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}

	if m.enriching {
		cmds = append(cmds, batchTick())
	}
	return m, tea.Batch(cmds...)
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

	// Wire v2 lone wolf when we have commits to analyze -- but only up to the
	// user's ceiling. At T2 the T3 params stay nil, so the lone-wolf component
	// drops out of the score and the 🐺 badge clears on its own: result is
	// freshly built by ScoreRaw below, and LoneWolfV2 is only set when
	// input.T3 is non-nil.
	if m.maxTier() >= 3 && len(t2.Commits) > 0 {
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

	// result is a fresh HeatResult, so assigning it wholesale would erase
	// everything the cluster pipeline wrote in place through &forks[i].Heat
	// (cluster_bridge.go) -- cluster identity, novelty, category, sibling
	// similarity. That is reachable today on the streaming path and becomes
	// trivially reachable once `c` can trigger a rescore after clusters have
	// landed, at which point `g` starts reporting "no clusters available" on
	// a repo that has them.
	carryClusterFields(&result, m.forks[i].Heat)
	m.forks[i].Heat = result
}

// carryClusterFields copies the post-clustering signals from the previous
// HeatResult onto a freshly scored one, then re-applies the two score bonuses
// that depend on them.
//
// The two Apply* helpers are documented as NOT idempotent -- each call adds up
// to +5 -- so they must run exactly once per HeatResult. That holds here
// because dst is always fresh from ScoreRaw/Finalize: the bonuses were never
// applied to it, only to the old result whose raw numbers are being replaced.
func carryClusterFields(dst *heat.HeatResult, old heat.HeatResult) {
	if old.ClusterID == "" && old.NoveltyScore == 0 && old.SiblingSim == 0 {
		return // clustering never ran; nothing to carry and no bonus to re-apply
	}
	dst.ClusterID = old.ClusterID
	dst.ClusterLabel = old.ClusterLabel
	dst.ClusterMemberCount = old.ClusterMemberCount
	dst.NoveltyScore = old.NoveltyScore
	dst.ChangeImpact = old.ChangeImpact
	dst.Category = old.Category
	dst.CategoryScore = old.CategoryScore
	dst.SiblingSim = old.SiblingSim

	heat.ApplyNoveltyToScore(dst)
	heat.ApplySiblingSimilarityToScore(dst)
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
	if keymap.Dispatch(keymap.Global, key) == keymap.Quit {
		m.quitting = true
		m.cancelEnrichment()
		m.cancelLifecycle()
		return m, tea.Quit
	}

	// Text prompts need the literal runes, not the key name: a paste is one
	// KeyRunes event whose String() is bracketed and never matches a binding.
	typed := typedText(msg)

	if m.view == viewSettings {
		updated, cmd := m.settings.Update(msg)
		m.settings = updated.(settings.Model)
		return m, cmd
	}
	switch m.view {
	case viewInput:
		return m.handleInputKey(key, typed)
	case viewTable:
		return m.handleTableKey(key)
	case viewDetail:
		return m.handleDetailKey(key)
	case viewPatch:
		return m.handlePatchKey(key)
	case viewExportPath:
		return m.handleExportPathKey(key, typed)
	case viewTopicPicker:
		return m.handleTopicPickerKey(key)
	case viewFilter:
		return m.handleFilterKey(key, typed)
	case viewRank:
		return m.handleRankKey(key, typed)
	case viewHelp:
		switch keymap.Dispatch(keymap.MainHelp, key) {
		case keymap.Back:
			m.helpOffset = 0
			m.view = viewTable
		case keymap.Up:
			m.scrollHelp(-1)
		case keymap.Down:
			m.scrollHelp(1)
		case keymap.PageUp:
			m.scrollHelp(-m.helpViewHeight())
		case keymap.PageDown:
			m.scrollHelp(m.helpViewHeight())
		case keymap.Home:
			m.helpOffset = 0
		case keymap.End:
			m.helpOffset = maxScrollOffset(helpBody(m.themeContext()), m.helpViewHeight())
		}
		return m, nil
	}

	return m, nil
}

func (m *Model) handleInputKey(key string, typed string) (tea.Model, tea.Cmd) {
	action := keymap.Dispatch(keymap.MainInput, key)
	switch action {
	case keymap.Submit:
		m.inputErr = ""
		m.errMsg = ""
		repo := strings.TrimSpace(m.input)
		if repo == "" || !strings.Contains(repo, "/") {
			m.inputErr = "Enter a valid repository (e.g., golang/go)"
			return m, nil
		}
		return m, m.startFetch()
	case keymap.Back:
		if m.parent != nil {
			m.view = viewTable
		}
	default:
		m.input, m.inputCursor, _ = lineEdit(m.input, m.inputCursor, action, typed)
	}
	return m, nil
}

func (m *Model) handleTableKey(key string) (tea.Model, tea.Cmd) {
	switch keymap.Dispatch(keymap.MainTable, key) {
	case keymap.Quit:
		m.quitting = true
		m.cancelEnrichment()
		m.cancelLifecycle()
		return m, tea.Quit
	case keymap.Up:
		m.moveCursorBy(-1)
	case keymap.Down:
		m.moveCursorBy(1)
	case keymap.PageUp:
		m.moveCursorBy(-m.pageSize())
	case keymap.PageDown:
		m.moveCursorBy(m.pageSize())
	case keymap.Home:
		m.moveCursorTo(0)
	case keymap.End:
		m.moveCursorTo(math.MaxInt)
	case keymap.GroupClusters:
		m.toggleGroupByCluster()
	case keymap.OpenDetail:
		if m.cursor >= 0 && m.cursor < len(m.forks) {
			m.detailOffset = 0
			m.view = viewDetail
		}
	case keymap.NewRepository:
		m.view = viewInput
	case keymap.Filter:
		m.filterInput = m.filter
		m.filterCursor = len([]rune(m.filterInput))
		m.view = viewFilter
	case keymap.Rank:
		m.promptRank()
	case keymap.ClearFilter:
		if m.filter != "" {
			m.applyFilter("")
		}
	case keymap.ToggleHelp:
		m.view = viewHelp
	case keymap.CycleSort:
		m.cycleSortColumn()
	case keymap.ReverseSort:
		m.sortAsc = !m.sortAsc
		m.reapplySort()
	case keymap.OpenBrowser:
		return m, m.openInBrowser()
	case keymap.OpenCompare:
		return m, m.openCompare()
	case keymap.CycleTier:
		return m, m.cycleMaxTier()
	case keymap.ToggleTheme:
		m.theme = theme.ToggleDarkLight(m.themeContext())
		m.settings = m.settings.WithTheme(m.theme)
	case keymap.ToggleFullscreen:
		m.fullscreen = !m.fullscreen
	case keymap.Yank:
		return m, m.yankCloneCommand()
	case keymap.Refresh:
		return m, m.doRefresh()
	case keymap.ToggleMark:
		if m.cursor >= 0 && m.cursor < len(m.forks) {
			m.forks[m.cursor].Marked = !m.forks[m.cursor].Marked
			m.moveCursorBy(1)
		}
	case keymap.ExportMarked:
		return m, m.promptExportMarked()
	case keymap.ExportAll:
		return m, m.promptExportAll()
	case keymap.EmbedMarked:
		return m, m.startEmbedRun(false)
	case keymap.EmbedAll:
		return m, m.startEmbedRun(true)
	case keymap.OpenSettings:
		m.view = viewSettings
		updated, _ := m.settings.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		m.settings = updated.(settings.Model)
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
	// Every heat change (initial score, per-compare rescore, ceiling rescore,
	// cluster novelty) ends in a reapplySort, so ranking here keeps the P
	// column and the precision segment in step with the heat the rows show.
	m.recomputeShortlist()
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
	m.cursor = clampCursorVisible(m.cursor, m.visibleIdx())
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
	switch keymap.Dispatch(keymap.MainDetail, key) {
	case keymap.Back:
		m.detailOffset = 0
		m.view = viewTable
	case keymap.Up:
		m.scrollDetail(-1)
	case keymap.Down:
		m.scrollDetail(1)
	case keymap.PageUp:
		m.scrollDetail(-m.detailViewHeight())
	case keymap.PageDown:
		m.scrollDetail(m.detailViewHeight())
	case keymap.Home:
		m.detailOffset = 0
	case keymap.End:
		m.detailOffset = maxScrollOffset(m.detailBody(), m.detailViewHeight())
	case keymap.OpenBrowser:
		return m, m.openInBrowser()
	case keymap.OpenCompare:
		return m, m.openCompare()
	case keymap.CycleTier:
		return m, m.cycleMaxTier()
	case keymap.ToggleTheme:
		m.theme = theme.ToggleDarkLight(m.themeContext())
	case keymap.ToggleFullscreen:
		m.fullscreen = !m.fullscreen
	case keymap.Yank:
		return m, m.yankCloneCommand()
	case keymap.ViewPatch:
		// fetchPatchCmd returns nil when there is no provider or the cursor
		// is out of range; switching to viewPatch anyway would render
		// whatever patchBody a previous fork's fetch left behind. Only
		// commit to the view -- and clear that stale body -- once a real
		// fetch is in flight.
		if cmd := m.fetchPatchCmd(); cmd != nil {
			m.patchBody = ""
			m.patchOffset = 0
			m.view = viewPatch
			return m, cmd
		}
	}
	return m, nil
}

// --- Fetch logic ---

func (m *Model) startFetch() tea.Cmd {
	m.cancelEnrichment()
	m.loading = true
	m.errMsg = ""
	// A refresh keeps the existing fork list visible during the network
	// round-trip: the user pressed `r` to see what changed, not to stare at
	// an empty table while the request is in flight. The new slices are
	// swapped in by handleForksFetched on a successful response, and the
	// existing list stays in place on error so the user is never told
	// "no forks" mid-refresh.
	if !m.refresh {
		m.forks = nil
		m.parent = nil
	}
	// A new fork list gets its own automatic pass. Latching per list rather
	// than per session is what makes `r` and `n` behave like the first load.
	m.autoIndexDone = false

	repo := strings.TrimSpace(m.input)
	if m.initRepo != "" {
		repo = m.initRepo
		m.input = repo
		m.inputCursor = len([]rune(m.input))
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
	// doRefresh sets its own message and then calls straight into here, so
	// without this the word "Refreshing" never reached a frame. Which verb the
	// user sees should match the key they pressed.
	if m.refresh {
		m.loadMsg = fmt.Sprintf("Refreshing %s/%s...", owner, name)
	} else {
		m.loadMsg = fmt.Sprintf("Fetching %s/%s...", owner, name)
	}

	provider := m.provider
	refresh := m.refresh

	db := m.db
	storeProvider, storeHost := m.storeIdentity()

	return func() tea.Msg {
		// The store is consulted for every provider. A refresh skips it
		// entirely — both the fork list and the per-fork compare reuse.
		var snap *store.RepoSnapshot
		if !refresh && db != nil {
			snap, _ = db.LoadRepoSnapshotExact(context.Background(), storeProvider, storeHost, owner, name,
				m.auth.APIVersion, m.auth.AuthMode, m.auth.AuthScopeID)
		}
		if snap != nil && snap.Parent != nil && len(snap.Forks) > 0 && time.Since(snap.ForksSyncedAt) < forkListTTL {
			// This path returns without calling provider.Parent, which is
			// what normally latches the upstream baseline onto the provider.
			// Restore it from the snapshot, or every Compare below is issued
			// against an empty upstream and 404s. The ok-guard only tolerates
			// test doubles — the real providers implement the setter.
			baseOwner, baseName := owner, name
			baseBranch := snap.Parent.DefaultBranch
			if src := snap.Parent.SourceFullPath; src != "" {
				if o, n, ok := splitFullName(src); ok {
					baseOwner, baseName = o, n
					if snap.Parent.SourceDefaultBranch != "" {
						baseBranch = snap.Parent.SourceDefaultBranch
					}
				}
			}
			if setter, ok := provider.(forge.CompareBaselineSetter); ok {
				setter.SetCompareBaseline(baseOwner, baseName, baseBranch)
			}
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
		Provider:          providerName,
		Host:              host,
		Owner:             parts[0],
		Name:              parts[1],
		FirstSeen:         now,
		LastSeen:          now,
		ForksSyncedAt:     syncedAt,
		APIVersion:        m.auth.APIVersion,
		AcquisitionMethod: m.auth.AuthMode,
		AuthScopeID:       m.auth.AuthScopeID,
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
		snap := store.SnapshotFromForge(repo, f.Fork, nil, f.Heat.Score, f.Heat.Tier, now)
		attachDocument(&snap, f)
		snaps = append(snaps, snap)
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
	attachDocument(&snap, f)
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
		var report *forge.AcquisitionReport
		seen := make(map[string]struct{})
		for msg := range ch {
			if msg.Err != nil {
				streamErr = msg.Err // remember; per-fork errors are tolerated below
				continue
			}
			// msg.Report is a terminal message with zero Fork; it carries acquisition
			// metadata and must not be treated as a fork.
			if msg.Report != nil {
				report = msg.Report
				continue
			}
			// One row per fork is assumed by everything downstream: a repeat
			// becomes a second scored row, gets its own compare, and the
			// result lands only on the first copy (processPendingUpdates
			// matches by ID), leaving enriched/stub twins in the export.
			// Providers dedup; this guards against one that doesn't.
			if _, dup := seen[msg.Fork.ID]; dup {
				continue
			}
			seen[msg.Fork.ID] = struct{}{}
			forks = append(forks, msg.Fork)
		}

		// A fatal fetch failure (e.g. the GraphQL forks query erroring out)
		// arrives as a stream error and would otherwise leave us silently
		// showing zero forks. Surface it instead — but only when nothing came
		// through, so partial results from per-fork failures are still kept.
		if len(forks) == 0 && streamErr != nil {
			return forksFetchedMsg{err: streamErr}
		}

		// Report-only message (e.g. zero-fork repo with acquisition metadata).
		if len(forks) == 0 && report != nil {
			return forksFetchedMsg{Report: report}
		}

		// Partial result: forks arrived but the stream then errored. Keep the
		// list but surface a non-fatal warning so the user knows it was cut short
		// rather than silently trusting an incomplete list.
		if streamErr != nil {
			return forksFetchedMsg{forks: forks, warn: streamErr, Report: report}
		}

		return forksFetchedMsg{forks: forks, Report: report}
	}
}

func (m *Model) doRefresh() tea.Cmd {
	m.refresh = true
	m.cancelEnrichment()
	// m.forks and m.parent are NOT wiped here. startFetch is conditional on
	// m.refresh and keeps the existing list visible during the round-trip;
	// wiping here would race the wipe in startFetch and pull the rug out
	// from under the user's table view before the new forks arrive.
	m.enrichDone = 0

	// Results buffered from the run cancelEnrichment just cancelled describe
	// forks in the slice being thrown away. Keeping them would apply a dead
	// run's compares to the new list.
	m.pendingUpdates = m.pendingUpdates[:0]

	// The ranking was computed against the discarded list. Scores die with the
	// fork slice either way, but rankApplied and a "relevance" sort column
	// would survive and keep the footer naming a query that no longer orders
	// anything.
	m.rankApplied = ""
	m.rankMethod = ""
	m.rankScores = nil
	if m.sortCol == "relevance" {
		m.sortCol = "heat"
		m.sortAsc = false
	}

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
	m.reapplySort()
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

	// Ceiling T1 spends nothing, so dispatch nothing: marking the forks here
	// beats launching N closures that would each immediately report a skip.
	// enriching stays false so handleForksFetched's nil-command branch still
	// reaches the cluster pipeline.
	if m.maxTier() < 2 {
		for i := range m.forks {
			m.forks[i].Enriching = false
			// Only forks without cached T2 (applyCachedCompares runs first) are
			// actually skipped by the ceiling; the rest already have their compare.
			m.forks[i].TierSkipped = m.forks[i].T2 == nil
		}
		m.enriching = false
		m.enrichDone, m.enrichTotal = 0, 0
		return nil
	}

	// Force the ceiling atomic into existence before any compareCmd closure
	// captures m.tierCeiling below: a nil pointer captured here would never
	// observe a later `c` press, since setMaxTier would go on to allocate a
	// fresh atomic that the already-built closures never see.
	m.setMaxTier(m.maxTier())

	ctx, cancel := context.WithCancel(context.Background())
	m.enrichCtx = ctx
	m.enrichCancel = cancel
	m.enriching = true
	m.enrichDone = 0

	if len(m.forks) == 0 {
		m.enriching = false
		return nil
	}

	// Only forks without a compare need one: applyCachedCompares has already
	// served every fork whose stored compare is still valid (and on a
	// refresh nothing was cached, so every fork qualifies).
	parentPushed := m.parent.PushedAt
	var order []enrichEntry
	for i := range m.forks {
		if m.forks[i].T2 != nil {
			continue
		}
		m.forks[i].Enriching = true
		order = append(order, enrichEntry{
			fork:     m.forks[i].Fork,
			priority: forksops.DispatchPriority(m.forks[i].Fork, parentPushed, m.forks[i].Heat.Score),
		})
	}
	m.enrichTotal = len(order)
	if len(order) == 0 {
		m.enriching = false
		return nil
	}
	// Best-first: enrichQueue hands forks out in this order, so when the
	// rate-limit reserve cuts the pass short, the forks that DID get compared
	// are the ones most likely to have real divergence.
	sort.SliceStable(order, func(a, b int) bool { return order[a].priority > order[b].priority })

	concurrency := m.auth.Concurrency
	if concurrency <= 0 {
		concurrency = 2
	}
	m.enrichSem = make(chan struct{}, concurrency)
	m.enrichQueue = &enrichQueue{}
	m.batchStats, m.batchErr = nil, ""

	// Resolve divergence for the whole list in one GraphQL sweep first,
	// as `spn forks list` does: forks with nothing ahead then never spend a
	// REST compare, and divergent ones carry a pre-chosen branch.
	if bp, ok := m.provider.(forge.BatchCompareProvider); ok {
		m.batchResolving = true
		return tea.Batch(batchTick(), batchDivergenceCmd(ctx, bp, order))
	}
	return tea.Batch(append([]tea.Cmd{batchTick()}, m.dispatchQueued(order)...)...)
}

func (m *Model) cancelEnrichment() {
	if m.enrichCancel != nil {
		m.enrichCancel()
		m.enrichCancel = nil
	}
	m.enriching = false
	m.batchResolving = false
	// Drop the limiter and queue with the run they belonged to, so a fresh
	// fetch builds new ones rather than inheriting slots held by dead
	// closures.
	m.enrichSem = nil
	m.enrichQueue = nil
}

// cycleMaxTier steps the enrichment ceiling T3 → T2 → T1 → T3.
//
// Lowering never cancels in-flight compares and never discards T2 already
// fetched: cancelling would strand the batch mid-flight and block clustering,
// and discarding would re-arm the no-ahead penalty, changing scores the user
// did not ask to change while throwing away requests already paid for. What
// changes immediately is scoring (the T3 lone-wolf component) and what future
// compares are allowed to spend.
func (m *Model) cycleMaxTier() tea.Cmd {
	next := m.maxTier() - 1
	if next < 1 {
		next = 3
	}
	m.setMaxTier(next)
	m.rescoreAllEnriched()

	switch next {
	case 3:
		m.errMsg = "enrichment ceiling T3 - full scoring"
	case 2:
		m.errMsg = "enrichment ceiling T2 - lone-wolf scoring off"
	default:
		m.errMsg = "enrichment ceiling T1 - no further compares will be fetched"
	}
	m.errMsgTime = time.Now()

	if next >= 2 {
		return m.reenrichPending()
	}
	return nil
}

// rescoreAllEnriched re-derives the heat score of every fork that already has
// T2 data, under the current ceiling, then re-establishes the list ordering
// and the cursor. No network: raising 2→3 or lowering 3→2 only changes which
// scoring components are wired.
func (m *Model) rescoreAllEnriched() {
	var selectedID string
	if m.cursor >= 0 && m.cursor < len(m.forks) {
		selectedID = m.forks[m.cursor].Fork.ID
	}
	for i := range m.forks {
		if m.forks[i].T2 != nil {
			m.recomputeT2Score(i)
		}
	}
	m.assignDuplicateGroups()
	m.reapplySort()
	m.restoreCursorByID(selectedID)
}

// reenrichPending dispatches compares for forks that were skipped at a lower
// ceiling. Errored forks are deliberately excluded: retrying them is a
// separate concern and would risk a loop on a fork that fails every time.
func (m *Model) reenrichPending() tea.Cmd {
	if m.parent == nil || m.provider == nil {
		return nil
	}
	var pending []int
	for i := range m.forks {
		sf := m.forks[i]
		if !sf.Enriched && !sf.Enriching && (sf.TierSkipped || sf.BudgetSkipped) {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	// Reuse the live context and limiter when a pass is still running; minting
	// new ones would orphan the original cancel func (so q/r would stop
	// cancelling the first batch) and double the effective concurrency.
	if m.enrichCtx == nil || m.enrichCtx.Err() != nil {
		ctx, cancel := context.WithCancel(context.Background())
		m.enrichCtx = ctx
		m.enrichCancel = cancel
	}
	if m.enrichSem == nil {
		concurrency := m.auth.Concurrency
		if concurrency <= 0 {
			concurrency = 2
		}
		m.enrichSem = make(chan struct{}, concurrency)
	}

	wasEnriching := m.enriching
	if wasEnriching {
		m.enrichTotal += len(pending)
	} else {
		m.enrichDone, m.enrichTotal = 0, len(pending)
	}
	m.enriching = true

	cmds := make([]tea.Cmd, 0, len(pending)+1)
	// Only start a tick chain when one is not already running: two concurrent
	// chains would double the batch-apply rate.
	if !wasEnriching {
		cmds = append(cmds, batchTick())
	}
	parentPushed := m.parent.PushedAt
	entries := make([]enrichEntry, 0, len(pending))
	for _, i := range pending {
		m.forks[i].Enriching = true
		m.forks[i].TierSkipped = false
		m.forks[i].BudgetSkipped = false
		entries = append(entries, enrichEntry{
			fork:     m.forks[i].Fork,
			priority: forksops.DispatchPriority(m.forks[i].Fork, parentPushed, m.forks[i].Heat.Score),
		})
	}
	// Appended to a running pass's queue, these are ordered against the
	// forks it has not reached yet rather than queued behind them.
	cmds = append(cmds, m.dispatchQueued(entries)...)
	return tea.Batch(cmds...)
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
	compareBase, compareBranch := m.parent.FullName, m.parent.DefaultBranch
	if m.parent.SourceFullPath != "" {
		compareBase = m.parent.SourceFullPath
		if m.parent.SourceDefaultBranch != "" {
			compareBranch = m.parent.SourceDefaultBranch
		}
	}
	url := forge.CompareURL(m.auth.Provider, m.auth.Host,
		compareBase, compareBranch,
		fork.Owner, fork.DefaultBranch)
	return func() tea.Msg {
		b := browser.New("", os.Stdout, os.Stderr)
		_ = b.Browse(url)
		return nil
	}
}

// --- View ---

func (m Model) View() string {
	if ui.TooSmall(m.width, m.height) {
		return ui.FallbackMessageFor(m.themeContext(), m.width, m.height)
	}
	if m.quitting {
		return ""
	}
	if m.overlay.IsOpen() {
		width := ui.ContentWidth(m.width)
		if m.overlay.Kind() == ui.SheetOverlay {
			return ui.Sheet(m.themeContext(), "Help", m.overlayTitle, width)
		}
		return ui.Modal(m.themeContext(), "Confirm", m.overlayTitle, width)
	}

	switch m.view {
	case viewInput:
		return m.viewInput()
	case viewTable:
		return m.viewTable()
	case viewDetail:
		return m.viewDetail()
	case viewPatch:
		return m.viewPatch()
	case viewExportPath:
		return m.viewExportPath()
	case viewTopicPicker:
		return m.viewTopicPicker()
	case viewFilter:
		return m.viewFilterPrompt()
	case viewRank:
		return m.viewRankPrompt()
	case viewHelp:
		return m.viewHelp()
	case viewSettings:
		return m.settings.View()
	}
	return ""
}

func (m Model) viewInput() string {
	var b strings.Builder
	s := m.styles()

	b.WriteString("\n")
	b.WriteString(s.title.Render("  spoon"))
	b.WriteString(s.subtitle.Render(" " + m.themeContext().Glyph(theme.EmDash) + " find useful forks"))
	b.WriteString("\n\n")

	if m.authMsg != "" {
		if m.auth.Authenticated() {
			b.WriteString("  " + s.subtitle.Render(m.authMsg) + "\n\n")
		} else {
			b.WriteString("  " + s.warn.Render("! ") + m.authMsg + "\n\n")
		}
	}

	b.WriteString("  Repository: " + ui.Input(m.themeContext(), ui.InputState{
		Value: m.input, Cursor: m.inputCursor, Focused: true, Enabled: true,
	}, ui.ContentWidth(m.width)-14) + "\n")
	if m.inputErr != "" {
		b.WriteString("  " + ui.TitledAlert(m.themeContext(), ui.AlertError, "Repository", m.inputErr, ui.ContentWidth(m.width)-2) + "\n")
	}
	if m.errMsg != "" {
		b.WriteString("  " + ui.TitledAlert(m.themeContext(), ui.AlertError, "Operation", m.errMsg, ui.ContentWidth(m.width)-2) + "\n")
	}
	if m.loading {
		b.WriteString("  " + ui.Alert(m.themeContext(), ui.AlertInfo, m.loadMsg, ui.ContentWidth(m.width)-2) + "\n")
	} else {
		b.WriteString("\n  " + ui.KeyLegend(m.themeContext(), ui.ContentWidth(m.width)-2, keymap.MainInput) + "\n")
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
