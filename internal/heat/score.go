package heat

import (
	"math"
	"time"
)

// Tier1Params are the inputs for Tier 1 scoring (from forks list only).
type Tier1Params struct {
	Stars          int
	Forks          int
	OpenIssues     int
	ForkSize       int
	ParentSize     int
	ForkDesc       string
	ParentDesc     string
	Archived       bool
	PushedAt       time.Time
	ParentPushedAt time.Time
	Now            time.Time
}

// ComputeTier1 computes a heat score using only data from the forks list endpoint.
func ComputeTier1(p Tier1Params) HeatResult {
	now := p.Now
	if now.IsZero() {
		now = time.Now()
	}

	// Relative recency: normalize against parent's activity
	daysSinceForkPush := now.Sub(p.PushedAt).Hours() / 24
	daysSinceParentPush := now.Sub(p.ParentPushedAt).Hours() / 24
	// If parent is dormant, adjust halflife upward
	halflife := 180.0
	if daysSinceParentPush > 365 {
		halflife = math.Max(halflife, daysSinceParentPush*0.5)
	}

	signals := []Signal{
		{
			Name:   "stars",
			Value:  LogNorm(float64(p.Stars), 50),
			Weight: 0.25,
			Raw:    float64(p.Stars),
		},
		{
			Name:   "sub-forks",
			Value:  LogNorm(float64(p.Forks), 10),
			Weight: 0.15,
			Raw:    float64(p.Forks),
		},
		{
			Name:   "recency",
			Value:  DecayNorm(daysSinceForkPush, halflife),
			Weight: 0.30,
			Raw:    daysSinceForkPush,
		},
		{
			Name:   "issues",
			Value:  LogNorm(float64(p.OpenIssues), 5),
			Weight: 0.10,
			Raw:    float64(p.OpenIssues),
		},
		{
			Name:   "size-delta",
			Value:  LogNorm(math.Abs(float64(p.ForkSize-p.ParentSize)), 500),
			Weight: 0.10,
			Raw:    math.Abs(float64(p.ForkSize - p.ParentSize)),
		},
		{
			Name:   "description",
			Value:  boolToFloat(p.ForkDesc != "" && p.ForkDesc != p.ParentDesc),
			Weight: 0.05,
			Raw:    boolToFloat(p.ForkDesc != "" && p.ForkDesc != p.ParentDesc),
		},
		{
			Name:   "not-archived",
			Value:  boolToFloat(!p.Archived),
			Weight: 0.05,
			Raw:    boolToFloat(!p.Archived),
		},
	}

	score := weightedSum(signals)

	return HeatResult{
		Score:      clampScore(score * 100),
		Tier:       1,
		Confidence: 0.3,
		Signals:    signals,
	}
}

// Tier2Params extends Tier1Params with compare data.
type Tier2Params struct {
	Tier1Params

	AheadBy       int
	BehindBy      int
	FilesChanged  int
	TotalAdds     int
	TotalDels     int
	UniqueAuthors int
	Diverged      bool
}

