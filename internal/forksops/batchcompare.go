package forksops

// Pre-dispatch GraphQL divergence batch.
//
// Before Stream dispatches workers to run the expensive per-fork compare,
// it optionally resolves every eligible fork's divergence in one GraphQL
// batch call (forge.BatchCompareProvider). A fork the batch finds has
// nothing ahead of upstream is fully resolved right there -- no REST call
// at all. A fork the batch finds divergent keeps the single branch the
// batch-derived policy (forge.SelectDivergentBranch) chose, so the REST
// step issues exactly one forge.ResolvedCompareProvider.CompareResolved
// call instead of forge.Forge.Compare's own per-fork branch scan.
//
// This file holds the pieces of that stage that don't need Stream's local
// state: the caller-owned report type, the estimate-line math, the
// per-fork lookup + branch-choice application, the zero-ahead T2
// synthesis, and the REST call itself (with its one-shot fallback to
// Compare). The pre-dispatch batch call and the worker call sites stay
// inline in Stream (stream.go) -- see the task-5 brief.

import (
	"context"
	"fmt"
	"io"

	"github.com/svnbjrn/spoon/internal/forge"
)

// CompareSummary tallies how each eligible fork's T2 compare was obtained
// during a Stream run: served from the cache, synthesised from the
// pre-dispatch GraphQL batch (zero ahead, no REST call), or fetched live
// over REST (via CompareResolved or the plain Compare fallback). Caller-
// owned, like Options.TouchReport -- Stream fills it in as the run
// completes; nil means the caller does not want it.
type CompareSummary struct {
	// Cached counts forks served from Options.CachedT2.
	Cached int
	// Batch counts forks fully resolved by the GraphQL batch (nothing
	// ahead of upstream) -- synthesised T2, no REST call made.
	Batch int
	// REST counts forks that made a live compare call, whether via
	// CompareResolved (a batch-chosen branch) or the plain Compare
	// fallback.
	REST int
	// DiffFallback counts REST compares whose file list was completed by
	// the unbounded .diff fallback (T2Data.FilesComplete == true).
	DiffFallback int
	// LastTouchSkipped is filled in by a later stage (--touching's
	// last-touch attribution pass); it stays 0 for a run that doesn't use
	// that stage.
	LastTouchSkipped int
	// BatchQueries and BatchCost mirror forge.BatchStats from the batch
	// call, for the acquisition report. Both 0 when no batch ran.
	BatchQueries int
	BatchCost    int
	// BatchError holds the batch call's error text when it returned
	// something other than forge.ErrBatchCompareUnavailable (that specific
	// error means "no GraphQL backend," and is not reported here -- it is
	// the expected, silent fallback path). Empty when the batch succeeded,
	// did not run, or was unavailable.
	BatchError string
}

// batchResolution is what the pre-dispatch batch determined for one fork.
type batchResolution struct {
	// T2 is set when the fork's selected branch had nothing ahead of
	// upstream: fully resolved from the batch, no REST call needed.
	T2 *forge.T2Data
	// Selection is set when the fork still needs a REST call, to the
	// single branch forge.SelectDivergentBranch chose (Selection.NeedsREST
	// is always true here).
	Selection *forge.BranchSelection
}

// resolveFromBatch looks up a fork's batch-resolved forge.ForkDivergence
// and applies the branch-choice policy. ok is false when the fork was
// absent from the batch result, or the batch could not resolve it
// (ForkDivergence.Resolved == false) -- both mean "no batch signal for
// this fork," so the caller falls back to the REST path exactly as it
// would without a batch at all.
func resolveFromBatch(divergence map[string]forge.ForkDivergence, forkID string) (batchResolution, bool) {
	d, ok := divergence[forkID]
	if !ok || !d.Resolved {
		return batchResolution{}, false
	}
	sel := forge.SelectDivergentBranch(d)
	if !sel.NeedsREST {
		t2 := synthesiseT2(d)
		return batchResolution{T2: &t2}, true
	}
	return batchResolution{Selection: &sel}, true
}

