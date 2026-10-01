package tui

import (
	"context"
	"errors"
	"io"
	"sort"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
)

// enrichEntry is one fork waiting for a live compare.
type enrichEntry struct {
	fork     forge.T1Data
	priority float64
	// sel is the branch the GraphQL divergence batch chose for this fork,
	// or nil when the batch did not run or could not resolve it (the
	// compare then scans branches itself, as it did before batching).
	sel *forge.BranchSelection
}

// enrichQueue hands forks to compare commands in descending priority order.
// Entries are taken in that order; with a concurrency limit above one, the
// compares they start can still swap places by up to concurrency-1 positions,
// so the reserve floor is a soft, priority-biased cutoff rather than an exact
// one.
//
// Every compare command is launched up front by tea.Batch and the commands
// race for the concurrency semaphore in no particular order. Binding a
// command to a fork when it is built therefore made dispatch order random:
// when the rate-limit reserve cut the pass short, which forks had been
// compared was luck. Instead each command takes the next entry only after it
// holds a semaphore slot, so work starts in priority order no matter which
// command wins the race, and the reserve trips on the least promising forks.
//
// Invariant: exactly one pop per command. The number of commands issued
// always equals the number of entries pushed, so pop never runs dry and
// every entry yields exactly one tier2ResultMsg -- processPendingUpdates only
// finishes the pass (and starts clustering) once enrichDone reaches
// enrichTotal.
type enrichQueue struct {
	mu      sync.Mutex
	entries []enrichEntry
	next    int
}

// push appends entries and re-sorts everything not yet handed out, so a
// re-enrichment appended mid-pass is ordered against the remaining forks
// rather than queued behind them.
func (q *enrichQueue) push(entries ...enrichEntry) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.entries = append(q.entries, entries...)
	rest := q.entries[q.next:]
	sort.SliceStable(rest, func(a, b int) bool { return rest[a].priority > rest[b].priority })
}

// pop returns the highest-priority entry not yet handed out. ok is false
// only if the one-pop-per-command invariant was broken.
func (q *enrichQueue) pop() (enrichEntry, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.next >= len(q.entries) {
		return enrichEntry{}, false
	}
	e := q.entries[q.next]
	q.next++
	return e, true
}

// errEnrichQueueEmpty reports a broken one-pop-per-command invariant. It is
// surfaced as a result with no fork rather than a panic so a bug here
// degrades one progress count instead of killing the TUI.
var errEnrichQueueEmpty = errors.New("enrichment queue empty: more compare commands than queued forks")

// batchDivergenceMsg carries the GraphQL divergence batch result back to the
// update loop, where it is applied before any live compare is dispatched.
type batchDivergenceMsg struct {
	// ctx identifies the enrichment pass that asked; a result for a pass
	// that has since been cancelled or replaced is dropped.
	ctx        context.Context
	order      []enrichEntry
	divergence map[string]forge.ForkDivergence
	stats      forge.BatchStats
	err        error
	// missing names forks whose repository no longer resolves at all.
	missing map[string]bool
}

// batchChunkForks is how many forks one divergence-batch step covers.
//
// The batch is sequential GraphQL, about one query per 50 branches. Over
// the whole of ggml-org/llama.cpp (20,433 forks) it took 42 minutes (873
// queries, 2026-09-26) before a single compare could start. In chunks of
// this size, in priority order, the most promising forks start comparing
// after roughly one minute, and later chunks resolve while earlier compares
// run.
const batchChunkForks = 500

