package heat

import (
	"math"

	"github.com/svnbjrn/spoon/internal/forge"
)

// ---- V2 Scoring API (additive point tiers) ----

// Tier1ParamsV2 are the inputs for T1 scoring (max 40 pts).
type Tier1ParamsV2 struct {
	Stars             int
	SubForks          int
	ReleaseCount      int
	DaysSincePush     float64
	DaysSinceUpstream float64
}

// tier point budgets.
const (
	tier1Max = 40
	tier2Max = 40
	tier3Max = 20
)

// computeTier1Components scores the T1 signals as unweighted components.
func computeTier1Components(p Tier1ParamsV2) []Component {
	// Recency: expDecay against an upstream-relative half-life — a fork of
	// a slow-moving upstream shouldn't be punished for moving at the
	// upstream's pace.
	halfLife := 30.0
	if p.DaysSinceUpstream > 365 {
		halfLife = math.Max(halfLife, p.DaysSinceUpstream*0.25)
	}
	recency := ExpDecay(p.DaysSincePush, halfLife) * 12

	stars := LogNormRange(float64(p.Stars), 10000, 12)

	// Sub-forks: linear up to 8. (A previous revision also applied a 1.5x
	// multiplier to all of T1 above 3 sub-forks; combined with these points
	// and the 30% sub-fork share of the trust multiplier that counted the
	// same signal three times, so the multiplier was removed.)
	subForks := math.Min(float64(p.SubForks)*2, 8)

	releases := LogNormRange(float64(p.ReleaseCount), 20, 8)

	return []Component{
		{Name: "recency", Points: recency, Max: 12, Raw: p.DaysSincePush},
		{Name: "stars", Points: stars, Max: 12, Raw: float64(p.Stars)},
		{Name: "sub_forks", Points: subForks, Max: 8, Raw: float64(p.SubForks)},
		{Name: "releases", Points: releases, Max: 8, Raw: float64(p.ReleaseCount)},
	}
}

// ComputeTier1V2 scores T1 with additive point budgets.
// Returns total T1 score and component breakdown.
func ComputeTier1V2(p Tier1ParamsV2) (float64, []Component) {
	comps := computeTier1Components(p)
	return capSum(comps, tier1Max), comps
}

// Tier2ParamsV2 are the inputs for the T2 scoring (max 40 pts).
type Tier2ParamsV2 struct {
	MNA                int // meaningful net additions
	AheadBy            int
	BehindBy           int
	FeatureCommitRatio float64 // fraction of non-merge, non-sync commits
	IsJunkHeavy        bool    // >90% junk/docs files
}

func computeTier2Components(p Tier2ParamsV2) []Component {
	mna := LogNormRange(float64(p.MNA), 20000, 15)

	// Divergence ownership: how much of the fork↔upstream divergence is the
	// fork's own work. Behind-counts grow mechanically with upstream
	// velocity, so they enter under a square root — otherwise a 50-ahead
	// fork tracking a fast upstream (50/251 ≈ 0.2) loses to a 1-ahead fork
	// of a dead repo (1/2 = 0.5). With the damping the same pair scores
	// ~0.77 vs 0.5.
	syncRatio := float64(p.AheadBy) / (float64(p.AheadBy) + math.Sqrt(float64(p.BehindBy)) + 1)
	sync := syncRatio * 15

	feature := p.FeatureCommitRatio * 10

	comps := []Component{
		{Name: "mna", Points: mna, Max: 15, Raw: float64(p.MNA)},
		{Name: "sync_ratio", Points: sync, Max: 15, Raw: syncRatio},
		{Name: "feature_ratio", Points: feature, Max: 10, Raw: p.FeatureCommitRatio},
	}
	// Halve everything if junk-heavy (>90% docs/vendored churn).
	if p.IsJunkHeavy {
		for i := range comps {
			comps[i].Points *= 0.5
		}
	}
	return comps
}

// ComputeTier2V2 scores T2 with additive point budgets.
func ComputeTier2V2(p Tier2ParamsV2) (float64, []Component) {
	comps := computeTier2Components(p)
	return capSum(comps, tier2Max), comps
}

