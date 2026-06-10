package forksops

import (
	"math"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestSelectByOptimalStopping_BudgetBounds(t *testing.T) {
	p := []float64{1, 2, 3, 4, 5}
	// Budget >= n → all selected.
	got := SelectByOptimalStopping(p, 10)
	for i, b := range got {
		if !b {
			t.Errorf("budget>=n: index %d not selected", i)
		}
	}
	// Budget 0 / negative → none.
	for _, b := range SelectByOptimalStopping(p, 0) {
		if b {
			t.Error("budget 0 should select nothing")
		}
	}
	// Empty input → empty mask.
	if len(SelectByOptimalStopping(nil, 5)) != 0 {
		t.Error("nil input should give empty mask")
	}
}

func TestSelectByOptimalStopping_HiresRecordBeatersAfterObservation(t *testing.T) {
	// Ascending promise (worst case: best candidates arrive last). The online
	// rule learns the threshold from the first n/e and then hires the earliest
	// candidates that beat it — spending the full budget on above-threshold
	// forks, NOT on the observation window.
	n := 100
	p := make([]float64, n)
	for i := range p {
		p[i] = float64(i)
	}
	budget := 10
	got := SelectByOptimalStopping(p, budget)

	observe := int(float64(n) / math.E)
	threshold := float64(observe - 1) // max of p[0..observe-1] for this input
	count := 0
	for i, b := range got {
		if !b {
			continue
		}
		count++
		if i < observe {
			t.Errorf("hired an observation-window candidate at idx %d", i)
		}
		if p[i] <= threshold {
			t.Errorf("hired a below-threshold candidate at idx %d (p=%v, thr=%v)", i, p[i], threshold)
		}
	}
	if count != budget {
		t.Fatalf("hired %d, want full budget %d", count, budget)
	}
}

func TestSelectByOptimalStopping_FillsWhenThresholdTooStrict(t *testing.T) {
	// Best candidate is in the observation window → threshold is the global max,
	// so the record phase hires nobody; the fill phase must still spend budget.
	p := make([]float64, 50)
	p[0] = 1000 // global max sits in the observation window
	for i := 1; i < len(p); i++ {
		p[i] = float64(i)
	}
	got := SelectByOptimalStopping(p, 5)
	count := 0
	for _, b := range got {
		if b {
			count++
		}
	}
	if count != 5 {
		t.Errorf("fill phase should spend the full budget; got %d want 5", count)
	}
}

func TestComparePromise_BranchAfterUpstreamWins(t *testing.T) {
	upstream := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	diverged := forge.T1Data{
		ID:       "a/repo",
		PushedAt: upstream.Add(48 * time.Hour),
		Branches: []forge.BranchRef{{Name: "feat", CommittedDate: upstream.Add(72 * time.Hour)}},
	}
	// Popular but stale: lots of stars, but no commits after upstream.
	popularStale := forge.T1Data{
		ID:       "b/repo",
		Stars:    9000,
		PushedAt: upstream, // never pushed past upstream
		Branches: []forge.BranchRef{{Name: "main", CommittedDate: upstream.Add(-time.Hour)}},
	}

	pd := ComparePromise(diverged, upstream)
	ps := ComparePromise(popularStale, upstream)
	if pd <= ps {
		t.Errorf("a fork that diverged after upstream (%.2f) should outrank a popular-but-stale one (%.2f)", pd, ps)
	}
}

func TestComparePromise_PRsAndSubforksCount(t *testing.T) {
	upstream := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	base := forge.T1Data{ID: "x", PushedAt: upstream}
	withPRs := base
	withPRs.OpenPRCount = 3
	if ComparePromise(withPRs, upstream) <= ComparePromise(base, upstream) {
		t.Error("open PRs should raise promise")
	}
	withSubforks := base
	withSubforks.SubForkCount = 4
	if ComparePromise(withSubforks, upstream) <= ComparePromise(base, upstream) {
		t.Error("sub-forks should raise promise")
	}
}
