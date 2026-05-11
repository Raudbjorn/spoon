package heat

import (
	"testing"
	"time"
)

func TestComputeTier1(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		params  Tier1Params
		minHeat float64
		maxHeat float64
	}{
		{
			name: "popular active fork",
			params: Tier1Params{
				Stars: 100, Forks: 20, OpenIssues: 10,
				ForkSize: 5000, ParentSize: 3000,
				ForkDesc: "Custom version", ParentDesc: "Original",
				PushedAt:       now.Add(-48 * time.Hour),
				ParentPushedAt: now.Add(-24 * time.Hour),
				Now:            now,
			},
			minHeat: 50, maxHeat: 100,
		},
		{
			name: "zero-star fresh fork",
			params: Tier1Params{
				Stars: 0, Forks: 0, OpenIssues: 0,
				ForkSize: 1000, ParentSize: 1000,
				ForkDesc: "Original", ParentDesc: "Original",
				PushedAt:       now.Add(-24 * time.Hour),
				ParentPushedAt: now.Add(-24 * time.Hour),
				Now:            now,
			},
			minHeat: 0, maxHeat: 40,
		},
		{
			name: "old stale fork",
			params: Tier1Params{
				Stars: 2, Forks: 0, OpenIssues: 0,
				ForkSize: 1000, ParentSize: 1000,
				ForkDesc: "Original", ParentDesc: "Original",
				PushedAt:       now.Add(-3 * 365 * 24 * time.Hour),
				ParentPushedAt: now.Add(-24 * time.Hour),
				Now:            now,
			},
			minHeat: 0, maxHeat: 20,
		},
		{
			name: "archived fork",
			params: Tier1Params{
				Stars: 50, Forks: 5, OpenIssues: 0,
				ForkSize: 1000, ParentSize: 1000,
				ForkDesc: "Archived", ParentDesc: "Original",
				Archived:       true,
				PushedAt:       now.Add(-30 * 24 * time.Hour),
				ParentPushedAt: now.Add(-24 * time.Hour),
				Now:            now,
			},
			minHeat: 20, maxHeat: 75,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ComputeTier1(tt.params)
			if result.Score < tt.minHeat || result.Score > tt.maxHeat {
				t.Errorf("ComputeTier1() score = %.1f, want [%.0f, %.0f]", result.Score, tt.minHeat, tt.maxHeat)
			}
			if result.Tier != 1 {
				t.Errorf("Tier = %d, want 1", result.Tier)
			}
			if result.Confidence != 0.3 {
				t.Errorf("Confidence = %v, want 0.3", result.Confidence)
			}
			if len(result.Signals) == 0 {
				t.Error("Expected signals, got none")
			}
		})
	}
}

func TestComputeTier1_Ordering(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	parentPushed := now.Add(-24 * time.Hour)

	// Popular fork should score higher than zero-star fork
	popular := ComputeTier1(Tier1Params{
		Stars: 100, Forks: 10, OpenIssues: 5,
		ForkSize: 5000, ParentSize: 3000,
		ForkDesc: "Enhanced", ParentDesc: "Original",
		PushedAt: now.Add(-48 * time.Hour), ParentPushedAt: parentPushed, Now: now,
	})
	unpopular := ComputeTier1(Tier1Params{
		Stars: 0, Forks: 0, OpenIssues: 0,
		ForkSize: 1000, ParentSize: 1000,
		ForkDesc: "Original", ParentDesc: "Original",
		PushedAt: now.Add(-365 * 24 * time.Hour), ParentPushedAt: parentPushed, Now: now,
	})

	if popular.Score <= unpopular.Score {
		t.Errorf("Popular fork (%.1f) should score higher than unpopular (%.1f)", popular.Score, unpopular.Score)
	}
}

// ---- V2 Scoring Tests ----

