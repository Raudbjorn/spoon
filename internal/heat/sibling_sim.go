package heat

// CombinedNoveltyBonusCap bounds the sum of cluster-novelty bonus and
// sibling-similarity bonus. Both can add up to +5 individually; we cap
// the combined contribution at +7.5 to prevent a single fork from
// accumulating +10 of post-hoc bonus above the percentile distribution.
//
// Math: NoveltyScore in [0, 1] maps to up to +5 (ApplyNoveltyToScore).
// SiblingSim in [0, 1] maps to up to +5 (ApplySiblingSimilarityToScore).
// Combined cap = 7.5 — leaves 2.5 of headroom for the second signal to
// contribute when the first is at maximum.
const CombinedNoveltyBonusCap = 7.5

// ApplySiblingSimilarityToScore mirrors ApplyNoveltyToScore. It adds
// clamp(SiblingSim, 0, 1) * 5 * weight to Score, capped at the
// combined-novelty-bonus cap (shared with the cluster-novelty bonus).
// A nil or non-positive-SiblingSim HeatResult is a no-op.
//
// Intentionally NOT idempotent. Call exactly once per HeatResult,
// after the cluster pipeline has populated both NoveltyScore and
// SiblingSim.
func ApplySiblingSimilarityToScore(hr *HeatResult) {
	if hr == nil || hr.SiblingSim <= 0 {
		return
	}
	s := hr.SiblingSim
	if s > 1 {
		s = 1
	}
	w := 1.0
	if hr.siblingSimWeightSet {
		w = hr.siblingSimWeight
	}
	// Compute the combined post-hoc bonus from the two available
	// signals. Each is in [0, 1] and maps to up to +5. The cluster
	// bonus is scaled by noveltyWeight to mirror ApplyNoveltyToScore
	// (which also weights novelty), so the cap math stays consistent
	// when the user has nudged novelty above or below 1.0.
	clusterBonus := 0.0
	if hr.NoveltyScore > 0 {
		n := hr.NoveltyScore
		if n > 1 {
			n = 1
		}
		clusterBonus = n * 5
		if hr.noveltyWeightSet {
			clusterBonus *= hr.noveltyWeight
		}
	}
	sibBonus := s * 5 * w
	combined := clusterBonus + sibBonus
	if combined > CombinedNoveltyBonusCap {
		sibBonus = CombinedNoveltyBonusCap - clusterBonus
		if sibBonus < 0 {
			sibBonus = 0
		}
	}
	hr.Score += sibBonus
	if hr.Score > 100 {
		hr.Score = 100
	}
}
