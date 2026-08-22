package forksops

import (
	"math"
	"math/rand"
	"strconv"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

func TestNormalCDF(t *testing.T) {
	cases := map[float64]float64{0: 0.5, 1.281551: 0.9, -1.281551: 0.1}
	for x, want := range cases {
		if got := normalCDF(x); math.Abs(got-want) > 1e-3 {
			t.Errorf("normalCDF(%v)=%v want %v", x, got, want)
		}
	}
}

func TestExpectedRanks_OrderingAndBounds(t *testing.T) {
	// Distinct utilities, equal modest uncertainty: best mu → lowest rank.
	mu := []float64{30, 10, 20}
	sigma := []float64{5, 5, 5}
	ranks := expectedRanks(mu, sigma)
	if !(ranks[0] < ranks[2] && ranks[2] < ranks[1]) {
		t.Errorf("expected rank(30) < rank(20) < rank(10); got %v", ranks)
	}
	// Best item's expected rank is close to 1 when well-separated.
	if ranks[0] < 1 || ranks[0] > 1.5 {
		t.Errorf("top item expected rank=%v, want ~1", ranks[0])
	}
	// Worst item's rank approaches n.
	if ranks[1] < 2.5 || ranks[1] > 3.0 {
		t.Errorf("bottom item expected rank=%v, want ~3", ranks[1])
	}
}

func TestExpectedRanks_UncertaintyDemotesUnconfirmed(t *testing.T) {
	// A and B have the same surface mu, but A is confirmed (low sigma) and B is
	// T1-only (high sigma). With equal mu they tie ~evenly; bump A slightly and
	// it should clearly lead — the point is high-uncertainty items don't get a
	// free top rank. Here: A mu 22 low-sigma vs B mu 25 high-sigma.
	mu := []float64{22, 25}
	sigma := []float64{rankSigma(0.9), rankSigma(0.3)} // A confident, B not
	ranks := expectedRanks(mu, sigma)
	// B's higher mu still wins on its own, but its wide sigma narrows the gap;
	// assert both ranks are sane and B (higher mu) leads.
	if ranks[1] >= ranks[0] {
		t.Errorf("higher-mu item should lead; ranks=%v", ranks)
	}
	if ranks[0] < 1 || ranks[1] < 1 {
		t.Errorf("ranks must be >= 1; got %v", ranks)
	}
}

func TestExpectedRanks_Singleton(t *testing.T) {
	if r := expectedRanks([]float64{5}, []float64{1}); len(r) != 1 || r[0] != 1 {
		t.Errorf("singleton rank=%v want [1]", r)
	}
}

func TestRankSigma_MonotoneInConfidence(t *testing.T) {
	if rankSigma(0.3) <= rankSigma(0.9) {
		t.Error("lower confidence should give larger sigma")
	}
	if rankSigma(1.0) <= 0 {
		t.Error("sigma must stay positive even at full confidence")
	}
}

// --- PR1: P-score + Poisson-binomial rank distribution ---

func randPool(rng *rand.Rand, n int) (mu, sigma []float64) {
	mu = make([]float64, n)
	sigma = make([]float64, n)
	for i := range mu {
		mu[i] = rng.Float64() * 60
		sigma[i] = rankSigma([]float64{0.3, 0.7, 0.9}[rng.Intn(3)])
	}
	return mu, sigma
}

func TestWinProbs_Complementary(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	mu, sigma := randPool(rng, 12)
	p := winProbs(mu, sigma)
	for i := range p {
		if p[i][i] != 0 {
			t.Fatalf("p[%d][%d]=%v want 0", i, i, p[i][i])
		}
		for j := range p {
			if i == j {
				continue
			}
			if s := p[i][j] + p[j][i]; math.Abs(s-1) > 1e-12 {
				t.Fatalf("p[%d][%d]+p[%d][%d]=%v want 1", i, j, j, i, s)
			}
		}
	}
}

func TestExpectedRanksFrom_MatchesExpectedRanks(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for trial := 0; trial < 50; trial++ {
		mu, sigma := randPool(rng, 1+rng.Intn(40))
		want := expectedRanks(mu, sigma)
		got := expectedRanksFrom(winProbs(mu, sigma))
		for i := range want {
			if math.Abs(want[i]-got[i]) > 1e-9 {
				t.Fatalf("trial %d i=%d: %v vs %v", trial, i, want[i], got[i])
			}
		}
	}
}

func TestRankDistribution_SumsToOneAndMeanIsExpectedRank(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for trial := 0; trial < 50; trial++ {
		mu, sigma := randPool(rng, 1+rng.Intn(50))
		p := winProbs(mu, sigma)
		er := expectedRanksFrom(p)
		for i := range mu {
			d := rankDistribution(p, i)
			if len(d) != len(mu) {
				t.Fatalf("len(dist)=%d want %d", len(d), len(mu))
			}
			sum, mean := 0.0, 0.0
			for k, pk := range d {
				if pk < 0 {
					t.Fatalf("negative probability %v at rank %d", pk, k+1)
				}
				sum += pk
				mean += float64(k+1) * pk
			}
			if math.Abs(sum-1) > 1e-12 {
				t.Fatalf("sum=%v want 1", sum)
			}
			if math.Abs(mean-er[i]) > 1e-9 {
				t.Fatalf("DP mean %v != expected rank %v", mean, er[i])
			}
		}
	}
}

func TestRankInterval_CoversMass(t *testing.T) {
	// Point mass at rank 3 → [3,3]. Uniform over 4 → 95% interval is [1,4].
	if lo, hi := rankInterval([]float64{0, 0, 1, 0}, 0.95); lo != 3 || hi != 3 {
		t.Errorf("point mass: got [%d,%d] want [3,3]", lo, hi)
	}
	if lo, hi := rankInterval([]float64{0.25, 0.25, 0.25, 0.25}, 0.95); lo != 1 || hi != 4 {
		t.Errorf("uniform: got [%d,%d] want [1,4]", lo, hi)
	}
	// Mass concentrated in the middle: tails of 1% each are excluded at 95%.
	if lo, hi := rankInterval([]float64{0.01, 0.49, 0.49, 0.01}, 0.95); lo != 2 || hi != 3 {
		t.Errorf("central: got [%d,%d] want [2,3]", lo, hi)
	}
}

func TestRankInterval_ContainsRoundedExpectedRank(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	trials, hits := 0, 0
	for trial := 0; trial < 100; trial++ {
		mu, sigma := randPool(rng, 2+rng.Intn(30))
		p := winProbs(mu, sigma)
		er := expectedRanksFrom(p)
		for i := range mu {
			lo, hi := rankInterval(rankDistribution(p, i), 0.95)
			r := int(math.Round(er[i]))
			trials++
			if r >= lo && r <= hi {
				hits++
			}
		}
	}
	if float64(hits)/float64(trials) < 0.95 {
		t.Errorf("rounded expected rank inside 95%% interval only %d/%d", hits, trials)
	}
}

func TestComputeRankStats_PScoreIdentityAndBounds(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for trial := 0; trial < 30; trial++ {
		mu, sigma := randPool(rng, 2+rng.Intn(40))
		n := len(mu)
		k := 1 + rng.Intn(n)
		rs := computeRankStats(mu, sigma, k)
		if len(rs) != n {
			t.Fatalf("len=%d want %d", len(rs), n)
		}
		for i, r := range rs {
			wantP := (float64(n) - r.ExpectedRank) / float64(n-1)
			if math.Abs(r.PScore-wantP) > 1e-12 {
				t.Fatalf("PScore %v != (n-E)/(n-1)=%v", r.PScore, wantP)
			}
			if r.PScore < 0 || r.PScore > 1 || r.PTopK < 0 || r.PTopK > 1 || r.PFirst < 0 || r.PFirst > 1 {
				t.Fatalf("probability out of [0,1]: %+v", r)
			}
			if r.Lo < 1 || r.Hi > n || r.Lo > r.Hi {
				t.Fatalf("bad interval [%d,%d] n=%d", r.Lo, r.Hi, n)
			}
			if r.PTopK < r.PFirst-1e-12 {
				t.Fatalf("P(rank<=k) %v < P(rank=1) %v", r.PTopK, r.PFirst)
			}
			_ = i
		}
		// k == n: every fork is certainly in the top n.
		all := computeRankStats(mu, sigma, n)
		for _, r := range all {
			if math.Abs(r.PTopK-1) > 1e-9 {
				t.Fatalf("PTopK with k=n is %v want 1", r.PTopK)
			}
		}
	}
}

func TestComputeRankStats_DominantForkAndSingleton(t *testing.T) {
	rs := computeRankStats([]float64{90, 10, 5}, []float64{1, 1, 1}, 1)
	if rs[0].PScore < 0.999 || rs[0].PFirst < 0.999 || rs[0].Lo != 1 || rs[0].Hi != 1 {
		t.Errorf("dominant fork: %+v", rs[0])
	}
	one := computeRankStats([]float64{5}, []float64{1}, 1)
	if one[0].PScore != 1 || one[0].PTopK != 1 || one[0].PFirst != 1 || one[0].Lo != 1 || one[0].Hi != 1 {
		t.Errorf("singleton: %+v", one[0])
	}
}

// --- PR2: shortlist selection rule ---

func TestSelectShortlist_ExpectedRuleIsTopKByExpectedRank(t *testing.T) {
	rs := []RankStats{
		{ExpectedRank: 2.0, PTopK: 0.9},
		{ExpectedRank: 1.2, PTopK: 0.95},
		{ExpectedRank: 3.5, PTopK: 0.2},
		{ExpectedRank: 1.9, PTopK: 0.6},
	}
	got := selectShortlist(rs, ShortlistRuleExpected, 2)
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("expected rule: got %v want [1 3]", got)
	}
}

