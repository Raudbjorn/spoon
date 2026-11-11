package forksops

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestRefreshCachedT2(t *testing.T) {
	// These branch-scoped count refreshes cannot select a replacement comparison.
	// Full-batch selection changes are covered by TestStreamCachedComparisonCoherence.
	base := forge.T2Data{Performed: true, ComparedBranch: "main", HeadSHA: "cached-head", AheadCount: 3, BehindCount: 2,
		Diffs: []forge.FileDiff{{Path: "keep.go", Patch: "keep patch"}}}
	for _, tc := range []struct {
		name             string
		t2               forge.T2Data
		d                forge.ForkDivergence
		wantBehind       int
		changed, invalid bool
	}{
		{"default", base, forge.ForkDivergence{Resolved: true, CountsOnly: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "cached-head", AheadBy: 3, BehindBy: 5}}, 5, true, false},
		{"changed default", base, forge.ForkDivergence{Resolved: true, CountsOnly: true, Default: forge.BranchDivergence{Name: "develop", TipSHA: "other-head", AheadBy: 3, BehindBy: 9}}, 2, false, false},
		{"old default now side", base, forge.ForkDivergence{Resolved: true, CountsOnly: true, Default: forge.BranchDivergence{Name: "develop"}, Sides: []forge.BranchDivergence{{Name: "main", TipSHA: "cached-head", AheadBy: 3, BehindBy: 5}}}, 5, true, false},
		{"upstream absorbed work", base, forge.ForkDivergence{Resolved: true, CountsOnly: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "cached-head", AheadBy: 0, BehindBy: 5}}, 0, false, true},
		{"head moved", base, forge.ForkDivergence{Resolved: true, CountsOnly: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "new-head", AheadBy: 3, BehindBy: 5}}, 0, false, true},
		{"missing head", base, forge.ForkDivergence{Resolved: true, CountsOnly: true, Default: forge.BranchDivergence{Name: "main", AheadBy: 3, BehindBy: 5}}, 2, false, false},
		{"unresolved", base, forge.ForkDivergence{}, 2, false, false},
		{"unknown default identity", forge.T2Data{AheadCount: 3, BehindCount: 2, HeadSHA: "cached-head"}, forge.ForkDivergence{Resolved: true, CountsOnly: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "cached-head", AheadBy: 3, BehindBy: 9}}, 2, false, false},
		{"legacy active branch", forge.T2Data{IsBranchWork: true, ActiveBranch: "feature", HeadSHA: "cached-head", AheadCount: 3, BehindCount: 2}, forge.ForkDivergence{Resolved: true, CountsOnly: true, Default: forge.BranchDivergence{Name: "feature", TipSHA: "cached-head", AheadBy: 3, BehindBy: 5}}, 5, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := RefreshCachedT2(&tc.t2, tc.d)
			if (got == nil) != tc.invalid || changed != tc.changed {
				t.Fatalf("refresh = (%+v, %v), want invalid=%v changed=%v", got, changed, tc.invalid, tc.changed)
			}
			if got != nil {
				if got == &tc.t2 || got.BehindCount != tc.wantBehind || got.AheadCount != tc.t2.AheadCount {
					t.Fatalf("refresh = %+v; original = %+v", got, tc.t2)
				}
				if len(got.Diffs) != len(tc.t2.Diffs) {
					t.Fatal("refresh changed cached diffs")
				}
				_, changedAgain := RefreshCachedT2(got, tc.d)
				if changedAgain {
					t.Fatal("unchanged count reported as refreshed")
				}
			}
			if tc.t2.BehindCount != 2 {
				t.Fatal("shared cache was mutated")
			}
		})
	}
	if got, changed := RefreshCachedT2(nil, forge.ForkDivergence{}); got != nil || changed {
		t.Fatal("nil cache was refreshed")
	}
}

func TestResolveFromBatch_RejectsCountsOnly(t *testing.T) {
	if _, ok := ResolveFromBatch(map[string]forge.ForkDivergence{"fork": {Resolved: true, CountsOnly: true}}, "fork"); ok {
		t.Fatal("cached branch counts cannot select a full fork comparison")
	}
}

func TestRefreshCachedT2_ReconcilesAheadCountZero(t *testing.T) {
	t2 := &forge.T2Data{
		Performed: true, ComparedBranch: "main", HeadSHA: "oldsha123",
		AheadCount: 3, BehindCount: 2,
		Diffs: []forge.FileDiff{{Path: "old.go", Patch: "old patch"}},
	}
	d := forge.ForkDivergence{
		Resolved: true, CountsOnly: true,
		Default: forge.BranchDivergence{Name: "main", TipSHA: "oldsha123", AheadBy: 0, BehindBy: 5},
	}

	got, changed := RefreshCachedT2(t2, d)
	// Clearing only AheadCount would leave stale commits/diffs attached to an empty comparison.
	if got != nil || changed {
		t.Fatalf("absorbed work must invalidate the comparison for a full refresh, got (%+v, %v)", got, changed)
	}
	if t2.AheadCount != 3 || t2.BehindCount != 2 || len(t2.Diffs) != 1 {
		t.Fatal("invalidation modified caller-owned evidence")
	}
}

