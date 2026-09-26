package forge

import (
	"context"
	"errors"
	"sort"
	"time"
)

// BranchDivergence is one branch's ahead/behind relationship to the upstream
// baseline, as resolved by a single GraphQL batch call. It carries the same
// verdict CompareResult carries per-branch on the REST path, plus the
// upstreamed-PR signal, so SelectDivergentBranch can apply the branch-choice
// policy without a second round trip.
type BranchDivergence struct {
	Name           string
	TipSHA         string
	TipCommittedAt time.Time
	AheadBy        int
	BehindBy       int
	// UpstreamedPR is the merged upstream PR number this branch's tip heads,
	// or 0 when the tip is not known to head a merged PR. UpstreamedPR > 0
	// is the definition of "upstreamed" throughout this package.
	UpstreamedPR int
}

// ForkDivergence is one fork's batch-resolved divergence: the default branch
// plus every side branch the batch considered, in the order the provider
// listed them (newest tip first). SelectDivergentBranch consumes this to
// pick the branch to attribute the fork's work to.
type ForkDivergence struct {
	Default BranchDivergence
	Sides   []BranchDivergence
	// Resolved is false when the batch call could not resolve this
	// particular fork (e.g. deleted, made private, or otherwise
	// unreachable at query time). Default and Sides are then zero and
	// callers must fall back to the per-fork REST path instead of passing
	// this value to SelectDivergentBranch.
	Resolved bool
}

// BranchSelection is the outcome of applying the branch-choice policy to a
// ForkDivergence: which branch to attribute the fork's work to, and the
// divergence to report for it.
type BranchSelection struct {
	Branch       string
	Ahead        int
	Behind       int
	TipSHA       string
	Upstreamed   bool
	UpstreamedPR int
	// IsSide is true when Branch is not the fork's default branch.
	IsSide bool
	// NeedsREST is true when Ahead > 0 -- there is genuine or upstreamed
	// work worth enriching with a REST/GraphQL Compare (commits, file
	// diffs). False means the fork has nothing ahead of upstream on the
	// selected branch and no further compare call is needed.
	NeedsREST bool
}

// BatchStats reports the API cost of a BatchCompare call, for the
// acquisition report.
type BatchStats struct {
	Queries int
	Cost    int
}

// ErrBatchCompareUnavailable is returned by a BatchCompareProvider
// implementation that determines, at call time, it cannot serve a batch
// compare at all (e.g. the authenticated token lacks GraphQL access).
// Callers fall back to the per-fork REST path silently on this error rather
// than surfacing it as a hard failure.
var ErrBatchCompareUnavailable = errors.New("forge: batch compare unavailable")

// BatchCompareProvider is an optional provider capability that resolves
// divergence for many forks against their shared upstream baseline in one
// GraphQL batch, replacing a per-fork REST branch scan. Implementations
// should return ErrBatchCompareUnavailable (rather than a wrapped error)
// when the batch path cannot be used at all, so callers can fall back
// silently to the REST path.
//
// A fork ID absent from the returned map was not resolved by the batch and
// must be handled by the REST path individually, same as a present entry
// with Resolved == false.
type BatchCompareProvider interface {
	BatchCompare(ctx context.Context, forks []T1Data) (map[string]ForkDivergence, BatchStats, error)
}

// MissingReposProvider is an optional provider capability that reports which
// forks' repositories no longer exist (deleted, disabled or hidden), keyed by
// T1Data.ID. A fork absent from the map is not known to be missing.
type MissingReposProvider interface {
	MissingRepos(ctx context.Context, forks []T1Data) (map[string]bool, error)
}

// ResolvedCompareProvider is an optional provider capability that fetches
// the full T2Data (commits, file diffs) for a fork given a BranchSelection
// already chosen by SelectDivergentBranch, so the branch-choice decision
// made from the batch result does not have to be re-derived from a second
// REST round trip.
type ResolvedCompareProvider interface {
	CompareResolved(ctx context.Context, fork T1Data, sel BranchSelection) (T2Data, error)
}

// SelectDivergentBranch applies the branch-choice policy to a fork's
// batch-resolved divergence and returns the branch to attribute the fork's
// work to.
//
// Precedence: the default branch wins if it carries genuine (not yet
// upstreamed) work; otherwise the most-recently-committed side branch with
// genuine work wins; otherwise the default branch wins if its own work is
// already upstreamed; otherwise the most-recently-committed upstreamed side
// branch wins; otherwise (nothing ahead anywhere) the default branch is
// returned with Ahead=0 and NeedsREST=false. "Upstreamed" means
// UpstreamedPR > 0. Ties on TipCommittedAt keep the input order of Sides
// (the caller is expected to list sides newest-first, but this function
// sorts defensively rather than trusting that).
//
// This is a pure port of the REST-path policy in
// internal/github/branches.go (FetchCompareWithBranchScan + ScanBranches).
func SelectDivergentBranch(d ForkDivergence) BranchSelection {
	if d.Default.AheadBy > 0 && d.Default.UpstreamedPR == 0 {
		return selectionFor(d.Default, false)
	}

	sides := newestFirst(d.Sides)

	for _, s := range sides {
		if s.AheadBy > 0 && s.UpstreamedPR == 0 {
			return selectionFor(s, true)
		}
	}

	if d.Default.AheadBy > 0 && d.Default.UpstreamedPR > 0 {
		return selectionFor(d.Default, false)
	}

	for _, s := range sides {
		if s.AheadBy > 0 && s.UpstreamedPR > 0 {
			return selectionFor(s, true)
		}
	}

	// Nothing ahead anywhere: report the default branch as-is, with no
	// further REST enrichment needed.
	return BranchSelection{
		Branch:    d.Default.Name,
		Ahead:     0,
		Behind:    d.Default.BehindBy,
		TipSHA:    d.Default.TipSHA,
		NeedsREST: false,
	}
}

// selectionFor builds the BranchSelection for a chosen BranchDivergence.
func selectionFor(b BranchDivergence, isSide bool) BranchSelection {
	return BranchSelection{
		Branch:       b.Name,
		Ahead:        b.AheadBy,
		Behind:       b.BehindBy,
		TipSHA:       b.TipSHA,
		Upstreamed:   b.UpstreamedPR > 0,
		UpstreamedPR: b.UpstreamedPR,
		IsSide:       isSide,
		NeedsREST:    b.AheadBy > 0,
	}
}

// newestFirst returns a stably-sorted copy of sides ordered by
// TipCommittedAt descending. Ties keep their relative input order. The
// input slice is never mutated, keeping SelectDivergentBranch pure.
func newestFirst(sides []BranchDivergence) []BranchDivergence {
	sorted := make([]BranchDivergence, len(sides))
	copy(sorted, sides)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].TipCommittedAt.After(sorted[j].TipCommittedAt)
	})
	return sorted
}
