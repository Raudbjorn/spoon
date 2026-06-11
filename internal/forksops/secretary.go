package forksops

import (
	"math"
	"sort"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// ComparePromise scores how likely a fork is to have *real divergence* from its
// upstream, using only the cheap signals already present at T1 (no compare
// call). It is the "quality" a candidate is judged on in the optimal-stopping
// gate below — deliberately NOT the surface heat score (stars/recency), which
// says nothing about whether a fork actually contains novel code.
//
// The strongest cheap signal is a branch whose newest commit postdates the
// upstream's last push: that almost always means the fork carries its own
// commits. Open PRs and sub-forks corroborate; stars are a faint tiebreak only.
func ComparePromise(fork forge.T1Data, parentPushedAt time.Time) float64 {
	var p float64

	switch tip := newestBranchTime(fork); {
	case !tip.IsZero() && tip.After(parentPushedAt):
		// Fork has a branch with commits newer than upstream's last push: the
		// clearest cheap divergence signal. Weight by how far ahead it is, so
		// recently-diverged forks rank above long-stale ones.
		days := tip.Sub(parentPushedAt).Hours() / 24
		p += 10 + math.Log1p(days)
	case fork.PushedAt.After(parentPushedAt):
		// Weaker fallback (e.g. GitLab T1 without branch data): a push after
		// upstream could be a sync, so a small bump only.
		p += 2
	}

	p += float64(fork.OpenPRCount) * 5  // PRs imply real, upstream-aimed changes
	p += float64(fork.SubForkCount) * 3 // others forked it → notable
	// Stars are only a faint tiebreak (×0.1): the whole point is that a
	// no-stars fork with real divergence must outrank a popular-but-stale one,
	// so popularity must never dominate the divergence signals above.
	p += math.Log1p(float64(fork.Stars)) * 0.1
	return p
}

// dispatchSurfaceTiebreak scales the surface heat score into a genuinely faint
// additive tiebreak (heat maxes ~100 → ≤0.1), so it only orders forks whose
// divergence promise is essentially equal and never overrides it.
const dispatchSurfaceTiebreak = 1e-3

// DispatchPriority is the expected-value-per-request used to order which forks
// get their expensive T2/T3 calls first, so the compare budget (and any rate
// window) is spent best-first. The cheap divergence promise dominates; surface
// heat is only a faint tiebreak among equally-promising forks. Higher = sooner.
func DispatchPriority(fork forge.T1Data, parentPushedAt time.Time, surfaceScore float64) float64 {
	return ComparePromise(fork, parentPushedAt) + surfaceScore*dispatchSurfaceTiebreak
}

// newestBranchTime returns the most recent branch commit date known at T1, or
// the zero time when no branch data is available.
func newestBranchTime(fork forge.T1Data) time.Time {
	var newest time.Time
	for _, b := range fork.Branches {
		if b.CommittedDate.After(newest) {
			newest = b.CommittedDate
		}
	}
	return newest
}

// SelectByOptimalStopping picks up to `budget` indices to deep-scan from a
// stream of candidate promise scores in arrival (enumeration) order, applying
// the secretary problem's 1/e rule: observe (and skip) the first n/e
// candidates to learn a threshold, then "hire" every later record-beater until
// the budget runs out. If the threshold proves too strict and budget is left
// unspent, the remainder is filled with the highest-promise candidates overall
// — so a tight rate budget is never under-used.
//
// Returns a mask the same length as promises (true = compare this fork).
func SelectByOptimalStopping(promises []float64, budget int) []bool {
	n := len(promises)
	sel := make([]bool, n)
	if n == 0 || budget <= 0 {
		return sel
	}
	if budget >= n {
		for i := range sel {
			sel[i] = true
		}
		return sel
	}

	// Observation phase: the first n/e candidates set the bar but are not hired.
	observe := int(float64(n) / math.E)
	if observe < 1 {
		observe = 1
	}
	threshold := math.Inf(-1)
	for i := 0; i < observe; i++ {
		if promises[i] > threshold {
			threshold = promises[i]
		}
	}

	// Selection phase: hire record-beaters in arrival order until budget is gone.
	count := 0
	for i := observe; i < n && count < budget; i++ {
		if promises[i] > threshold {
			sel[i] = true
			count++
		}
	}

	// Fill any leftover budget with the best of the not-yet-selected (including
	// strong candidates that fell inside the observation window).
	if count < budget {
		rest := make([]int, 0, n-count)
		for i := 0; i < n; i++ {
			if !sel[i] {
				rest = append(rest, i)
			}
		}
		sort.SliceStable(rest, func(a, b int) bool { return promises[rest[a]] > promises[rest[b]] })
		for _, idx := range rest {
			if count >= budget {
				break
			}
			sel[idx] = true
			count++
		}
	}
	return sel
}
