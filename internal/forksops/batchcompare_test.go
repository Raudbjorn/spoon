package forksops

import (
	"context"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestRefreshCachedBehind_DefaultBranch(t *testing.T) {
	t2 := &forge.T2Data{Performed: true, AheadCount: 3, BehindCount: 2}
	d := forge.ForkDivergence{
		Resolved: true,
		Default:  forge.BranchDivergence{Name: "main", BehindBy: 5},
	}

	changed := RefreshCachedBehind(t2, d)
	if !changed {
		t.Fatal("expected RefreshCachedBehind to report changed=true")
	}
	if t2.BehindCount != 5 {
		t.Fatalf("BehindCount = %d, want 5", t2.BehindCount)
	}

	// Calling again with the same divergence should report changed=false
	changedAgain := RefreshCachedBehind(t2, d)
	if changedAgain {
		t.Fatal("expected RefreshCachedBehind to report changed=false when already up to date")
	}
	if t2.BehindCount != 5 {
		t.Fatalf("BehindCount = %d, want 5", t2.BehindCount)
	}
}

func TestRefreshCachedBehind_SideBranch(t *testing.T) {
	t2 := &forge.T2Data{
		Performed:    true,
		AheadCount:   2,
		BehindCount:  1,
		IsBranchWork: true,
		ActiveBranch: "feature",
	}
	d := forge.ForkDivergence{
		Resolved: true,
		Default:  forge.BranchDivergence{Name: "main", BehindBy: 10},
		Sides: []forge.BranchDivergence{
			{Name: "feature", BehindBy: 4},
		},
	}

	changed := RefreshCachedBehind(t2, d)
	if !changed {
		t.Fatal("expected RefreshCachedBehind to report changed=true")
	}
	if t2.BehindCount != 4 {
		t.Fatalf("BehindCount = %d, want 4 (from side branch)", t2.BehindCount)
	}
}

func TestRefreshCachedBehind_ActiveBranchMatchesDefault(t *testing.T) {
	t2 := &forge.T2Data{
		Performed:    true,
		AheadCount:   1,
		BehindCount:  0,
		IsBranchWork: true,
		ActiveBranch: "main",
	}
	d := forge.ForkDivergence{
		Resolved: true,
		Default:  forge.BranchDivergence{Name: "main", BehindBy: 6},
	}

	changed := RefreshCachedBehind(t2, d)
	if !changed {
		t.Fatal("expected RefreshCachedBehind to report changed=true")
	}
	if t2.BehindCount != 6 {
		t.Fatalf("BehindCount = %d, want 6", t2.BehindCount)
	}
}

func TestRefreshCachedBehind_SideBranchNotFound(t *testing.T) {
	t2 := &forge.T2Data{
		Performed:    true,
		AheadCount:   2,
		BehindCount:  3,
		IsBranchWork: true,
		ActiveBranch: "deleted-branch",
	}
	d := forge.ForkDivergence{
		Resolved: true,
		Default:  forge.BranchDivergence{Name: "main", BehindBy: 12},
		Sides: []forge.BranchDivergence{
			{Name: "other", BehindBy: 4},
		},
	}

	changed := RefreshCachedBehind(t2, d)
	if changed {
		t.Fatal("expected changed=false when active branch is not in sides")
	}
	if t2.BehindCount != 3 {
		t.Fatalf("BehindCount = %d, want 3 (original value preserved)", t2.BehindCount)
	}
}

func TestRefreshCachedBehind_Unresolved(t *testing.T) {
	t2 := &forge.T2Data{Performed: true, AheadCount: 2, BehindCount: 1}
	d := forge.ForkDivergence{Resolved: false}

	changed := RefreshCachedBehind(t2, d)
	if changed {
		t.Fatal("expected changed=false when divergence is unresolved")
	}
	if t2.BehindCount != 1 {
		t.Fatalf("BehindCount = %d, want 1", t2.BehindCount)
	}
}

func TestRefreshCachedBehind_NilT2(t *testing.T) {
	d := forge.ForkDivergence{
		Resolved: true,
		Default:  forge.BranchDivergence{Name: "main", BehindBy: 5},
	}
	if RefreshCachedBehind(nil, d) {
		t.Fatal("expected false for nil t2")
	}
}

func TestBatchEstimate_CachedForksExcludedFromRestEstimate(t *testing.T) {
	pending := []forge.T1Data{
		{ID: "o/cached1"},
		{ID: "o/cached2"},
		{ID: "o/uncached_zero"},
		{ID: "o/uncached_div"},
		{ID: "o/uncached_unresolved"},
	}
	divergence := map[string]forge.ForkDivergence{
		"o/cached1":       {Resolved: true, Default: forge.BranchDivergence{AheadBy: 0}},
		"o/cached2":       {Resolved: true, Default: forge.BranchDivergence{AheadBy: 3}},
		"o/uncached_zero": {Resolved: true, Default: forge.BranchDivergence{AheadBy: 0}},
		"o/uncached_div":  {Resolved: true, Default: forge.BranchDivergence{AheadBy: 2}},
	}
	cached := func(f forge.T1Data) bool {
		return f.ID == "o/cached1" || f.ID == "o/cached2"
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
	// tier 3: 1 divergent * 1 + 4 resolved * 2 contributors + 1 unresolved * 5 = 1 + 8 + 5 = 14
	if tier3Estimate != 14 {
		t.Errorf("tier3Estimate = %d, want 14", tier3Estimate)
	}
}

func TestStream_cachedT2_refreshesBehindCountInPlace(t *testing.T) {
	now := time.Now()
	cachedT2Def := forge.T2Data{Performed: true, AheadCount: 4, BehindCount: 1}
	cachedT2Side := forge.T2Data{
		Performed:    true,
		AheadCount:   2,
		BehindCount:  3,
		IsBranchWork: true,
		ActiveBranch: "feature",
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
				Default:  forge.BranchDivergence{Name: "main", AheadBy: 4, BehindBy: 9},
			},
			"o/cached_side": {
				Resolved: true,
				Default:  forge.BranchDivergence{Name: "main", AheadBy: 0, BehindBy: 15},
				Sides:    []forge.BranchDivergence{{Name: "feature", AheadBy: 2, BehindBy: 7, TipCommittedAt: now}},
			},
		},
		batchStats:    forge.BatchStats{Queries: 1, Cost: 2},
		resolvedCalls: &resolvedCalls,
	}

	cachedMap := map[string]*forge.T2Data{
		"o/cached_def":  &cachedT2Def,
		"o/cached_side": &cachedT2Side,
	}

	summary := &CompareSummary{}
	opts := Options{
		Tier:          2,
		CompareReport: summary,
		CachedT2: func(f forge.T1Data) *forge.T2Data {
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

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	resDef := results["o/cached_def"]
	if resDef.T2 == nil {
		t.Fatal("expected T2 on o/cached_def, got nil")
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
	if cachedT2Def.BehindCount != 9 {
		t.Errorf("cachedT2Def in-place BehindCount = %d, want 9", cachedT2Def.BehindCount)
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
	if cachedT2Side.BehindCount != 7 {
		t.Errorf("cachedT2Side in-place BehindCount = %d, want 7", cachedT2Side.BehindCount)
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