// ComputeTier2 computes a heat score using fork list + compare data.
func ComputeTier2(p Tier2Params) HeatResult {
	now := p.Now
	if now.IsZero() {
		now = time.Now()
	}

	daysSinceForkPush := now.Sub(p.PushedAt).Hours() / 24
	daysSinceParentPush := now.Sub(p.ParentPushedAt).Hours() / 24
	halflife := 180.0
	if daysSinceParentPush > 365 {
		halflife = math.Max(halflife, daysSinceParentPush*0.5)
	}

	netAdds := p.TotalAdds - p.TotalDels
	if netAdds < 0 {
		netAdds = 0
	}

	// Tier 1 signals (reweighted: 0.45 total)
	t1Signals := []Signal{
		{Name: "stars", Value: LogNorm(float64(p.Stars), 50), Weight: 0.10, Raw: float64(p.Stars)},
		{Name: "sub-forks", Value: LogNorm(float64(p.Forks), 10), Weight: 0.05, Raw: float64(p.Forks)},
		{Name: "recency", Value: DecayNorm(daysSinceForkPush, halflife), Weight: 0.15, Raw: daysSinceForkPush},
		{Name: "issues", Value: LogNorm(float64(p.OpenIssues), 5), Weight: 0.05, Raw: float64(p.OpenIssues)},
		{Name: "size-delta", Value: LogNorm(math.Abs(float64(p.ForkSize-p.ParentSize)), 500), Weight: 0.05, Raw: math.Abs(float64(p.ForkSize - p.ParentSize))},
		{Name: "description", Value: boolToFloat(p.ForkDesc != "" && p.ForkDesc != p.ParentDesc), Weight: 0.03, Raw: boolToFloat(p.ForkDesc != "" && p.ForkDesc != p.ParentDesc)},
		{Name: "not-archived", Value: boolToFloat(!p.Archived), Weight: 0.02, Raw: boolToFloat(!p.Archived)},
	}

	// Tier 2 signals (0.55 total)
	t2Signals := []Signal{
		{Name: "ahead", Value: LogNorm(float64(p.AheadBy), 100), Weight: 0.20, Raw: float64(p.AheadBy)},
		{Name: "behind", Value: InverseLogNorm(float64(p.BehindBy), 200), Weight: 0.08, Raw: float64(p.BehindBy)},
		{Name: "files-changed", Value: LogNorm(float64(p.FilesChanged), 50), Weight: 0.08, Raw: float64(p.FilesChanged)},
		{Name: "net-additions", Value: LogNorm(float64(netAdds), 2000), Weight: 0.10, Raw: float64(netAdds)},
		{Name: "impact", Value: LogNorm(float64(p.TotalAdds+p.TotalDels), 5000), Weight: 0.04, Raw: float64(p.TotalAdds + p.TotalDels)},
		{Name: "authors", Value: LogNorm(float64(p.UniqueAuthors), 5), Weight: 0.05, Raw: float64(p.UniqueAuthors)},
	}

	signals := append(t1Signals, t2Signals...)
	score := weightedSum(signals)

	confidence := 0.7
	if p.Diverged {
		confidence = 0.5
	}

	return HeatResult{
		Score:      clampScore(score * 100),
		Tier:       2,
		Confidence: confidence,
		Signals:    signals,
	}
}

func weightedSum(signals []Signal) float64 {
	var sum float64
	for _, s := range signals {
		sum += s.Value * s.Weight
	}
	return sum
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func clampScore(s float64) float64 {
	if s < 0 {
		return 0
	}
	if s > 100 {
		return 100
	}
	return s
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
}

// ComputeTier3V2 scores T3 with additive point budgets.
func ComputeTier3V2(p Tier3ParamsV2) (float64, []Component) {
	// Lone Wolf: strength * 10, with archetype multiplier
	var lwPts float64
	var lwRaw float64
	if p.LoneWolf != nil && p.LoneWolf.Detected {
		lwRaw = p.LoneWolf.Strength
		lwPts = p.LoneWolf.Strength * 10
		switch p.LoneWolf.Archetype {
		case ArchetypeSniper:
			// Full credit — no change
		case ArchetypeFeatureBuilder:
			// Full credit — no change
		case ArchetypeDrifter:
			lwPts *= 0.8
		}
	}
	if lwPts > 10 {
		lwPts = 10
	}

	// Commit span: logNorm(spanDays, 180) * 10
	span := LogNormRange(p.CommitSpanDays, 180, 10)

	total := lwPts + span
	if total > 20 {
		total = 20
	}

	components := []Component{
		{Name: "lone_wolf", Points: lwPts, Max: 10, Raw: lwRaw},
		{Name: "span", Points: span, Max: 10, Raw: p.CommitSpanDays},
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
