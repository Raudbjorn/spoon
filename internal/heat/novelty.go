package heat

// NoveltyComponent converts a 0..1 novelty score to a T3 Component (max 5).
// Linear: Points = clamp(novelty, 0, 1) * 5. Raw carries the input novelty.
func NoveltyComponent(novelty float64) Component {
	n := novelty
	if n < 0 {
		n = 0
	} else if n > 1 {
		n = 1
	}
	return Component{Name: "novelty", Points: n * 5, Max: 5, Raw: novelty}
}

// ApplyNoveltyToScore folds a previously-populated NoveltyScore into
// HeatResult.Score. It adds clamp(NoveltyScore, 0, 1) * 5 points to Score,
// capped at 100. A nil or non-positive-novelty HeatResult is a no-op.
//
// Use case: the cluster pipeline populates HeatResult.NoveltyScore after the
// initial scoring pass has already run. Both v1 and v2 callers reach this
// helper because in practice neither pre-populates Tier3ParamsV2.NoveltyScore
// before the initial ScoreRaw — novelty is a strictly post-cluster signal.
// The original scoring pass's trust multiplier and penalties remain applied;
// the novelty contribution lands on top. +5 matches the v2 NoveltyComponent's
// Max so the heat score remains comparable across scoring paths.
//
// The function is intentionally NOT idempotent: each call adds up to +5
// points. Call it exactly once per HeatResult, immediately after the cluster
// pipeline populates NoveltyScore.
//
// Design choice — clustered vs unclustered asymmetry:
//
// Novelty is layered on top of percentile-based heat as a separate axis, NOT
// rescaled as a percentile-adjusted signal. Forks without cluster data
// (NoveltyScore == 0) receive no bump; clustered forks gain up to +5 above
// the rest of the population's distribution. Within a single run this is
// fine — agents can compare clustered and unclustered scores meaningfully,
// and the bonus is bounded (+5 of a 100-point budget). Across runs where
// clustering was on for one and off for the other, the same fork's score may
// differ by up to +5; that delta is the visible cost of the design choice.
// If you need cross-run comparability, hold the clustering toggle constant.
func ApplyNoveltyToScore(hr *HeatResult) {
	if hr == nil || hr.NoveltyScore <= 0 {
		return
	}
	n := hr.NoveltyScore
	if n > 1 {
		n = 1
	}
	w := 1.0
	if hr.noveltyWeightSet {
		w = hr.noveltyWeight
	}
	hr.Score += n * 5 * w
	if hr.Score > 100 {
		hr.Score = 100
	}
}
