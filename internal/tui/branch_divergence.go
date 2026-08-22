package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/forge"
)

// needsBranchSweep reports whether a fork's divergence data is still missing
// and the sweep should include it.
//
// A fork with zero divergent branches legitimately has no fingerprint — there
// is no work to fingerprint — so an empty fingerprint only counts as missing
// when the fork actually has divergent branches. Testing the fingerprint
// unconditionally would re-sweep every inert mirror on every run, which is
// most of a typical fork network.
func needsBranchSweep(f forge.T1Data) bool {
	count := f.DivergentBranches
	return count == nil || (*count > 0 && f.BranchFingerprint == "")
}

// startBranchDivergenceSweep counts, for every loaded fork, how many of its
// branches carry commits the upstream lacks.
//
// This is deliberately not gated on rate-limit headroom the way the REST branch
// scan is (github.minHeadroom): the whole sweep is two GraphQL queries for the
// entire fork network no matter how large, so there is nothing to budget.
//
// Returns nil when the provider cannot answer (GitLab, Gitea) or when every
// fork already carries a cached count.
func (m *Model) startBranchDivergenceSweep() tea.Cmd {
	prov, ok := m.provider.(forge.BranchDivergenceProvider)
	if !ok || len(m.forks) == 0 {
		return nil
	}

	// Only sweep forks whose data is still missing. On a cache hit that is
	// usually none, so a second run costs nothing.
	pending := make([]forge.T1Data, 0, len(m.forks))
	for _, sf := range m.forks {
		if needsBranchSweep(sf.Fork) {
			pending = append(pending, sf.Fork)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	ctx := m.lifecycleCtx
	if ctx == nil {
		ctx = context.Background()
	}

	return func() tea.Msg {
		counts, fps, truncated, err := prov.DivergentBranchCounts(ctx, pending)
		return branchDivergenceMsg{counts: counts, fingerprints: fps, truncated: truncated, err: err}
	}
}

// handleBranchDivergence applies sweep results and re-persists the fork list so
// the counts survive into the next run.
func (m *Model) handleBranchDivergence(msg branchDivergenceMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		// Non-fatal: the column renders "-" and everything else proceeds. The
		// count is a convenience, not core data.
		return m, nil
	}

	applied := false
	for i := range m.forks {
		id := m.forks[i].Fork.ID
		if n, ok := msg.counts[id]; ok {
			count := n
			m.forks[i].Fork.DivergentBranches = &count
			applied = true
		}
		if fp, ok := msg.fingerprints[id]; ok && fp != "" {
			m.forks[i].Fork.BranchFingerprint = fp
			applied = true
		}
	}

	if applied {
		// The fingerprint is the strongest duplicate key, so re-group and
		// re-sort now that it has landed. This runs asynchronously after the
		// table is already on screen, so capture the fork under the cursor and
		// restore it afterward — otherwise a re-sort landing between the user
		// looking at a row and acting on it (space to mark, enter to open)
		// would apply to whichever fork the reorder happened to leave there.
		var selectedID string
		if m.cursor >= 0 && m.cursor < len(m.forks) {
			selectedID = m.forks[m.cursor].Fork.ID
		}
		m.assignDuplicateGroups()
		m.reapplySort()
		m.restoreCursorByID(selectedID)
	}

	if len(msg.truncated) > 0 {
		m.errMsg = fmt.Sprintf("%d fork(s) have more branches than could be listed; their BRANCH counts are lower bounds", len(msg.truncated))
		m.errMsgTime = time.Now()
	}
	if applied {
		// Re-persist the fork list (T1-only snapshots) so the counts and
		// fingerprints are available on the next run without another sweep.
		// Store rows are per-fork upserts, so ghost forks filtered out of
		// m.forks keep their existing rows untouched.
		return m, m.persistForkList()
	}
	return m, nil
}

// splitFullName splits "owner/name", reporting false for anything else.
func splitFullName(full string) (owner, name string, ok bool) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// needsLinearHistorySweep reports whether a fork's linear-history data is
// still missing and the sweep should include it. LinearHistory being set
// already implies the vector was computed once, so re-sweeping would be
// pure network waste. The MergeCommitHistory nil check is belt-and-braces:
// a fork whose vector was populated but the boolean never derived should
// re-derive rather than re-fetch, but that path is rare enough to skip.
func needsLinearHistorySweep(f forge.T1Data) bool {
	return f.LinearHistory == nil && f.MergeCommitHistory == nil
}

// linearHistoryMsg is the result of a linear-history sweep.
type linearHistoryMsg struct {
	histories map[string][]int
	truncated []string
	err       error
}

// startLinearHistorySweep fetches, for every loaded fork, the raw parents.
// totalCount vector for the fork's ahead-of-upstream history. Costs one
// GraphQL compare query per linearHistoryBatchSize forks, so no per-fork
// budget gate is needed.
//
// Returns nil when the provider cannot answer (GitLab, Gitea) or when every
// fork already carries a derived LinearHistory boolean.
func (m *Model) startLinearHistorySweep() tea.Cmd {
	prov, ok := m.provider.(forge.LinearHistoryProvider)
	if !ok || len(m.forks) == 0 {
		return nil
	}

	// Only sweep forks whose data is still missing. A cache hit is usually
	// every fork, so a second run costs nothing.
	pending := make([]forge.T1Data, 0, len(m.forks))
	for _, sf := range m.forks {
		if needsLinearHistorySweep(sf.Fork) {
			pending = append(pending, sf.Fork)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	ctx := m.lifecycleCtx
	if ctx == nil {
		ctx = context.Background()
	}

	return func() tea.Msg {
		histories, truncated, err := prov.MergeCommitHistory(ctx, pending)
		return linearHistoryMsg{histories: histories, truncated: truncated, err: err}
	}
}

// handleLinearHistory applies sweep results, derives MergeCommits and
// LinearHistory per fork, and re-persists so the relational history table
// and the forks-row columns survive into the next run.
func (m *Model) handleLinearHistory(msg linearHistoryMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		// Non-fatal: the column renders "-" and everything else proceeds. The
		// boolean is a convenience, not core data.
		return m, nil
	}

	truncatedSet := make(map[string]bool, len(msg.truncated))
	for _, id := range msg.truncated {
		truncatedSet[id] = true
	}

	applied := false
	for i := range m.forks {
		id := m.forks[i].Fork.ID
		hist, ok := msg.histories[id]
		if !ok {
			continue
		}
		m.forks[i].Fork.MergeCommitHistory = hist
		count := 0
		for _, p := range hist {
			if p >= 2 {
				count++
			}
		}
		m.forks[i].Fork.MergeCommits = count
		linear := count == 0
		m.forks[i].Fork.LinearHistory = &linear
		if truncatedSet[id] {
			m.forks[i].Fork.MergeCommitTruncated = true
		}
		applied = true
	}

	if !applied {
		return m, nil
	}

	// Sort order may have shifted: linear forks land first when sorted by
	// the LIN column, and a refresh that re-sorts now keeps the table in
	// sync with the new data without a redraw race. Capture the cursor
	// under the row so the re-sort doesn't move the user's selection.
	var selectedID string
	if m.cursor >= 0 && m.cursor < len(m.forks) {
		selectedID = m.forks[m.cursor].Fork.ID
	}
	m.reapplySort()
	m.restoreCursorByID(selectedID)

	if len(msg.truncated) > 0 {
		m.errMsg = fmt.Sprintf("%d fork(s) have a longer merge history than could be fetched; LIN counts are lower bounds", len(msg.truncated))
		m.errMsgTime = time.Now()
	}
	// Re-persist so the relational merge_commit_history table and the
	// forks-row columns survive across sessions. The persistence layer
	// decides what to write; here we just signal "fork list changed".
	return m, m.persistForkList()
}

// deriveLinearHistoryFromCached walks the loaded forks and, for any fork
// that has a MergeCommitHistory vector but no LinearHistory boolean,
// computes the boolean locally without a network call. This handles the
// cache-hit path where the relational vector was loaded but the boolean
// was never derived (e.g. a TUI run that never completed a sweep).
func (m *Model) deriveLinearHistoryFromCached() {
	applied := false
	for i := range m.forks {
		f := &m.forks[i].Fork
		if f.LinearHistory != nil || f.MergeCommitHistory == nil {
			continue
		}
		count := 0
		for _, p := range f.MergeCommitHistory {
			if p >= 2 {
				count++
			}
		}
		f.MergeCommits = count
		linear := count == 0
		f.LinearHistory = &linear
		applied = true
	}
	if applied {
		m.reapplySort()
	}
}