func TestComputeTier1V2_MaxCap(t *testing.T) {
	p := Tier1ParamsV2{
		Stars:            100000,
		SubForks:         100,
		ReleaseCount:     100,
		DaysSincePush:    0,
		DaysSinceUpstream: 0,
	}
	score, comps := ComputeTier1V2(p)
	if score > 40 {
		t.Errorf("T1 score %v exceeds max 40", score)
	}
	if len(comps) != 4 {
		t.Errorf("Expected 4 components, got %d", len(comps))
	}
	for _, c := range comps {
		if c.Points > c.Max+0.001 {
			t.Errorf("Component %q: points %v > max %v", c.Name, c.Points, c.Max)
		}
	}
}

func TestComputeTier1V2_SubForkMultiplier(t *testing.T) {
	base := Tier1ParamsV2{
		Stars:         10,
		SubForks:      1,
		ReleaseCount:  1,
		DaysSincePush: 10,
	}
	boosted := base
	boosted.SubForks = 5 // > 3, triggers 1.5x

	scoreBase, _ := ComputeTier1V2(base)
	scoreBoosted, _ := ComputeTier1V2(boosted)

	if scoreBoosted <= scoreBase {
		t.Errorf("SubFork > 3 should boost T1: base=%v, boosted=%v", scoreBase, scoreBoosted)
	}
}

func TestComputeTier1V2_Monotonicity(t *testing.T) {
	// More stars + more recency should always produce higher T1
	low := Tier1ParamsV2{Stars: 1, DaysSincePush: 365, SubForks: 0}
	high := Tier1ParamsV2{Stars: 100, DaysSincePush: 1, SubForks: 5, ReleaseCount: 3}

	scoreLow, _ := ComputeTier1V2(low)
	scoreHigh, _ := ComputeTier1V2(high)

	if scoreLow >= scoreHigh {
		t.Errorf("T1 not monotonic: low=%v >= high=%v", scoreLow, scoreHigh)
	}
}

func TestComputeTier2V2_MaxCap(t *testing.T) {
	p := Tier2ParamsV2{
		MNA:                100000,
		AheadBy:            1000,
		BehindBy:           0,
		FeatureCommitRatio: 1.0,
	}
	score, comps := ComputeTier2V2(p)
	if score > 40 {
		t.Errorf("T2 score %v exceeds max 40", score)
	}
	for _, c := range comps {
		if c.Points > c.Max+0.001 {
			t.Errorf("Component %q: points %v > max %v", c.Name, c.Points, c.Max)
		}
	}
}

func TestComputeTier2V2_JunkHeavy(t *testing.T) {
	p := Tier2ParamsV2{
		MNA:                5000,
		AheadBy:            50,
		BehindBy:           10,
		FeatureCommitRatio: 0.8,
	}
	normal, _ := ComputeTier2V2(p)

	p.IsJunkHeavy = true
	halved, _ := ComputeTier2V2(p)

	if !approxEqual(halved, normal*0.5, 0.01) {
		t.Errorf("Junk-heavy should halve T2: normal=%v, halved=%v", normal, halved)
	}
}

func TestComputeTier2V2_SyncRatio(t *testing.T) {
	// ahead=100, behind=0 → sync ratio ~1.0 → 15 pts
	highSync := Tier2ParamsV2{AheadBy: 100, BehindBy: 0}
	scoreHigh, _ := ComputeTier2V2(highSync)

	// ahead=10, behind=100 → sync ratio ~0.09 → ~1.4 pts
	lowSync := Tier2ParamsV2{AheadBy: 10, BehindBy: 100}
	scoreLow, _ := ComputeTier2V2(lowSync)

	if scoreLow >= scoreHigh {
		t.Errorf("Higher sync ratio should produce higher T2: low=%v >= high=%v", scoreLow, scoreHigh)
	}
}

func TestComputeTier3V2_MaxCap(t *testing.T) {
	p := Tier3ParamsV2{
		LoneWolf:       &LoneWolfResult{Detected: true, Strength: 1.0, Archetype: ArchetypeFeatureBuilder},
		CommitSpanDays: 365,
	}
	score, _ := ComputeTier3V2(p)
	if score > 20 {
		t.Errorf("T3 score %v exceeds max 20", score)
	}
}