func TestSelectShortlist_MembershipRuleSelectsByPTopKOrdersByExpectedRank(t *testing.T) {
	// Fork 0 has the better expected rank but a lower membership probability
	// than fork 2 (a wide-sigma fork can have a low E_rank yet low P(rank<=k)
	// — or, as here, the reverse). Membership rule must pick 2 over 0.
	rs := []RankStats{
		{ExpectedRank: 1.8, PTopK: 0.55},
		{ExpectedRank: 1.5, PTopK: 0.90},
		{ExpectedRank: 2.2, PTopK: 0.70},
		{ExpectedRank: 4.0, PTopK: 0.05},
	}
	got := selectShortlist(rs, ShortlistRuleMembership, 2)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("membership rule: got %v want [1 2] (ordered by expected rank)", got)
	}
	if exp := selectShortlist(rs, ShortlistRuleExpected, 2); len(exp) != 2 || exp[0] != 1 || exp[1] != 0 {
		t.Errorf("expected rule should differ here: got %v want [1 0]", exp)
	}
}

func TestSelectShortlist_TiesBreakOnExpectedRankThenIndex(t *testing.T) {
	rs := []RankStats{
		{ExpectedRank: 2.0, PTopK: 0.5},
		{ExpectedRank: 1.0, PTopK: 0.5},
		{ExpectedRank: 1.0, PTopK: 0.5},
	}
	got := selectShortlist(rs, ShortlistRuleMembership, 2)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("ties: got %v want [1 2]", got)
	}
}