func TestRefreshCachedT2_DefaultBranchTipMismatch_NotFoundInSides(t *testing.T) {
	t2 := &forge.T2Data{
		Performed: true, ComparedBranch: "old-default",
		AheadCount: 3, BehindCount: 2, HeadSHA: "oldsha123",
	}
	d := forge.ForkDivergence{
		Resolved: true, CountsOnly: true,
		Default: forge.BranchDivergence{Name: "main", TipSHA: "newsha456", BehindBy: 10},
		Sides:   []forge.BranchDivergence{{Name: "other", TipSHA: "othersha789", BehindBy: 4}},
	}

	got, changed := RefreshCachedT2(t2, d)
	// An unrelated default cannot refresh the recorded branch's comparison.
	if changed || got == nil || got.BehindCount != 2 || got.AheadCount != 3 {
		t.Fatalf("unmatched comparison changed: (%+v, %v)", got, changed)
	}
}

func TestRefreshCachedT2_DefaultBranchTipMismatch_FoundInSides(t *testing.T) {
	t2 := &forge.T2Data{
		Performed: true, ComparedBranch: "old-default",
		AheadCount: 3, BehindCount: 2, HeadSHA: "oldsha123",
	}
	d := forge.ForkDivergence{
		Resolved: true, CountsOnly: true,
		Default: forge.BranchDivergence{Name: "main", TipSHA: "newsha456", BehindBy: 10},
		Sides:   []forge.BranchDivergence{{Name: "old-default", TipSHA: "oldsha123", AheadBy: 3, BehindBy: 7}},
	}

	got, changed := RefreshCachedT2(t2, d)
	// Both recorded branch and head must match; update the result, not the shared cache.
	if !changed || got == nil || got.BehindCount != 7 || got.AheadCount != 3 {
		t.Fatalf("old default in sides was not refreshed: (%+v, %v)", got, changed)
	}
	if t2.BehindCount != 2 {
		t.Fatal("shared cached count was mutated")
	}
}

func TestBatchEstimate_CachedForksExcludedFromRestEstimate(t *testing.T) {
	pending := []forge.T1Data{
		{ID: "o/cached1"},
		{ID: "o/cached2"},
		{ID: "o/cached_missing"},
		{ID: "o/cached_unresolved"},
		{ID: "o/uncached_zero"},
		{ID: "o/uncached_div"},
		{ID: "o/uncached_unresolved"},
	}
	divergence := map[string]forge.ForkDivergence{
		"o/cached_unresolved": {Resolved: false},
		"o/cached1":           {Resolved: true, Default: forge.BranchDivergence{AheadBy: 0}},
		"o/cached2":           {Resolved: true, Default: forge.BranchDivergence{AheadBy: 3}},
		"o/uncached_zero":     {Resolved: true, Default: forge.BranchDivergence{AheadBy: 0}},
		"o/uncached_div":      {Resolved: true, Default: forge.BranchDivergence{AheadBy: 2}},
	}
	cached := func(f forge.T1Data) bool {
		return strings.HasPrefix(f.ID, "o/cached")
	}

	divergent, resolved, tier2Estimate := batchEstimate(pending, divergence, 2, cached)
	if divergent != 1 {
		t.Errorf("divergent = %d, want 1 (only o/uncached_div)", divergent)
	}
	if resolved != 4 {
		t.Errorf("resolved = %d, want 4", resolved)
	}
	// tier 2: 1 divergent * 1 + 1 unresolved * perRequestCost(2)=3 -> 4
	if tier2Estimate != 4 {
		t.Errorf("tier2Estimate = %d, want 4", tier2Estimate)
	}

	_, _, tier3Estimate := batchEstimate(pending, divergence, 3, cached)
	// Cached missing/unresolved forks still need contributors: 1 + 6*2 + 1*5 = 18.
	if tier3Estimate != 18 {
		t.Errorf("tier3Estimate = %d, want 18", tier3Estimate)
	}
}

