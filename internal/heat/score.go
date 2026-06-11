package heat

import (
	"math"
	"time"
)

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// ---- V2 Scoring API (additive point tiers) ----

// Tier1ParamsV2 are the inputs for the new T1 scoring (max 40 pts).
type Tier1ParamsV2 struct {
	Stars            int
	SubForks         int
	ReleaseCount     int
	DaysSincePush    float64
	DaysSinceUpstream float64
	Archived         bool
	ForkCount        int
	Now              time.Time
}

// ComputeTier1V2 scores T1 with additive point budgets.
// Returns total T1 score and component breakdown.
func ComputeTier1V2(p Tier1ParamsV2) (float64, []Component) {
	// Recency: expDecay against upstream-relative halflife, max 12
	halfLife := 30.0
	if p.DaysSinceUpstream > 365 {
		halfLife = math.Max(halfLife, p.DaysSinceUpstream*0.25)
	}
	recencyNorm := ExpDecay(p.DaysSincePush, halfLife)
	recency := recencyNorm * 12

	// Stars: logNorm scaled to max 12
	stars := LogNormRange(float64(p.Stars), 10000, 12)

	// Sub-forks: min(subForks*2, 8)
	subForks := math.Min(float64(p.SubForks)*2, 8)

	// Releases: logNorm scaled to max 8
	releases := LogNormRange(float64(p.ReleaseCount), 20, 8)

	// Sub-fork multiplier: if subForks > 3, apply 1.5x to total T1 (capped at 40)
	total := recency + stars + subForks + releases
	multiplied := false
	if p.SubForks > 3 {
		total *= 1.5
		multiplied = true
	}
	if total > 40 {
		total = 40
	}

	components := []Component{
		{Name: "recency", Points: recency, Max: 12, Raw: p.DaysSincePush},
		{Name: "stars", Points: stars, Max: 12, Raw: float64(p.Stars)},
		{Name: "sub_forks", Points: subForks, Max: 8, Raw: float64(p.SubForks)},
		{Name: "releases", Points: releases, Max: 8, Raw: float64(p.ReleaseCount)},
	}

	_ = multiplied // used in future detail display
	return total, components
}

// Tier2ParamsV2 are the inputs for the new T2 scoring (max 40 pts).
type Tier2ParamsV2 struct {
	MNA                int     // meaningful net additions
	AheadBy            int
	BehindBy           int
	FeatureCommitRatio float64 // fraction of non-merge, non-sync commits
	IsJunkHeavy        bool    // >90% junk/docs files
}

// ComputeTier2V2 scores T2 with additive point budgets.
func ComputeTier2V2(p Tier2ParamsV2) (float64, []Component) {
	// MNA: logNorm scaled to max 15
	mna := LogNormRange(float64(p.MNA), 20000, 15)

	// Sync ratio: ahead / (ahead + behind + 1) * 15
	syncRatio := float64(p.AheadBy) / (float64(p.AheadBy) + float64(p.BehindBy) + 1)
	sync := syncRatio * 15

	// Feature commit ratio: scaled to max 10
	feature := p.FeatureCommitRatio * 10

	total := mna + sync + feature

	// Halve if junk-heavy
	if p.IsJunkHeavy {
		total *= 0.5
	}
	if total > 40 {
		total = 40
	}

	components := []Component{
		{Name: "mna", Points: mna, Max: 15, Raw: float64(p.MNA)},
		{Name: "sync_ratio", Points: sync, Max: 15, Raw: syncRatio},
		{Name: "feature_ratio", Points: feature, Max: 10, Raw: p.FeatureCommitRatio},
	}

	return total, components
}

// Tier3ParamsV2 are the inputs for the new T3 scoring (max 20 pts).
type Tier3ParamsV2 struct {
	LoneWolf       *LoneWolfResult
	CommitSpanDays float64
	NoveltyScore   float64 // 0..1
}

// ComputeTier3V2 scores T3 with additive point budgets. Max 20 points.
//
//	lone_wolf : 0..7   (was 0..10)
//	span      : 0..8   (was 0..10)
//	novelty   : 0..5   (new)
func ComputeTier3V2(p Tier3ParamsV2) (float64, []Component) {
	// Lone Wolf: strength * 7, with archetype multiplier (Drifter * 0.8).
	var lwPts float64
	var lwRaw float64
	if p.LoneWolf != nil && p.LoneWolf.Detected {
		lwRaw = p.LoneWolf.Strength
		lwPts = p.LoneWolf.Strength * 7
		switch p.LoneWolf.Archetype {
		case ArchetypeSniper:
			// Full credit — no change
		case ArchetypeFeatureBuilder:
			// Full credit — no change
		case ArchetypeDrifter:
			lwPts *= 0.8
		}
	}
	if lwPts > 7 {
		lwPts = 7
	}

	// Commit span: logNorm(spanDays, 180) * 8.
	span := LogNormRange(p.CommitSpanDays, 180, 8)

	// Novelty: NoveltyComponent helper returns up to 5.
	noveltyComp := NoveltyComponent(p.NoveltyScore)

	total := lwPts + span + noveltyComp.Points
	if total > 20 {
		total = 20
	}

	components := []Component{
		{Name: "lone_wolf", Points: lwPts, Max: 7, Raw: lwRaw},
		{Name: "span", Points: span, Max: 8, Raw: p.CommitSpanDays},
		noveltyComp,
	}

	return total, components
}

