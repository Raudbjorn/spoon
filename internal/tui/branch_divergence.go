package tui

import (
	"context"
	"strings"

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
		counts, fps, err := prov.DivergentBranchCounts(ctx, pending)
		return branchDivergenceMsg{counts: counts, fingerprints: fps, err: err}
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
		// re-sort now that it has landed.
		m.assignDuplicateGroups()
		m.sortForks()
		m.persistForkListCounts()
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

	ghForks := make([]gh.ForkInfo, 0, len(m.forks))
	ghExtras := make(map[int64]gh.T1Extra, len(m.forks))
	for _, sf := range m.forks {
		ghF := forgeT1ToGHForkInfo(sf.Fork)
		ghForks = append(ghForks, ghF)
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
