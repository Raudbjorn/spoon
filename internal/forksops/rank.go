package forksops

import (
	"math"
	"sort"
)

// Robbins expected-rank shortlisting. Rather than maximizing the chance of
// picking the single best fork, we minimize each fork's *expected rank* under
// uncertainty — a better fit for "give me the top-k worth integrating, with
// confidence." The pass is O(n^2) in the number of forks ranked; the caller
// (stream.go) bounds it to the strongest rankPoolCap candidates by heat so it
// stays cheap even on huge fork networks.

// rankPoolCap bounds the O(n^2) expected-rank pass: only the strongest
// candidates by surface heat are ranked (a fork outside this pool would not
// make a small shortlist anyway).
const rankPoolCap = 200

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

// winProbs returns the pairwise win matrix p[i][j] = P(U_i > U_j) for
// independent utilities U ~ N(mu, sigma²): Φ((mu_i − mu_j)/√(σ_i²+σ_j²)).
// p[i][i] is 0 and p[i][j] + p[j][i] = 1. When both uncertainties vanish
// the comparison is deterministic with a stable index tiebreak (0.5 on an
// exact tie, matching the historical expectedRanks behaviour).
func winProbs(mu, sigma []float64) [][]float64 {
	n := len(mu)
	p := make([][]float64, n)
	for i := range p {
		p[i] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			denom := math.Hypot(sigma[i], sigma[j])
			var pij float64
			if denom < 1e-9 {
				switch {
				case mu[i] > mu[j]:
					pij = 1
				case mu[i] < mu[j]:
					pij = 0
				default:
					pij = 0.5
				}
			} else {
				pij = normalCDF((mu[i] - mu[j]) / denom)
			}
			p[i][j] = pij
			p[j][i] = 1 - pij
		}
	}
	return p
}

// expectedRanksFrom computes the Robbins expected rank from a win matrix:
// E_rank(i) = 1 + Σ_{j≠i} P(U_j > U_i) = 1 + Σ_{j≠i} (1 − p[i][j]).
// Lower is better. Under independent Gaussians this is also
// n − (n−1)·PScore (Rücker & Schwarzer 2015 identity).
func expectedRanksFrom(p [][]float64) []float64 {
	n := len(p)
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		rank := 1.0
		for j := 0; j < n; j++ {
			if i != j {
				rank += 1 - p[i][j]
			}
		}
		out[i] = rank
	}
	return out
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
	return expectedRanksFrom(winProbs(mu, sigma))
}

// rankDistribution returns P(rank_i = r) for r = 1..n as a slice indexed
// r−1. With independent utilities, rank_i − 1 is the number of j ≠ i that
// beat i: a Poisson-binomial over Bernoulli(1 − p[i][j]). Exact O(n²)
// dynamic programme; probabilities are clamped at 0 against float drift.
func rankDistribution(p [][]float64, i int) []float64 {
	n := len(p)
	if n == 0 {
		return nil
	}
	dist := make([]float64, n)
	dist[0] = 1
	for j := 0; j < n; j++ {
		if j == i {
			continue
		}
		q := 1 - p[i][j] // P(j beats i)
		for r := n - 1; r >= 1; r-- {
			dist[r] = dist[r]*(1-q) + dist[r-1]*q
		}
		dist[0] *= 1 - q
	}
	for r := range dist {
		if dist[r] < 0 {
			dist[r] = 0
		}
	}
	return dist
}

// rankInterval returns the narrowest central credible interval [lo, hi]
// (1-based ranks) that holds at least level of the rank distribution:
// it trims (1−level)/2 of mass from each tail, never crossing the mode's
// side of the mass.
func rankInterval(dist []float64, level float64) (lo, hi int) {
	n := len(dist)
	if n == 0 {
		return 1, 1
	}
	tail := (1 - level) / 2
	lo, hi = 1, n
	acc := 0.0
	for r := 0; r < n; r++ {
		if acc+dist[r] > tail {
			lo = r + 1
			break
		}
		acc += dist[r]
	}
	acc = 0
	for r := n - 1; r >= 0; r-- {
		if acc+dist[r] > tail {
			hi = r + 1
			break
		}
		acc += dist[r]
	}
	if hi < lo {
		hi = lo
	}
	return lo, hi
}

// RankStats is the per-fork rank summary computed for the shortlist pool.
type RankStats struct {
	ExpectedRank float64 // Robbins expected rank, lower is better
	PScore       float64 // (n − ExpectedRank)/(n − 1) = SUCRA; 1 when n == 1
	PTopK        float64 // P(rank ≤ k): probability the fork belongs in a top-k shortlist
	PFirst       float64 // P(rank = 1)
	Lo, Hi       int     // 95% central rank interval, 1-based
}

// rankIntervalLevel is the credible level of RankStats.Lo/Hi.
const rankIntervalLevel = 0.95

// computeRankStats computes RankStats for every item in the pool, with the
// shortlist size k used for PTopK (clamped to [1, n]).
func computeRankStats(mu, sigma []float64, k int) []RankStats {
	n := len(mu)
	if k < 1 {
		k = 1
	}
	if k > n {
		k = n
	}
	p := winProbs(mu, sigma)
	er := expectedRanksFrom(p)
	out := make([]RankStats, n)
	for i := 0; i < n; i++ {
		dist := rankDistribution(p, i)
		top := 0.0
		for r := 0; r < k; r++ {
			top += dist[r]
		}
		if top > 1 {
			top = 1
		}
		ps := 1.0
		if n > 1 {
			ps = (float64(n) - er[i]) / float64(n-1)
			if ps < 0 {
				ps = 0
			} else if ps > 1 {
				ps = 1
			}
		}
		lo, hi := rankInterval(dist, rankIntervalLevel)
		out[i] = RankStats{ExpectedRank: er[i], PScore: ps, PTopK: top, PFirst: dist[0], Lo: lo, Hi: hi}
	}
	return out
}

// Shortlist selection rules. Expected ranks the pool by Robbins expected rank
// (squared-error-optimal for the whole ordering). Membership selects the k
// forks most likely to truly belong in a top-k (P(rank ≤ k), the 0/1-loss
// optimal selector of Lin et al. 2006, Thm 1) and then orders the selected
// forks by expected rank (their Thm 3 hybrid). The two rules differ only
// near the cut, where wide-uncertainty forks trade places.
const (
	ShortlistRuleExpected   = "expected"
	ShortlistRuleMembership = "membership"
)

// selectShortlist returns the indices of the k forks chosen under rule,
// ordered by expected rank ascending (index ascending on exact ties). k
// larger than the pool returns the whole pool ordered.
func selectShortlist(rs []RankStats, rule string, k int) []int {
	if k < 0 {
		k = 0
	}
	n := len(rs)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	byExpected := func(a, b int) bool {
		if rs[a].ExpectedRank != rs[b].ExpectedRank {
			return rs[a].ExpectedRank < rs[b].ExpectedRank
		}
		return a < b
	}
	switch rule {
	case ShortlistRuleMembership:
		sort.SliceStable(idx, func(x, y int) bool {
			a, b := idx[x], idx[y]
			if rs[a].PTopK != rs[b].PTopK {
				return rs[a].PTopK > rs[b].PTopK
			}
			return byExpected(a, b)
		})
	default:
		sort.SliceStable(idx, func(x, y int) bool { return byExpected(idx[x], idx[y]) })
	}
	if k < n {
		idx = idx[:k]
	}
	sort.SliceStable(idx, func(x, y int) bool { return byExpected(idx[x], idx[y]) })
	return idx
}