// ScoreInput collects all inputs for the full scoring pipeline.
type ScoreInput struct {
	T1 Tier1ParamsV2
	T2 *Tier2ParamsV2 // nil if not enriched yet
	T3 *Tier3ParamsV2 // nil if not enriched yet
}

// PenaltyInput collects conditions for post-scoring penalties.
type PenaltyInput struct {
	AheadAllBranches int  // sum of ahead across all scanned branches; 0 means no work
	Archived         bool
	RecencyPct       float64 // percentile of recency within the fork set
}

// RawScore computes the additive tier scores and returns a HeatResult.
// Does NOT apply trust or penalties — call ApplyTrust and ApplyPenalties after.
func RawScore(input ScoreInput) HeatResult {
	t1Score, t1Comps := ComputeTier1V2(input.T1)

	tier := 1
	var t2Score float64
	var t2Comps []Component
	if input.T2 != nil {
		t2Score, t2Comps = ComputeTier2V2(*input.T2)
		tier = 2
	}

	var t3Score float64
	var t3Comps []Component
	if input.T3 != nil {
		t3Score, t3Comps = ComputeTier3V2(*input.T3)
		tier = 3
	}

	signal := t1Score + t2Score + t3Score

	components := make([]Component, 0, len(t1Comps)+len(t2Comps)+len(t3Comps))
	components = append(components, t1Comps...)
	components = append(components, t2Comps...)
	components = append(components, t3Comps...)

	return HeatResult{
		Score:      signal,
		Tier:       tier,
		Confidence: tierConfidence(tier),
		Components: components,
		TierScores: [3]float64{t1Score, t2Score, t3Score},
	}
}

// ApplyTrust multiplies the raw score by the trust factor.
// trust = starsPct * 0.7 + subForksPct * 0.3
// heat = signal * (0.7 + 0.3 * trust)
// Lone wolf boost: if strength > 0.6, heat *= 1.1 + 0.2*strength
func ApplyTrust(result *HeatResult, starsPct, subForksPct float64) {
	trust := starsPct*0.7 + subForksPct*0.3
	result.Trust = trust

	result.Score = result.Score * (0.7 + 0.3*trust)

	// Lone wolf boost
	if result.LoneWolfV2 != nil && result.LoneWolfV2.Detected && result.LoneWolfV2.Strength > 0.6 {
		result.Score *= 1.1 + 0.2*result.LoneWolfV2.Strength
	}

	if result.Score > 100 {
		result.Score = 100
	}
}

// ApplyPenalties applies post-trust penalties. Floor is 0.
func ApplyPenalties(result *HeatResult, p PenaltyInput) {
	// ahead == 0 on all branches → score 0
	if p.AheadAllBranches == 0 {
		result.Score = 0
		result.Penalties = append(result.Penalties, "no_ahead")
		return
	}

	// Archived → cap at 30
	if p.Archived {
		if result.Score > 30 {
			result.Score = 30
		}
		result.Penalties = append(result.Penalties, "archived")
	}

	// Low recency percentile → 0.7x
	if p.RecencyPct < 0.2 {
		result.Score *= 0.7
		result.Penalties = append(result.Penalties, "low_recency")
	}

	if result.Score < 0 {
		result.Score = 0
	}
}

// TinySetScore produces a simplified score for repos with < 10 forks.
// No percentile, no heat bar — just logNorm(stars) + recency.
func TinySetScore(stars int, daysSincePush float64) HeatResult {
	starsNorm := LogNorm(float64(stars), 100)
	recencyNorm := ExpDecay(daysSincePush, 90)
	score := (starsNorm + recencyNorm) * 50 // scale to 0-100 range

	if score > 100 {
		score = 100
	}

	return HeatResult{
		Score:     score,
		Tier:      1,
		IsTinySet: true,
		Components: []Component{
			{Name: "stars", Points: starsNorm * 50, Max: 50, Raw: float64(stars)},
			{Name: "recency", Points: recencyNorm * 50, Max: 50, Raw: daysSincePush},
		},
	}
}

func tierConfidence(tier int) float64 {
	switch tier {
	case 1:
		return 0.3
	case 2:
		return 0.7
	case 3:
		return 0.9
	default:
		return 0.3
	}
}
