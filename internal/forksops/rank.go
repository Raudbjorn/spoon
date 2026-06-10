package forksops

import "math"

// Robbins expected-rank shortlisting. Rather than maximizing the chance of
// picking the single best fork, we minimize each fork's *expected rank* under
// uncertainty — a better fit for "give me the top-k worth integrating, with
// confidence." Computed over the enriched set only (budget/topN candidates), so
// the O(n^2) pass is cheap.

// normalCDF is Φ(x), the standard normal CDF.
func normalCDF(x float64) float64 {
	return 0.5 * math.Erfc(-x/math.Sqrt2)
}

// rankSigmaScale maps heat-score confidence to an uncertainty (sigma): a
// T1-only fork (low confidence) gets a wide posterior, so a high surface score
// with no compare evidence ranks below a confirmed-divergent fork of similar
// score. Heuristic, in heat-score points.
const rankSigmaScale = 10.0

// rankSigma derives a fork's utility uncertainty from its heat Confidence
// (tier-based: ~0.3 T1 / 0.7 T2 / 0.9 T3). Never zero, to avoid degeneracy.
func rankSigma(confidence float64) float64 {
	s := (1 - confidence) * rankSigmaScale
	if s < 1e-6 {
		s = 1e-6
	}
	return s
}

// expectedRanks computes, for each item, its Robbins expected rank from the
// posterior utility mu and uncertainty sigma, modeling each true utility as
// Normal(mu_i, sigma_i^2):
//
//	E_rank(i) = 1 + Σ_{j≠i} P(U_j > U_i)
//	          = 1 + Σ_{j≠i} Φ((mu_j - mu_i) / sqrt(sigma_i^2 + sigma_j^2))
//
// Lower is better (rank 1 ≈ most likely the best). Length matches mu/sigma.
func expectedRanks(mu, sigma []float64) []float64 {
	n := len(mu)
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		rank := 1.0
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			denom := math.Sqrt(sigma[i]*sigma[i] + sigma[j]*sigma[j])
			if denom < 1e-9 {
				// No uncertainty → deterministic comparison with a stable tiebreak.
				switch {
				case mu[j] > mu[i]:
					rank++
				case mu[j] == mu[i] && j < i:
					rank += 0.5
				}
				continue
			}
			rank += normalCDF((mu[j] - mu[i]) / denom)
		}
		out[i] = rank
	}
	return out
}