func TestStream_cachedT2_refreshesPrivateCopy(t *testing.T) {
	now := time.Now()
	cachedT2Def := forge.T2Data{Performed: true, ComparedBranch: "main", HeadSHA: "def-head", AheadCount: 4, BehindCount: 1}
	cachedT2Side := forge.T2Data{
		Performed:    true,
		AheadCount:   2,
		BehindCount:  3,
		IsBranchWork: true,
		ActiveBranch: "feature",
		HeadSHA:      "side-head",
	}

	var resolvedCalls []string
	ff := &batchFakeForge{
		fakeForge: &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
			forks: []forge.T1Data{
				{ID: "o/cached_def", Owner: "o", Name: "cached_def", DefaultBranch: "main", PushedAt: now},
				{ID: "o/cached_side", Owner: "o", Name: "cached_side", DefaultBranch: "main", PushedAt: now},
			},
			t2: map[string]forge.T2Data{},
		},
		batch: map[string]forge.ForkDivergence{
			"o/cached_def": {
				Resolved: true,
				Default:  forge.BranchDivergence{Name: "main", TipSHA: "def-head", AheadBy: 4, BehindBy: 9},
			},
			"o/cached_side": {
				Resolved: true,
				Default:  forge.BranchDivergence{Name: "main", AheadBy: 0, BehindBy: 15},
				Sides:    []forge.BranchDivergence{{Name: "feature", TipSHA: "side-head", AheadBy: 2, BehindBy: 7, TipCommittedAt: now}},
			},
		},
		batchStats:    forge.BatchStats{Queries: 1, Cost: 2},
		resolvedCalls: &resolvedCalls,
	}

	var cacheCalls atomic.Int32
	cachedMap := map[string]*forge.T2Data{
		"o/cached_def":  &cachedT2Def,
		"o/cached_side": &cachedT2Side,
	}

	summary := &CompareSummary{}
	opts := Options{
		Tier:          2,
		CompareReport: summary,
		CachedT2: func(f forge.T1Data) *forge.T2Data {
			cacheCalls.Add(1)
			return cachedMap[f.ID]
		},
	}

	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	results := make(map[string]Result)
	for r := range ch {
		results[r.Fork.ID] = r
	}

	if cacheCalls.Load() != 2 {
		t.Errorf("cache called %d times, want once per fork", cacheCalls.Load())
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	resDef := results["o/cached_def"]
	if resDef.T2 == nil {
		t.Fatal("expected T2 on o/cached_def, got nil")
	}
	if !resDef.T2BehindRefreshed {
		t.Error("missing scalar refresh persistence flag")
	}
	if !resDef.T2FromCache {
		t.Error("expected T2FromCache=true for o/cached_def")
	}
	if resDef.T2.BehindCount != 9 {
		t.Errorf("o/cached_def BehindCount = %d, want 9 (refreshed)", resDef.T2.BehindCount)
	}
	if resDef.T2.AheadCount != 4 {
		t.Errorf("o/cached_def AheadCount = %d, want 4 (preserved)", resDef.T2.AheadCount)
	}
	if cachedT2Def.BehindCount != 1 {
		t.Errorf("cachedT2Def original BehindCount = %d, want 1", cachedT2Def.BehindCount)
	}

	resSide := results["o/cached_side"]
	if resSide.T2 == nil {
		t.Fatal("expected T2 on o/cached_side, got nil")
	}
	if !resSide.T2FromCache {
		t.Error("expected T2FromCache=true for o/cached_side")
	}
	if resSide.T2.BehindCount != 7 {
		t.Errorf("o/cached_side BehindCount = %d, want 7 (refreshed for side branch)", resSide.T2.BehindCount)
	}
	if resSide.T2.AheadCount != 2 {
		t.Errorf("o/cached_side AheadCount = %d, want 2 (preserved)", resSide.T2.AheadCount)
	}
	if cachedT2Side.BehindCount != 3 {
		t.Errorf("cachedT2Side original BehindCount = %d, want 3", cachedT2Side.BehindCount)
	}

	if summary.Cached != 2 {
		t.Errorf("summary.Cached = %d, want 2", summary.Cached)
	}
	if summary.REST != 0 {
		t.Errorf("summary.REST = %d, want 0 (no REST compares for cached forks)", summary.REST)
	}
	if len(resolvedCalls) != 0 {
		t.Errorf("CompareResolved called %d times (%v), want 0 (no redundant T2 re-fetch)", len(resolvedCalls), resolvedCalls)
	}
	if ff.batchCalls != 1 {
		t.Errorf("ff.batchCalls = %d, want 1", ff.batchCalls)
	}
}