func TestComputeTier3V2_DrifterPenalty(t *testing.T) {
	fb := Tier3ParamsV2{
		LoneWolf:       &LoneWolfResult{Detected: true, Strength: 0.8, Archetype: ArchetypeFeatureBuilder},
		CommitSpanDays: 30,
	}
	scoreFB, _ := ComputeTier3V2(fb)

	drifter := Tier3ParamsV2{
		LoneWolf:       &LoneWolfResult{Detected: true, Strength: 0.8, Archetype: ArchetypeDrifter},
		CommitSpanDays: 30,
	}
	scoreDrifter, _ := ComputeTier3V2(drifter)

	if scoreDrifter >= scoreFB {
		t.Errorf("Drifter should score lower than FeatureBuilder: drifter=%v >= fb=%v", scoreDrifter, scoreFB)
	}
}

func TestComputeTier3V2_NoLoneWolf(t *testing.T) {
	p := Tier3ParamsV2{CommitSpanDays: 60}
	score, comps := ComputeTier3V2(p)

	for _, c := range comps {
		if c.Name == "lone_wolf" && c.Points != 0 {
			t.Errorf("No lone wolf should give 0 pts, got %v", c.Points)
		}
	}

	// Should still have span points
	if score <= 0 {
		t.Errorf("Should have span points, got %v", score)
	}
}

func TestComputeTier3V2_WithNovelty(t *testing.T) {
	p := Tier3ParamsV2{
		LoneWolf:       &LoneWolfResult{Detected: true, Strength: 1.0, Archetype: ArchetypeSniper},
		CommitSpanDays: 180,
		NoveltyScore:   1.0,
	}
	total, comps := ComputeTier3V2(p)
	if total > 20 {
		t.Errorf("Total %v exceeds max 20", total)
	}

	got := map[string]Component{}
	for _, c := range comps {
		got[c.Name] = c
	}
	lw, ok := got["lone_wolf"]
	if !ok {
		t.Fatal("missing lone_wolf component")
	}
	if !approxEqual(lw.Points, 7, 0.001) {
		t.Errorf("lone_wolf Points = %v, want 7", lw.Points)
	}
	if lw.Max != 7 {
		t.Errorf("lone_wolf Max = %v, want 7", lw.Max)
	}

	span, ok := got["span"]
	if !ok {
		t.Fatal("missing span component")
	}
	if !approxEqual(span.Points, 8, 0.05) {
		t.Errorf("span Points = %v, want ~8", span.Points)
	}
	if span.Max != 8 {
		t.Errorf("span Max = %v, want 8", span.Max)
	}

	nov, ok := got["novelty"]
	if !ok {
		t.Fatal("missing novelty component")
	}
	if !approxEqual(nov.Points, 5, 0.001) {
		t.Errorf("novelty Points = %v, want 5", nov.Points)
	}
	if nov.Max != 5 {
		t.Errorf("novelty Max = %v, want 5", nov.Max)
	}
}

func TestComputeTier3V2_NoveltyZero(t *testing.T) {
	p := Tier3ParamsV2{
		CommitSpanDays: 0,
		NoveltyScore:   0,
	}
	total, comps := ComputeTier3V2(p)
	if total != 0 {
		t.Errorf("Total = %v, want 0", total)
	}

	var hasNovelty bool
	for _, c := range comps {
		if c.Name == "novelty" {
			hasNovelty = true
			if c.Points != 0 {
				t.Errorf("novelty Points = %v, want 0", c.Points)
			}
		}
	}
	if !hasNovelty {
		t.Error("missing novelty component")
	}
}

func TestComputeTier3V2_CapAt20(t *testing.T) {
	// Very high inputs across all components: should be capped at 20.
	p := Tier3ParamsV2{
		LoneWolf:       &LoneWolfResult{Detected: true, Strength: 1.0, Archetype: ArchetypeSniper},
		CommitSpanDays: 10000,
		NoveltyScore:   1.0,
	}
	total, _ := ComputeTier3V2(p)
	if total > 20 {
		t.Errorf("Total %v exceeds max 20", total)
	}
	// Should also actually reach (or be near) the cap.
	if total < 19 {
		t.Errorf("Total %v unexpectedly far below cap of 20", total)
	}
}

