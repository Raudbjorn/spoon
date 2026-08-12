package tui

import (
	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/store"
)

// Message types for Bubble Tea update loop.

type authReadyMsg struct {
	provider forge.Forge
	auth     forge.AuthInfo
	err      error
}

// branchDivergenceMsg carries the result of the batched divergent-branch
// sweep. counts maps fork ID to the number of branches ahead of upstream; a
// fork absent from the map stayed unknown.
type branchDivergenceMsg struct {
	counts map[string]int
	// fingerprints maps fork ID to its divergent-branch identity; forks sharing
	// one carry identical work.
	fingerprints map[string]string
	// truncated lists fork IDs whose branch list was too large to enumerate in
	// full — their counts and fingerprints are lower bounds, not exact.
	truncated []string
	err       error
}

type parentFetchedMsg struct {
	parent forge.ParentData
	err    error
	// snap is the stored snapshot for this repo (nil on refresh or first run).
	// Carried so per-fork compares can be reused even when the fork list itself
	// is stale enough to refetch.
	snap *store.RepoSnapshot
}

type forksFetchedMsg struct {
	forks []forge.T1Data
	err   error
	// warn is a non-fatal warning to surface alongside a populated fork list —
	// e.g. the stream was cut short by an error after some forks arrived, so the
	// displayed list is partial. Distinct from err, which suppresses the list.
	warn error
}

type tier2ResultMsg struct {
	forkID string
	t2     forge.T2Data
	err    error
	// budgetSkipped is true when the compare was not attempted because the
	// rate-limit reserve floor was reached. Distinct from err: the fork is
	// kept, just marked un-enriched rather than failed.
	budgetSkipped bool
	// tierSkipped is true when the compare was not attempted because the
	// user's enrichment ceiling sat below T2. Distinct from budgetSkipped in
	// what undoes it: raising the ceiling re-enriches these immediately,
	// whereas a budget skip waits on the rate window. Both must produce a
	// message rather than a nil command -- processPendingUpdates only starts
	// the cluster pipeline once enrichDone reaches enrichTotal, so a skip
	// that reports nothing would freeze the progress counter and block
	// clustering forever.
	tierSkipped bool
	// fromCache marks a compare served from the store. It must not be
	// persisted back: cached T2s carry no patch text (the store's read path
	// skips it), and re-persisting would overwrite full rows with patch-less
	// ones — and the rows are identical anyway.
	fromCache bool
}

type startFetchMsg struct{}

type enrichBatchTickMsg struct{}

type enrichmentDoneMsg struct{}

type errMsg struct{ err error }

// cachedLoadMsg is used when the whole fork list is served from the store
// (parent + forks fresh within forkListTTL). snap keeps the cached compares
// for per-fork reuse.
type cachedLoadMsg struct {
	parent forge.ParentData
	forks  []forge.T1Data
	snap   *store.RepoSnapshot
}

// clusterResultMsg is published when the cluster pipeline finishes (or
// fails). The model handles it by setting cluster fields on each fork's
// HeatResult and triggering a re-render.
type clusterResultMsg struct {
	Assignments []cluster.Assignment
	Clusters    []cluster.Cluster
	Skip        *cluster.SkipReason // non-nil when clustering was skipped
	Err         error               // non-nil on hard failure
}
