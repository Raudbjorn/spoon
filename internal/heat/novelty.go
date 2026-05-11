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
func ApplyNoveltyToScore(hr *HeatResult) {
	if hr == nil || hr.NoveltyScore <= 0 {
		return
	}
	n := hr.NoveltyScore
	if n > 1 {
		n = 1
	}
	hr.Score += n * 5
	if hr.Score > 100 {
		hr.Score = 100
	}
}