func TestRawScore_TierProgression(t *testing.T) {
	input := ScoreInput{
		T1: Tier1ParamsV2{Stars: 50, SubForks: 2, DaysSincePush: 5, ReleaseCount: 3},
	}

	t1Result := RawScore(input)
	if t1Result.Tier != 1 {
		t.Errorf("T1-only should be tier 1, got %d", t1Result.Tier)
	}

	input.T2 = &Tier2ParamsV2{MNA: 1000, AheadBy: 20, BehindBy: 5, FeatureCommitRatio: 0.7}
	t2Result := RawScore(input)
	if t2Result.Tier != 2 {
		t.Errorf("With T2 should be tier 2, got %d", t2Result.Tier)
	}
	if t2Result.Score <= t1Result.Score {
		t.Errorf("T2 score should be higher than T1: t1=%v, t2=%v", t1Result.Score, t2Result.Score)
	}

	input.T3 = &Tier3ParamsV2{
		LoneWolf:       &LoneWolfResult{Detected: true, Strength: 0.7, Archetype: ArchetypeFeatureBuilder},
		CommitSpanDays: 45,
	}
	t3Result := RawScore(input)
	if t3Result.Tier != 3 {
		t.Errorf("With T3 should be tier 3, got %d", t3Result.Tier)
	}
	if t3Result.Score <= t2Result.Score {
		t.Errorf("T3 score should be higher than T2: t2=%v, t3=%v", t2Result.Score, t3Result.Score)
	}
}

func TestRawScore_ComponentCount(t *testing.T) {
	input := ScoreInput{
		T1: Tier1ParamsV2{Stars: 10},
		T2: &Tier2ParamsV2{MNA: 500, AheadBy: 10},
		T3: &Tier3ParamsV2{CommitSpanDays: 30},
	}
	result := RawScore(input)

	// 4 T1 + 3 T2 + 3 T3 = 10 components
	if len(result.Components) != 10 {
		t.Errorf("Expected 10 components, got %d", len(result.Components))
	}
}

func TestApplyTrust(t *testing.T) {
	result := HeatResult{Score: 60}

	// High trust: stars 90th pct, subforks 80th pct
	// trust = 0.9*0.7 + 0.8*0.3 = 0.87
	// heat = 60 * (0.7 + 0.3*0.87) = 60 * 0.961 = 57.66
	ApplyTrust(&result, 0.9, 0.8)

	expected := 60 * (0.7 + 0.3*0.87)
	if !approxEqual(result.Score, expected, 0.1) {
		t.Errorf("ApplyTrust: score=%v, want ~%v", result.Score, expected)
	}
	if !approxEqual(result.Trust, 0.87, 0.01) {
		t.Errorf("Trust=%v, want 0.87", result.Trust)
	}
}

func TestApplyTrust_LoneWolfBoost(t *testing.T) {
	result := HeatResult{
		Score:      60,
		LoneWolfV2: &LoneWolfResult{Detected: true, Strength: 0.8},
	}
	ApplyTrust(&result, 0.5, 0.5)

	// trust = 0.5
	// base after trust = 60 * (0.7 + 0.3*0.5) = 60 * 0.85 = 51
	// lone wolf boost: 51 * (1.1 + 0.2*0.8) = 51 * 1.26 = 64.26
	expected := 60 * 0.85 * 1.26
	if !approxEqual(result.Score, expected, 0.5) {
		t.Errorf("LoneWolf boost: score=%v, want ~%v", result.Score, expected)
	}
}

