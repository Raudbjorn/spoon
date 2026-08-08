package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
)

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
	//
	// A fork with zero divergent branches legitimately has no fingerprint —
	// there is no work to fingerprint — so an empty fingerprint only counts as
	// missing when the fork actually has divergent branches. Testing the
	// fingerprint unconditionally would re-sweep every inert mirror on every
	// run, which is most of a typical fork network.
	pending := make([]forge.T1Data, 0, len(m.forks))
	for _, sf := range m.forks {
		count := sf.Fork.DivergentBranches
		needsSweep := count == nil || (*count > 0 && sf.Fork.BranchFingerprint == "")
		if needsSweep {
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
		m.persistForkListCounts()
	}

	if len(msg.truncated) > 0 {
		m.errMsg = fmt.Sprintf("%d fork(s) have more branches than could be listed; their BRANCH counts are lower bounds", len(msg.truncated))
		m.errMsgTime = time.Now()
	}
	return m, nil
}

// persistForkListCounts rewrites the cached fork list so the newly obtained
// branch counts are available on the next run without another sweep.
//
// SaveForkList resets the entry's FetchedAt, which also renews the fork-list
// TTL. That is correct here: the fork list being written is the one just
// fetched or just validated, not stale data being laundered as fresh.
func (m *Model) persistForkListCounts() {
	if m.auth.Provider != forge.ProviderGitHub || m.parent == nil {
		return
	}
	owner, name, ok := splitFullName(m.parent.FullName)
	if !ok {
		return
	}

	// m.forks is not the full fork list: scoreForks drops ghost forks before
	// scoring, and SaveForkList replaces the stored Forks/T1Extras wholesale.
	// Rebuilding the cache from m.forks alone would therefore silently evict
	// every ghost from disk on the first sweep. Start from the on-disk list and
	// overlay the rows this model actually holds, keyed by FullName (the IDs
	// are hashes of it, so the two are interchangeable as keys).
	var ghForks []gh.ForkInfo
	ghExtras := make(map[int64]gh.T1Extra, len(m.forks))
	if existing := gh.LoadCache(owner, name); existing != nil {
		ghForks = existing.Forks
		for id, extra := range existing.T1Extras {
			ghExtras[id] = extra
		}
	}
	index := make(map[string]int, len(ghForks))
	for i, f := range ghForks {
		index[f.FullName] = i
	}

	for _, sf := range m.forks {
		ghF := forgeT1ToGHForkInfo(sf.Fork)
		if i, ok := index[ghF.FullName]; ok {
			ghForks[i] = ghF
		} else {
			ghForks = append(ghForks, ghF)
		}
		if sf.Fork.OpenPRCount > 0 || sf.Fork.ReleaseCount > 0 ||
			len(sf.Fork.Branches) > 0 || sf.Fork.DivergentBranches != nil {
			ghExtras[ghF.ID] = forgeT1ToGHExtra(sf.Fork)
		}
	}
	_ = gh.SaveForkList(owner, name, forgeParentToGHRepoInfo(*m.parent), ghForks, ghExtras)
}

// splitFullName splits "owner/name", reporting false for anything else.
func splitFullName(full string) (owner, name string, ok bool) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