// Tier3ParamsV2 are the inputs for the T3 scoring (max 20 pts).
type Tier3ParamsV2 struct {
	LoneWolf       *LoneWolfResult
	CommitSpanDays float64
	NoveltyScore   float64 // 0..1
}

func computeTier3Components(p Tier3ParamsV2) []Component {
	// Lone Wolf: strength * 7, with archetype multiplier (Drifter * 0.8).
	var lwPts float64
	var lwRaw float64
	if p.LoneWolf != nil && p.LoneWolf.Detected {
		lwRaw = p.LoneWolf.Strength
		lwPts = p.LoneWolf.Strength * 7
		if p.LoneWolf.Archetype == ArchetypeDrifter {
			lwPts *= 0.8
		}
	}
	if lwPts > 7 {
		lwPts = 7
	}

	span := LogNormRange(p.CommitSpanDays, 180, 8)

	return []Component{
		{Name: "lone_wolf", Points: lwPts, Max: 7, Raw: lwRaw},
		{Name: "span", Points: span, Max: 8, Raw: p.CommitSpanDays},
		NoveltyComponent(p.NoveltyScore),
	}
}

// ComputeTier3V2 scores T3 with additive point budgets. Max 20 points.
//
//	lone_wolf : 0..7
//	span      : 0..8
//	novelty   : 0..5
func ComputeTier3V2(p Tier3ParamsV2) (float64, []Component) {
	comps := computeTier3Components(p)
	return capSum(comps, tier3Max), comps
}

// ScoreInput collects all inputs for the full scoring pipeline.
type ScoreInput struct {
	T1 Tier1ParamsV2
	T2 *Tier2ParamsV2 // nil if not enriched yet
	T3 *Tier3ParamsV2 // nil if not enriched yet
}

// PenaltyInput collects conditions for post-scoring penalties.
type PenaltyInput struct {
	// AheadKnown reports whether divergence was actually measured (T2 ran).
	// The no-ahead zeroing only applies when it was — an unenriched fork is
	// "unknown", not "no work".
	AheadKnown bool
	// AheadAllBranches is the measured ahead count; 0 with AheadKnown means
	// the fork contains no work of its own.
	AheadAllBranches int
	// Upstreamed reports that the fork's divergent branch tip heads a merged
	// upstream PR — the "ahead" work is already integrated and there is nothing
	// left to pull. Distinct from AheadAllBranches==0: the commit graph still
	// shows real divergence, it just isn't novel.
	Upstreamed bool
	Archived   bool
	// RecencyPct is the fork's recency percentile within the fork set
	// (1 = pushed most recently).
	RecencyPct float64
	// ForkTopics is the fork's topic set; nil/empty means "no signal".
	// Used by the P1 topic-tag penalty.
	ForkTopics []string
	// ParentTopics is the parent's topic set; nil/empty disables the
	// P1 deviation comparison (we have no reference to compare against).
	ParentTopics []string
	// OwnerProfile is the fork-owner's farmer signal (P3). Nil means
	// "no signal" — the fetch was skipped, failed, or the provider
	// does not implement the owner-profile path.
	OwnerProfile *forge.OwnerProfile
}

// RawScore computes the additive tier scores and returns a HeatResult.
// Does NOT apply trust or penalties — Scorer.Finalize does that.
func RawScore(input ScoreInput) HeatResult {
	return RawScoreWeighted(input, nil)
}