func TestStream_cachedT2_noBatchCompare_leavesBehindCountUnchanged(t *testing.T) {
	now := time.Now()
	cachedT2 := forge.T2Data{Performed: true, AheadCount: 5, BehindCount: 2}

	ff := &batchFakeForge{
		fakeForge: &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
			forks: []forge.T1Data{
				{ID: "o/cached", Owner: "o", Name: "cached", DefaultBranch: "main", PushedAt: now},
			},
			t2: map[string]forge.T2Data{},
		},
		batch: map[string]forge.ForkDivergence{
			"o/cached": {Resolved: true, Default: forge.BranchDivergence{Name: "main", AheadBy: 5, BehindBy: 10}},
		},
	}

	summary := &CompareSummary{}
	opts := Options{
		Tier:           2,
		NoBatchCompare: true,
		CompareReport:  summary,
		CachedT2: func(f forge.T1Data) *forge.T2Data {
			if f.ID == "o/cached" {
				return &cachedT2
			}
			return nil
		},
	}

	ch, err := Stream(context.Background(), ff, "o", "r", opts)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var res Result
	for r := range ch {
		res = r
	}

	if res.T2 == nil || res.T2.BehindCount != 2 {
		t.Errorf("BehindCount = %v, want 2 (unchanged when NoBatchCompare is set)", res.T2)
	}
	if ff.batchCalls != 0 {
		t.Errorf("BatchCompare called %d times, want 0", ff.batchCalls)
	}
	if summary.Cached != 1 {
		t.Errorf("summary.Cached = %d, want 1", summary.Cached)
	}
}

func TestStream_CachedAheadChangeRefetchesOrInvalidates(t *testing.T) {
	for _, lowAfterBatch := range []bool{false, true} {
		t.Run(fmt.Sprintf("reserve=%v", lowAfterBatch), func(t *testing.T) {
			now := time.Now()
			cached := forge.T2Data{Performed: true, ComparedBranch: "main", HeadSHA: "old-head", AheadCount: 4, BehindCount: 1,
				Diffs: []forge.FileDiff{{Path: "old.go", Patch: "old patch"}}}
			var compares []string
			ff := &batchFakeForge{
				fakeForge: &fakeForge{parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
					forks: []forge.T1Data{{ID: "o/cached", Owner: "o", Name: "cached", DefaultBranch: "main", PushedAt: now}},
					t2:    map[string]forge.T2Data{"o/cached": {Performed: true, ComparedBranch: "feature", IsBranchWork: true, ActiveBranch: "feature", AheadCount: 1, BehindCount: 8, Diffs: []forge.FileDiff{{Path: "feature.go", Patch: "feature patch"}}}}, compareOrder: &compares},
				batch: map[string]forge.ForkDivergence{"o/cached": {Resolved: true, CountsOnly: true, Default: forge.BranchDivergence{Name: "main", TipSHA: "old-head", AheadBy: 0, BehindBy: 9}}},
			}
			if lowAfterBatch {
				low := .01
				ff.headroomAfterBatch = &low
			}
			ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2, CachedT2: func(forge.T1Data) *forge.T2Data { return &cached }})
			if err != nil {
				t.Fatal(err)
			}
			var result Result
			for r := range ch {
				result = r
			}
			if result.T2FromCache || result.T2Invalidated == nil || result.T2Invalidated.HeadSHA != "old-head" {
				t.Fatalf("stale comparison not invalidated: %+v", result)
			}
			if lowAfterBatch {
				if result.T2 != nil || result.BudgetSkip == nil || len(compares) != 0 {
					t.Fatalf("reserve ignored: %+v compares=%v", result, compares)
				}
			} else if result.T2 == nil || result.T2.ComparedBranch != "feature" || !result.T2.IsBranchWork ||
				result.T2.AheadCount != 1 || result.T2.BehindCount != 8 || len(result.T2.Diffs) != 1 || result.T2.Diffs[0].Path != "feature.go" || len(compares) != 1 {
				t.Fatalf("full branch scan not repeated after invalidation: %+v compares=%v", result, compares)
			}
			if cached.AheadCount != 4 || cached.BehindCount != 1 || len(cached.Diffs) != 1 {
				t.Fatal("original cache modified")
			}
		})
	}
}

func TestStream_CachedContributorsHonorReserve(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled=%v", disabled), func(t *testing.T) {
			low := .01
			now := time.Now()
			ff := &batchFakeForge{fakeForge: &fakeForge{
				parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
				forks:  []forge.T1Data{{ID: "o/cached", DefaultBranch: "main", PushedAt: now}}, headroom: &low,
			}}
			cached := forge.T2Data{Performed: true, AheadCount: 3}
			ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 3, ReserveDisabled: disabled, CachedT2: func(forge.T1Data) *forge.T2Data { return &cached }})
			if err != nil {
				t.Fatal(err)
			}
			var result Result
			for r := range ch {
				result = r
			}
			if result.T2 == nil || !result.T2FromCache {
				t.Fatal("reserve suppressed free cached evidence")
			}
			if disabled {
				if result.T3 == nil || result.T3Skip != nil || ff.batchCalls != 1 {
					t.Fatalf("disabled reserve not honored: %+v batch=%d", result, ff.batchCalls)
				}
			} else if result.T3 != nil || result.T3Skip == nil || ff.batchCalls != 0 {
				t.Fatalf("reserve failed: %+v batch=%d", result, ff.batchCalls)
			}
		})
	}
}
