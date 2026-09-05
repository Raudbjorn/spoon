package forksops

import (
	"encoding/json"
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

// RankPoolCap is rankPoolCap for callers outside the package (offline
// evaluation scores every ordering key over the same top-RankPoolCap rows).
const RankPoolCap = rankPoolCap

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

// rankDistribution integrates the conditional Poisson-binomial distribution
// over the focal normal utility. Comparisons are independent only conditional
// on that utility; multiplying marginal win probabilities loses their dependence.
func rankDistribution(mu, sigma []float64, i int) []float64 {
	n := len(mu)
	if n == 0 {
		return nil
	}
	if n == 1 {
		return []float64{1}
	}
	// Vanishing uncertainties use the continuous limit, including uniform ranks
	// for identical point masses, consistent with pairwise half-wins on ties.
	sd := func(j int) float64 { return math.Max(sigma[j], 1e-9) }
	si := sd(i)
	conditional := func(z float64) []float64 {
		d := make([]float64, n)
		d[0] = math.Exp(-z*z/2) / math.Sqrt(2*math.Pi)
		count := 0
		for j := range mu {
			if j == i {
				continue
			}
			q := normalCDF(((mu[j] - mu[i]) - si*z) / sd(j))
			count++
			for r := count; r > 0; r-- {
				d[r] = d[r]*(1-q) + d[r-1]*q
			}
			d[0] *= 1 - q
		}
		return d
	}
	// Eight-point Gauss-Legendre quadrature, refined by comparing two halves.
	quad := func(a, b float64) []float64 {
		out := make([]float64, n)
		nodes := [...]float64{0.1834346424956498, 0.5255324099163290, 0.7966664774136267, 0.9602898564975363}
		weights := [...]float64{0.3626837833783620, 0.3137066458778873, 0.2223810344533745, 0.1012285362903763}
		mid, half := (a+b)/2, (b-a)/2
		for k, x := range nodes {
			for _, sign := range []float64{-1, 1} {
				d := conditional(mid + sign*half*x)
				for r := range out {
					out[r] += half * weights[k] * d[r]
				}
			}
		}
		return out
	}
	var integrate func(float64, float64, []float64, float64, int) []float64
	integrate = func(a, b float64, whole []float64, tol float64, depth int) []float64 {
		mid := (a + b) / 2
		left, right := quad(a, mid), quad(mid, b)
		err := 0.0
		for r := range whole {
			err += math.Abs(left[r] + right[r] - whole[r])
		}
		if err > tol && depth > 0 {
			left = integrate(a, mid, left, tol/2, depth-1)
			right = integrate(mid, b, right, tol/2, depth-1)
		}
		for r := range left {
			left[r] += right[r]
		}
		return left
	}
	// Split at competitors' transitions so even narrow uncertainties are seen.
	// Outside eight focal standard deviations the omitted mass is < 1.3e-15.
	knots := []float64{-8, 0, 8}
	for j := range mu {
		if j == i {
			continue
		}
		for _, offset := range []float64{-8, 0, 8} {
			z := ((mu[j] - mu[i]) + offset*sd(j)) / si
			if z > -8 && z < 8 {
				knots = append(knots, z)
			}
		}
	}
	sort.Float64s(knots)
	out := make([]float64, n)
	for k := 1; k < len(knots); k++ {
		a, b := knots[k-1], knots[k]
		if a == b {
			continue
		}
		part := integrate(a, b, quad(a, b), 1e-10*(b-a)/16, 12)
		for r := range out {
			out[r] += part[r]
		}
	}
	sum := 0.0
	for _, v := range out {
		sum += v
	}
	for r := range out {
		out[r] /= sum
	}
	return out
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
	// TieBand marks a fork indistinguishable from an adjacent fork in the
	// emitted ordering (|Δμ| < tieBandFactor·√(σ_i²+σ_j²)); report such runs
	// as a band, not an order.
	TieBand bool
	// PothResidual is POTH − POTH(pool without this fork): positive when the
	// fork sharpens the hierarchy, negative when it blurs it. Set only under
	// Options.RankDiagnostics (it costs one extra O(n²) pass per fork).
	PothResidual *float64
}

// RankReport is the pool-level summary of a shortlist run, filled by Stream
// into Options.RankReport after the channel closes. POTH and CPOTHk are NaN
// when the respective set is smaller than pothMinPool. The JSON form is the
// "details" payload of the rank_report info envelope.
type RankReport struct {
	PoolSize      int     `json:"poolSize"`      // forks ranked (≤ rankPoolCap)
	NonzeroPool   int     `json:"nonzeroPool"`   // ranked forks with heat > 0
	ShortlistN    int     `json:"shortlistN"`    // requested shortlist size
	ShortlistRule string  `json:"shortlistRule"` // expected | membership
	POTH          float64 `json:"poth"`          // hierarchy precision over the pool, [0,1]
	CPOTHk        float64 `json:"cpothK"`        // POTH recomputed within the shortlist
	// Empirical-Bayes fields; zero/empty unless Options.EB.
	EBRegime   string  `json:"ebRegime,omitempty"`   // insufficient | pooled | heterogeneous | clamped
	EBPool     int     `json:"ebPool,omitempty"`     // forks entering the fit (heat > 0)
	TauHat     float64 `json:"tauHat,omitempty"`     // between-fork sd estimate
	EBMean     float64 `json:"ebMean,omitempty"`     // pooled mean m
	PriorScale float64 `json:"priorScale,omitempty"` // half-normal scale on τ actually used
	DBarOverK  float64 `json:"dBarOverK,omitempty"`  // posterior-mean deviance per fork, ≈ 1 when the model fits
	PD         float64 `json:"pD,omitempty"`         // effective parameters = Σ leverage
}

// MarshalJSON keeps undefined precision statistics representable in every export.
func (r RankReport) MarshalJSON() ([]byte, error) {
	type plain RankReport
	finite := func(v float64) *float64 {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
		return &v
	}
	return json.Marshal(struct {
		plain
		POTH   *float64 `json:"poth"`
		CPOTHk *float64 `json:"cpothK"`
	}{plain(r), finite(r.POTH), finite(r.CPOTHk)})
}

// EBStats is the per-fork empirical-Bayes summary (Result.EB), set only for
// forks with heat > 0 when Options.EB is on and the fit applied.
type EBStats struct {
	Theta     float64 // shrunken score, replaces heat as mu in the ranking
	PostSigma float64 // posterior sd, replaces the tier sigma
	Residual  float64 // standardised residual (y − θ̂)/σ at the fit
	Leverage  float64 // B = τ²/(τ²+σ²); Σ = pD
	// Flag marks residual² + leverage > ebFlagContour: a fork the model does
	// not explain (TSD2 leverage-plot rule, contour c = 3).
	Flag bool
}

// ebFlagContour is the TSD2 leverage-plot contour outside which a point is
// flagged as poorly fit / over-influential.
const ebFlagContour = 3.0

// rankIntervalLevel is the credible level of RankStats.Lo/Hi.
const rankIntervalLevel = 0.95

// computeRankStats computes RankStats for every item in the pool, with the
// shortlist size k used for PTopK (clamped to [1, n]).
func computeRankStats(mu, sigma []float64, k int) []RankStats {
	return computeRankStatsFrom(mu, sigma, winProbs(mu, sigma), k)
}

// computeRankStatsFrom is computeRankStats over a precomputed win matrix.
func computeRankStatsFrom(mu, sigma []float64, p [][]float64, k int) []RankStats {
	n := len(p)
	if k < 1 {
		k = 1
	}
	if k > n {
		k = n
	}
	er := expectedRanksFrom(p)
	out := make([]RankStats, n)
	for i := 0; i < n; i++ {
		dist := rankDistribution(mu, sigma, i)
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

// pothMinPool is the smallest pool for which POTH is reported; below it the
// normalisation is degenerate (n = 2 reduces to 4(p − ½)²).
const pothMinPool = 3

// subsetPoth is the precision of treatment hierarchy (Wigle et al. 2025)
// over the members idx of the pool described by win matrix p, with P-scores
// recomputed within the subset: P̄_i = mean_{j∈T, j≠i} p[i][j] and
// POTH_T = 12(m−1)/(m+1) · (1/m) Σ_{i∈T} (P̄_i − ½)², which lies in [0, 1]
// (0 = every pair a coin flip, 1 = a certain total order). NaN when the
// subset has fewer than pothMinPool members, or when idx references a row
// outside p's bounds (defensive: p and idx are internal to this package but
// helper functions like this are reused from multiple entry points).
func subsetPoth(p [][]float64, idx []int) float64 {
	m := len(idx)
	if m < pothMinPool {
		return math.NaN()
	}
	n := len(p)
	for _, i := range idx {
		if i < 0 || i >= n || len(p[i]) < n {
			return math.NaN()
		}
	}
	s2 := 0.0
	for _, i := range idx {
		sum := 0.0
		for _, j := range idx {
			if i != j {
				sum += p[i][j]
			}
		}
		pbar := sum / float64(m-1)
		s2 += (pbar - 0.5) * (pbar - 0.5)
	}
	s2 /= float64(m)
	return 12 * float64(m-1) / float64(m+1) * s2
}

// poth is subsetPoth over the whole pool.
func poth(p [][]float64) float64 {
	idx := make([]int, len(p))
	for i := range idx {
		idx[i] = i
	}
	return subsetPoth(p, idx)
}

// pothResiduals returns, for each fork j, POTH − POTH_{pool without j}:
// positive when j stands apart and sharpens the hierarchy, negative when j
// blurs it (wide uncertainty, close neighbours). NaN entries when the pool
// is too small for POTH before or after removal.
func pothResiduals(p [][]float64) []float64 {
	n := len(p)
	whole := poth(p)
	out := make([]float64, n)
	rest := make([]int, 0, n-1)
	for j := 0; j < n; j++ {
		rest = rest[:0]
		for i := 0; i < n; i++ {
			if i != j {
				rest = append(rest, i)
			}
		}
		out[j] = whole - subsetPoth(p, rest)
	}
	return out
}

// tieBandFactor is c in |μ_i − μ_j| < c·√(σ_i²+σ_j²): neighbours closer
// than that have P(i beats j) within ≈ 0.5 ± 0.15 (Φ(0.4) ≈ 0.66) and are
// reported as a band rather than ordered (Pearce & Erosheva 2025 pattern:
// say "indistinguishable" instead of inventing an order).
const tieBandFactor = 0.4

// tieBands reports, for each position in order (indices into mu/sigma),
// whether the fork is indistinguishable from at least one adjacent fork in
// the ordering under the band rule. Both members of a close pair are marked.
// Indices in order that fall outside mu/sigma's bounds are skipped rather
// than indexed, matching subsetPoth's defensive posture for the same reason:
// this helper is reused from more than one entry point.
func tieBands(mu, sigma []float64, order []int, c float64) []bool {
	out := make([]bool, len(order))
	n := len(mu)
	if len(sigma) < n {
		n = len(sigma)
	}
	for k := 1; k < len(order); k++ {
		i, j := order[k-1], order[k]
		if i < 0 || i >= n || j < 0 || j >= n {
			continue
		}
		if math.Abs(mu[i]-mu[j]) < c*math.Hypot(sigma[i], sigma[j]) {
			out[k-1] = true
			out[k] = true
		}
	}
	return out
}