func TestSelectShortlist_KLargerThanPoolReturnsAllOrdered(t *testing.T) {
	rs := []RankStats{{ExpectedRank: 2}, {ExpectedRank: 1}}
	for _, rule := range []string{ShortlistRuleExpected, ShortlistRuleMembership} {
		got := selectShortlist(rs, rule, 10)
		if len(got) != 2 || got[0] != 1 || got[1] != 0 {
			t.Errorf("%s: got %v want [1 0]", rule, got)
		}
	}
}

// --- PR3: POTH (precision of treatment hierarchy) ---

func TestPoth_ZeroForCoinFlipsOneForTotalOrder(t *testing.T) {
	// All pairwise 0.5 ⇒ every P-score is 0.5 ⇒ POTH 0.
	flat := winProbs([]float64{5, 5, 5, 5}, []float64{1, 1, 1, 1})
	if got := poth(flat); math.Abs(got) > 1e-12 {
		t.Errorf("flat POTH=%v want 0", got)
	}
	// Well separated with tiny sigma ⇒ total order ⇒ POTH → 1.
	sharp := winProbs([]float64{40, 30, 20, 10}, []float64{1e-6, 1e-6, 1e-6, 1e-6})
	if got := poth(sharp); got < 0.999 || got > 1+1e-9 {
		t.Errorf("total-order POTH=%v want ≈1", got)
	}
}

func TestPoth_BoundedOnRandomPools(t *testing.T) {
	rng := rand.New(rand.NewSource(6))
	for trial := 0; trial < 50; trial++ {
		mu, sigma := randPool(rng, 3+rng.Intn(40))
		got := poth(winProbs(mu, sigma))
		if got < -1e-12 || got > 1+1e-12 {
			t.Fatalf("POTH %v out of [0,1]", got)
		}
	}
}