// nextBatchChunk takes the next chunk of forks still awaiting the divergence
// batch and returns the command that resolves it, or nil when none remain.
func (m *Model) nextBatchChunk() tea.Cmd {
	if len(m.batchPending) == 0 || m.batchProvider == nil {
		return nil
	}
	// Ceiling lowered to T1 mid-pass: stop spending GraphQL on forks that
	// will not be compared. Each still reports once, as a ceiling skip, so
	// enrichDone reaches enrichTotal and raising the ceiling re-enriches them.
	if m.maxTier() < 2 {
		for _, e := range m.batchPending {
			m.pendingUpdates = append(m.pendingUpdates, tier2ResultMsg{forkID: e.fork.ID, tierSkipped: true})
		}
		m.batchPending = nil
		return nil
	}
	// Reserve floor: the batch spends GraphQL budget too (hundreds of
	// queries on a large network), and Headroom is the lower of the REST and
	// GraphQL windows. Below the floor, settle what is left as budget-skipped,
	// as the per-fork compare does, rather than draining the window further.
	if m.provider.Headroom() < forksops.ReserveHeadroom {
		for _, e := range m.batchPending {
			m.pendingUpdates = append(m.pendingUpdates, tier2ResultMsg{forkID: e.fork.ID, budgetSkipped: true})
		}
		m.batchPending = nil
		return nil
	}
	n := min(batchChunkForks, len(m.batchPending))
	chunk := m.batchPending[:n:n]
	m.batchPending = m.batchPending[n:]
	return batchDivergenceCmd(m.enrichCtx, m.batchProvider, chunk)
}

// batchDivergenceCmd resolves ahead/behind for one chunk of forks in batched
// GraphQL (about one query per 50 branches) instead of one REST compare per
// fork. Forks the batch finds have nothing ahead need no REST call at all.
func batchDivergenceCmd(ctx context.Context, bp forge.BatchCompareProvider, order []enrichEntry) tea.Cmd {
	return func() tea.Msg {
		forks := make([]forge.T1Data, len(order))
		for i, e := range order {
			forks[i] = e.fork
		}
		div, stats, err := bp.BatchCompare(ctx, forks)
		msg := batchDivergenceMsg{ctx: ctx, order: order, divergence: div, stats: stats, err: err}
		// Forks the batch could not resolve are the only candidates for a
		// vanished repository; checking just those keeps the lookup cheap.
		// A lookup failure leaves missing nil: every fork then gets its
		// compare as before, nothing is marked on a guess.
		if mp, ok := bp.(forge.MissingReposProvider); ok {
			var unresolved []forge.T1Data
			for _, f := range forks {
				if d, found := div[f.ID]; !found || !d.Resolved {
					unresolved = append(unresolved, f)
				}
			}
			if len(unresolved) > 0 {
				msg.missing, _ = mp.MissingRepos(ctx, unresolved)
			}
		}
		return msg
	}
}

// queuedCompareCmd builds one compare command. Which fork it compares is
// decided when it runs (see enrichQueue), not when it is built.
//
// Gate order mirrors forksops/stream.go: a stored compare costs no API budget,
// so it is consulted before the ceiling and the reserve floor.
func (m *Model) queuedCompareCmd(q *enrichQueue) tea.Cmd {
	provider := m.provider
	refresh := m.refresh
	cached := m.cached
	ctx := m.enrichCtx
	sem := m.enrichSem
	ceiling := m.tierCeiling

	// Read through the same clamping rule maxTier uses; a nil ceiling means
	// no limit was ever set.
	tierAllows := func() bool {
		if ceiling == nil {
			return defaultMaxTier >= 2
		}
		return clampInt(int(ceiling.Load()), 1, 3) >= 2
	}

	return func() tea.Msg {
		// Hold a slot before taking a fork: taking it first would let a
		// high-priority fork wait on the semaphore behind a lower one.
		// A cancelled pass still pops, so its entry reports exactly once.
		select {
		case <-ctx.Done():
		case sem <- struct{}{}:
			defer func() { <-sem }()
		}
		e, ok := q.pop()
		if !ok {
			return tier2ResultMsg{err: errEnrichQueueEmpty}
		}
		f := e.fork
		// Checked after the select, not only in it: select picks at random
		// when both cases are ready, so a cancelled pass could otherwise
		// still win a free slot and spend a compare.
		if err := ctx.Err(); err != nil {
			return tier2ResultMsg{forkID: f.ID, err: err}
		}

		if !refresh {
			if t2 := cached.ValidT2(f); t2 != nil {
				return tier2ResultMsg{forkID: f.ID, t2: *t2, fromCache: true}
			}
		}

		// The user's ceiling, read here rather than when this closure was
		// built: every command is handed to tea.Batch up front, so lowering
		// the ceiling mid-run can only take effect if the value is read at
		// execution time.
		if !tierAllows() {
			return tier2ResultMsg{forkID: f.ID, tierSkipped: true}
		}

		// Auto-budget reserve: stop spending the rate window once headroom
		// hits the floor. Forks are taken in priority order, so the ones
		// already compared are the most promising; this one is marked
		// degraded (not failed, not zeroed) so the export can say so.
		if provider.Headroom() < forksops.ReserveHeadroom {
			return tier2ResultMsg{forkID: f.ID, budgetSkipped: true}
		}

		t2, err := forksops.CompareFork(ctx, provider, f, e.sel, io.Discard)
		return tier2ResultMsg{forkID: f.ID, t2: t2, err: err}
	}
}

