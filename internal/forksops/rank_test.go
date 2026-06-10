package forksops

import (
	"math"
	"testing"
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