// synthesiseT2 builds the T2Data for a batch-resolved fork whose selected
// branch has nothing ahead of upstream. No REST call is needed: Behind and
// HeadSHA come straight from the batch's default-branch divergence (the
// same values forge.SelectDivergentBranch already returns for this case).
// Diffs is left nil -- an empty compare_files set is exact for zero ahead;
// see cmd/spn/forks.go's snapshot persistence, which only writes
// CompareFiles from Diffs.
func synthesiseT2(d forge.ForkDivergence) forge.T2Data {
	return forge.T2Data{
		Performed:     true,
		AheadCount:    0,
		BehindCount:   d.Default.BehindBy,
		HeadSHA:       d.Default.TipSHA,
		CompareSource: "graphql_batch",
	}
}

// compareFork issues the one live REST compare call a fork still needs
// after the cache and batch stages: CompareResolved against the batch's
// pre-chosen branch when one exists and the provider supports it,
// otherwise the provider's ordinary per-fork Compare (which itself scans
// branches to choose one, exactly as it did before batching existed). A
// CompareResolved error is not treated as fatal here -- it falls through
// to Compare once, so a batch-selection edge case (e.g. the branch was
// deleted between the batch call and dispatch) never turns a resolvable
// fork into a hard per-fork failure the way a plain error return would.
func compareFork(ctx context.Context, provider forge.Forge, fork forge.T1Data, sel *forge.BranchSelection, logger io.Writer) (forge.T2Data, error) {
	if sel != nil {
		if rp, ok := provider.(forge.ResolvedCompareProvider); ok {
			t2, err := rp.CompareResolved(ctx, fork, *sel)
			if err == nil {
				return t2, nil
			}
			logCompareResolvedFallback(logger, fork.ID, err)
		}
	}
	return provider.Compare(ctx, fork, fork.DefaultBranch)
}

// logCompareResolvedFallback reports a CompareResolved failure at
// debug-equivalent verbosity: silent with the default io.Discard logger
// (so it never affects tests or a caller that didn't wire one up), visible
// when the caller supplied a real logger. Mirrors logMomentumDebug in
// momentum.go.
func logCompareResolvedFallback(logger io.Writer, forkID string, err error) {
	if logger == nil || logger == io.Discard || err == nil {
		return
	}
	fmt.Fprintf(logger, "[triage] compare_resolved failed for %s: %v; falling back to full compare\n", forkID, err)
}

// batchEstimate summarises the pre-dispatch batch result for the up-front
// "[triage]" heads-up: how many of the forks the batch was asked about
// resolved to something ahead of upstream (and so still need one REST
// CompareResolved call each), versus resolved to nothing ahead (no REST
// call at all, already fully known) or were not resolved by the batch at
// all (fall back to a full per-fork Compare, same cost as before the
// batch existed).
//
// restEstimate adds those together: one request per divergent fork, plus
// the old per-fork tier estimate for every fork the batch could not
// resolve. A batch-resolved fork -- divergent or zero-ahead alike --
// skips or shrinks the T2 compare cost, but at tier >= 3 it still pays
// for the T3 Contributors call: the worker's `enrich` stays true for it
// (only the reserve floor or a fatal T2 error would flip it), so
// perRequestCost(tier)'s T2/T3 split (see budget.go) applies its
// contributors-only share -- perRequestCost(3)-perRequestCost(2) -- to
// every resolved fork, not just the unresolved ones already folded into
// perRequestCost(tier).
func batchEstimate(pending []forge.T1Data, divergence map[string]forge.ForkDivergence, tier int) (divergent, resolved, restEstimate int) {
	unresolved := 0
	for _, f := range pending {
		d, ok := divergence[f.ID]
		if !ok || !d.Resolved {
			unresolved++
			continue
		}
		resolved++
		if forge.SelectDivergentBranch(d).NeedsREST {
			divergent++
		}
	}
	contributorsShare := 0
	if tier >= 3 {
		contributorsShare = perRequestCost(3) - perRequestCost(2)
	}
	restEstimate = divergent + resolved*contributorsShare + unresolved*perRequestCost(tier)
	return divergent, resolved, restEstimate
}