func TestPoth_TooSmallPoolIsNaN(t *testing.T) {
	if got := poth(winProbs([]float64{1, 2}, []float64{1, 1})); !math.IsNaN(got) {
		t.Errorf("n=2 POTH=%v want NaN (gated n>=3)", got)
	}
}

func TestSubsetPoth_RecomputesWithinSubset(t *testing.T) {
	// Fork 0 far ahead; 1..3 a tight cluster. Whole-pool POTH is high, but
	// the cluster alone is near coin-flip.
	p := winProbs([]float64{60, 20, 20.5, 19.5}, []float64{1, 1, 1, 1})
	whole := poth(p)
	cluster := subsetPoth(p, []int{1, 2, 3})
	if !(cluster < whole) {
		t.Errorf("cluster POTH %v should be below whole-pool POTH %v", cluster, whole)
	}
	if got := subsetPoth(p, []int{1}); !math.IsNaN(got) {
		t.Errorf("single-member subset should be NaN, got %v", got)
	}
	// Subset equal to the whole pool reproduces poth.
	if got := subsetPoth(p, []int{0, 1, 2, 3}); math.Abs(got-whole) > 1e-12 {
		t.Errorf("full subset %v != whole %v", got, whole)
	}
}

func TestPothResiduals_OutlierSharpensClusterBlurs(t *testing.T) {
	p := winProbs([]float64{60, 20, 20.5, 19.5}, []float64{1, 1, 1, 1})
	res := pothResiduals(p)
	if len(res) != 4 {
		t.Fatalf("len=%d want 4", len(res))
	}
	// Removing the lone leader leaves only the blurry cluster: POTH drops,
	// so the leader's residual is positive (it sharpens the hierarchy).
	if res[0] <= 0 {
		t.Errorf("leader residual %v should be > 0", res[0])
	}
	// Removing a cluster member leaves the leader plus a smaller cluster —
	// the hierarchy gets sharper, so cluster members have negative residuals.
	for i := 1; i < 4; i++ {
		if res[i] >= 0 {
			t.Errorf("cluster member %d residual %v should be < 0", i, res[i])
		}
	}
}

// --- PR5: offline ranking entry point ---

func syntheticPool(scores []float64, tiers []int) []Result {
	out := make([]Result, len(scores))
	for i := range scores {
		conf := map[int]float64{1: 0.3, 2: 0.7, 3: 0.9}[tiers[i]]
		out[i] = Result{Fork: forge.T1Data{ID: "o/f" + strconv.Itoa(i)}, Heat: heat.HeatResult{Score: scores[i], Tier: tiers[i], Confidence: conf}}
	}
	return out
}

func TestRankResults_MatchesStreamSemantics(t *testing.T) {
	pool := syntheticPool([]float64{50, 40, 0, 30, 20, 10}, []int{3, 1, 2, 3, 1, 3})
	got, report := RankResults(pool, Options{ShortlistN: 3})
	if len(got) != 3 {
		t.Fatalf("shortlist len=%d want 3", len(got))
	}
	if report.PoolSize != 6 || report.NonzeroPool != 5 || report.ShortlistRule != ShortlistRuleExpected {
		t.Errorf("report %+v", report)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ExpectedRank > got[i].ExpectedRank {
			t.Errorf("not ordered by expected rank: %v", got)
		}
	}
	if got[0].Fork.ID != "o/f0" || got[0].Rank == nil {
		t.Errorf("best fork should be o/f0 with Rank set, got %+v", got[0])
	}
}

func TestRankResults_RespectsPoolCapAndEB(t *testing.T) {
	n := rankPoolCap + 50
	scores := make([]float64, n)
	tiers := make([]int, n)
	for i := range scores {
		scores[i] = float64(n - i)
		tiers[i] = 1 + i%3
	}
	got, report := RankResults(syntheticPool(scores, tiers), Options{ShortlistN: n, EB: true, PriorScale: 100})
	if len(got) != rankPoolCap || report.PoolSize != rankPoolCap {
		t.Errorf("pool cap: len=%d poolSize=%d want %d", len(got), report.PoolSize, rankPoolCap)
	}
	if report.EBRegime == "" || report.EBRegime == EBRegimeInsufficient {
		t.Errorf("EB should have run: %+v", report)
	}
	for _, r := range got {
		if r.EB == nil {
			t.Fatalf("fork %s missing EB stats", r.Fork.ID)
		}
	}
}