// RawScoreWeighted is RawScore with per-component weight overrides
// (component name → factor in [0,2]; missing names default to 1). Weights
// scale each component's points before tier caps apply, so they reshape the
// ranking without inflating the 0–100 scale.
func RawScoreWeighted(input ScoreInput, weights map[string]float64) HeatResult {
	t1Comps := applyComponentWeights(computeTier1Components(input.T1), weights)
	t1Score := capSum(t1Comps, tier1Max)

	tier := 1
	var t2Score float64
	var t2Comps []Component
	if input.T2 != nil {
		t2Comps = applyComponentWeights(computeTier2Components(*input.T2), weights)
		t2Score = capSum(t2Comps, tier2Max)
		tier = 2
	}

	var t3Score float64
	var t3Comps []Component
	if input.T3 != nil {
		t3Comps = applyComponentWeights(computeTier3Components(*input.T3), weights)
		t3Score = capSum(t3Comps, tier3Max)
		tier = 3
	}

	signal := t1Score + t2Score + t3Score

	components := make([]Component, 0, len(t1Comps)+len(t2Comps)+len(t3Comps))
	components = append(components, t1Comps...)
	components = append(components, t2Comps...)
	components = append(components, t3Comps...)

	return HeatResult{
		Score:               signal,
		Tier:                tier,
		Confidence:          tierConfidence(tier),
		Components:          components,
		TierScores:          [3]float64{t1Score, t2Score, t3Score},
		noveltyWeight:       weightFor(weights, "novelty"),
		noveltyWeightSet:    true,
		siblingSimWeight:    weightFor(weights, "sibling_sim"),
		siblingSimWeightSet: true,
	}
}

// applyComponentWeights scales each component's points by its weight.
func applyComponentWeights(comps []Component, weights map[string]float64) []Component {
	if len(weights) == 0 {
		return comps
	}
	for i := range comps {
		comps[i].Points *= weightFor(weights, comps[i].Name)
	}
	return comps
}

func weightFor(weights map[string]float64, name string) float64 {
	if weights == nil {
		return 1
	}
	w, ok := weights[name]
	if !ok {
		return 1
	}
	return w
}

// capSum sums component points, capped at max.
func capSum(comps []Component, max float64) float64 {
	var total float64
	for _, c := range comps {
		total += c.Points
	}
	if total > max {
		return max
	}
	return total
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

// ApplyPenalties applies post-trust penalties. Floor is 0. weights is
// the per-component factor map; "topic_tag" and "fork_farmer" are
// consulted here, and any new penalty components should be added
// to this list so the weight plumbing stays discoverable. Other
// weights (recency, novelty, sibling_sim, ...) are applied upstream
// in RawScoreWeighted or in their own Apply*ToScore helpers. Pass
// nil to use defaults (factor 1 for every penalty).
func ApplyPenalties(result *HeatResult, p PenaltyInput, weights map[string]float64) {
	if p.AheadKnown && p.AheadAllBranches == 0 {
		result.Score = 0
		result.Penalties = append(result.Penalties, "no_ahead")
		return
	}

	// Divergent work already merged upstream → nothing to integrate; score 0.
	if p.Upstreamed {
		result.Score = 0
		result.Penalties = append(result.Penalties, "upstreamed")
		return
	}

	// Archived → cap at 30
	if p.Archived {
		if result.Score > 30 {
			result.Score = 30
		}
		result.Penalties = append(result.Penalties, "archived")
	}

	// Bottom-quintile recency within the fork set → 0.7x
	if p.RecencyPct < 0.2 {
		result.Score *= 0.7
		result.Penalties = append(result.Penalties, "low_recency")
	}

	// P1 topic-tag fitness: penalize forks whose topic set diverges
	// sharply from the parent's, gated on real work
	// (AheadAllBranches > 0) and on at least one topic on each side.
	// Apply before the floor so the result lands at 0 when the penalty
	// would have driven it below.
	if pen := TopicTagPenaltyFromWeights(p.ForkTopics, p.ParentTopics, p, weights); pen < 0 {
		result.Score += pen
		if result.Score < 0 {
			result.Score = 0
		}
		result.Penalties = append(result.Penalties, "topic_tag")
	}

	// P3 fork-farmer penalty: owner has many mostly-fork repos and few
	// of their own. Cached upstream (FetchUserRepos) with a hard cap
	// of 30 distinct owners per run; nil OwnerProfile means "no
	// signal" → no penalty.
	if pen := forkFarmerFromWeights(p.OwnerProfile, weights); pen < 0 {
		result.Score += pen
		if result.Score < 0 {
			result.Score = 0
		}
		result.Penalties = append(result.Penalties, "fork_farmer")
	}

	if result.Score < 0 {
		result.Score = 0
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