// dispatchQueued pushes entries onto the pass's queue and returns one compare
// command per entry.
func (m *Model) dispatchQueued(entries []enrichEntry) []tea.Cmd {
	if len(entries) == 0 {
		return nil
	}
	if m.enrichQueue == nil {
		m.enrichQueue = &enrichQueue{}
	}
	m.enrichQueue.push(entries...)
	cmds := make([]tea.Cmd, len(entries))
	for i := range entries {
		cmds[i] = m.queuedCompareCmd(m.enrichQueue)
	}
	return cmds
}

// handleBatchDivergence applies the batch result: forks it fully resolved
// (nothing ahead of upstream) are settled here with no API call, and only
// forks that still need a live compare are queued.
func (m *Model) handleBatchDivergence(msg batchDivergenceMsg) (tea.Model, tea.Cmd) {
	if msg.ctx != m.enrichCtx || m.enrichCtx == nil || m.enrichCtx.Err() != nil {
		return m, nil // a cancelled or superseded pass
	}
	if m.batchStats == nil {
		m.batchStats = &forge.BatchStats{}
	}
	m.batchStats.Queries += msg.stats.Queries
	m.batchStats.Cost += msg.stats.Cost
	m.batchChunksDone++
	if msg.err != nil && !errors.Is(msg.err, forge.ErrBatchCompareUnavailable) {
		// Degraded, not fatal: whatever the batch did resolve still applies,
		// and every other fork falls back to a per-fork compare.
		m.batchErr = msg.err.Error()
	}

	rest := make([]enrichEntry, 0, len(msg.order))
	for _, e := range msg.order {
		res, ok := forksops.ResolveFromBatch(msg.divergence, e.fork.ID)
		switch {
		case msg.missing[e.fork.ID]:
			m.pendingUpdates = append(m.pendingUpdates, tier2ResultMsg{forkID: e.fork.ID, unreachable: true})
		case ok && res.T2 != nil && !forge.HasBranchInventory(e.fork):
			// A zero-ahead default branch only settles a fork whose side
			// branches were listed too. Without that inventory (REST-listed
			// rows) work may sit on a side branch the batch never paired, so
			// the fork takes the full compare, which scans branches itself.
			rest = append(rest, e)
		case ok && res.T2 != nil:
			// Settled through the same apply/persist path as a REST result,
			// so the no_ahead penalty and the store write both happen.
			m.pendingUpdates = append(m.pendingUpdates, tier2ResultMsg{forkID: e.fork.ID, t2: *res.T2})
		case ok:
			e.sel = res.Selection
			rest = append(rest, e)
		default:
			rest = append(rest, e)
		}
	}
	cmds := m.dispatchQueued(rest)
	if next := m.nextBatchChunk(); next != nil {
		cmds = append(cmds, next)
	} else {
		m.batchResolving = false
	}
	return m, tea.Batch(cmds...)
}