func TestApplyTrust_NoLoneWolfBoostBelowThreshold(t *testing.T) {
	result := HeatResult{
		Score:      60,
		LoneWolfV2: &LoneWolfResult{Detected: true, Strength: 0.5}, // below 0.6
	}
	ApplyTrust(&result, 0.5, 0.5)

	// No boost: heat = 60 * (0.7 + 0.3*0.5) = 51
	expected := 60 * 0.85
	if !approxEqual(result.Score, expected, 0.1) {
		t.Errorf("Low strength should not boost: score=%v, want ~%v", result.Score, expected)
	}
}

func TestApplyPenalties_NoAhead(t *testing.T) {
	result := HeatResult{Score: 75}
	ApplyPenalties(&result, PenaltyInput{AheadAllBranches: 0})

	if result.Score != 0 {
		t.Errorf("ahead=0 should zero score, got %v", result.Score)
	}
	if len(result.Penalties) != 1 || result.Penalties[0] != "no_ahead" {
		t.Errorf("Expected no_ahead penalty, got %v", result.Penalties)
	}
}

func TestApplyPenalties_Archived(t *testing.T) {
	result := HeatResult{Score: 75}
	ApplyPenalties(&result, PenaltyInput{AheadAllBranches: 10, Archived: true})

	if result.Score > 30 {
		t.Errorf("Archived should cap at 30, got %v", result.Score)
	}
}

func TestApplyPenalties_ArchivedAlreadyLow(t *testing.T) {
	result := HeatResult{Score: 20}
	ApplyPenalties(&result, PenaltyInput{AheadAllBranches: 10, Archived: true, RecencyPct: 0.5})

	if result.Score != 20 {
		t.Errorf("Archived with score 20 should stay at 20, got %v", result.Score)
	}
}

func TestApplyPenalties_LowRecency(t *testing.T) {
	result := HeatResult{Score: 60}
	ApplyPenalties(&result, PenaltyInput{AheadAllBranches: 10, RecencyPct: 0.1})

	expected := 60 * 0.7
	if !approxEqual(result.Score, expected, 0.1) {
		t.Errorf("Low recency penalty: score=%v, want ~%v", result.Score, expected)
	}
}

func TestApplyPenalties_Stacking(t *testing.T) {
	// Archived + low recency stack
	result := HeatResult{Score: 80}
	ApplyPenalties(&result, PenaltyInput{AheadAllBranches: 5, Archived: true, RecencyPct: 0.1})

	// Archived caps at 30, then low recency: 30 * 0.7 = 21
	if !approxEqual(result.Score, 21, 0.1) {
		t.Errorf("Stacked penalties: score=%v, want ~21", result.Score)
	}
}

func TestTinySetScore(t *testing.T) {
	result := TinySetScore(10, 5)

	if !result.IsTinySet {
		t.Error("TinySetScore should set IsTinySet=true")
	}
	if result.Score <= 0 || result.Score > 100 {
		t.Errorf("TinySetScore out of range: %v", result.Score)
	}
	if len(result.Components) != 2 {
		t.Errorf("Expected 2 components, got %d", len(result.Components))
	}
}

func TestTinySetScore_ZeroStars(t *testing.T) {
	result := TinySetScore(0, 365)
	// Zero stars + old push → low score
	if result.Score > 30 {
		t.Errorf("Zero stars + old push should be low score, got %v", result.Score)
	}
}

func TestTinySetScore_HighStars(t *testing.T) {
	result := TinySetScore(1000, 1)
	// High stars + very recent → high score
	if result.Score < 50 {
		t.Errorf("High stars + recent push should be high score, got %v", result.Score)
	}
}

func TestIsGhostFork(t *testing.T) {
	t1, _ := time.Parse(time.RFC3339, "2024-01-01T00:00:00Z")
	t2, _ := time.Parse(time.RFC3339, "2024-06-01T00:00:00Z")

	if !IsGhostFork(t1, t1, false) {
		t.Error("Same push time should be ghost")
	}
	if !IsGhostFork(t2, t1, true) {
		t.Error("Archived should be ghost")
	}
	if IsGhostFork(t2, t1, false) {
		t.Error("Different push time, not archived, should NOT be ghost")
	}
}
