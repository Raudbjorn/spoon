package forksops

import (
	"math"
	"math/rand"
	"testing"
)

func TestMadScale(t *testing.T) {
	if got := madScale([]float64{1, 2, 3, 4, 100}); math.Abs(got-1.4826) > 1e-9 {
		t.Errorf("madScale=%v want 1.4826 (MAD 1 × 1.4826)", got)
	}
	if got := madScale(nil); got != 0 {
		t.Errorf("madScale(nil)=%v want 0", got)
	}
	if got := madScale([]float64{5, 5, 5}); got != 0 {
		t.Errorf("madScale(constant)=%v want 0", got)
	}
}

func TestFitNormalNormal_InsufficientPool(t *testing.T) {
	fit, ok := fitNormalNormal([]float64{10, 20}, []float64{1, 1}, 0)
	if ok || fit.Regime != EBRegimeInsufficient {
		t.Errorf("pool of 2: ok=%v regime=%q want !ok insufficient", ok, fit.Regime)
	}
}

func TestFitNormalNormal_PooledWhenNoHeterogeneity(t *testing.T) {
	// Spread well inside measurement noise → DL truncates τ̂² to 0.
	y := []float64{10, 10.5, 9.5, 10.2, 9.8}
	s := []float64{7, 7, 7, 7, 7}
	fit, ok := fitNormalNormal(y, s, 0)
	if ok || fit.Regime != EBRegimePooled || fit.Tau2 != 0 {
		t.Errorf("ok=%v regime=%q tau2=%v want !ok pooled 0", ok, fit.Regime, fit.Tau2)
	}
}

func TestFitNormalNormal_ShrinkageMonotoneInSigma(t *testing.T) {
	// Two forks with the same score but different measurement sigma: the
	// noisier one is pulled closer to the pooled mean, and gets a smaller B.
	y := []float64{40, 40, 5, 6, 4, 5, 6}
	s := []float64{7, 1, 1, 1, 1, 1, 1}
	// Explicit wide prior scale so the MAD cap (tight bulk of 5s) does not
	// bind; the clamp has its own test.
	fit, ok := fitNormalNormal(y, s, 50)
	if !ok || fit.Regime != EBRegimeHeterogeneous {
		t.Fatalf("ok=%v regime=%q fit=%+v", ok, fit.Regime, fit)
	}
	if !(math.Abs(fit.Theta[0]-fit.M) < math.Abs(fit.Theta[1]-fit.M)) {
		t.Errorf("noisy fork θ=%v should sit closer to m=%v than precise fork θ=%v", fit.Theta[0], fit.M, fit.Theta[1])
	}
	if !(fit.B[0] < fit.B[1]) {
		t.Errorf("B noisy=%v should be < B precise=%v", fit.B[0], fit.B[1])
	}
	for i := range y {
		if !(fit.PostSigma[i] < s[i]) {
			t.Errorf("posterior sigma %v should shrink below measurement sigma %v", fit.PostSigma[i], s[i])
		}
		if want := fit.Tau2 / (fit.Tau2 + s[i]*s[i]); math.Abs(fit.B[i]-want) > 1e-12 {
			t.Errorf("B[%d]=%v want τ²/(τ²+σ²)=%v", i, fit.B[i], want)
		}
	}
}

func TestFitNormalNormal_RecoversTauOnSimulatedPool(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const n, tau, mean = 400, 8.0, 20.0
	tiers := []float64{7, 3, 1}
	y := make([]float64, n)
	s := make([]float64, n)
	for i := range y {
		theta := mean + tau*rng.NormFloat64()
		s[i] = tiers[rng.Intn(3)]
		y[i] = theta + s[i]*rng.NormFloat64()
	}
	fit, ok := fitNormalNormal(y, s, 0)
	if !ok {
		t.Fatalf("fit failed: %+v", fit)
	}
	if got := math.Sqrt(fit.Tau2); math.Abs(got-tau)/tau > 0.2 {
		t.Errorf("τ̂=%v want within 20%% of %v", got, tau)
	}
	if math.Abs(fit.M-mean) > 2 {
		t.Errorf("m=%v want ≈ %v", fit.M, mean)
	}
	if fit.DBarOverK < 0.8 || fit.DBarOverK > 1.2 {
		t.Errorf("D̄/k=%v want ≈ 1 on well-specified data", fit.DBarOverK)
	}
	if math.Abs(fit.PD-sumFloat(fit.B)) > 1e-9 {
		t.Errorf("pD %v should equal ΣB %v", fit.PD, sumFloat(fit.B))
	}
}

func TestFitNormalNormal_PriorScaleClampsTau(t *testing.T) {
	y := []float64{60, 5, 50, 8, 40, 2}
	s := []float64{1, 1, 1, 1, 1, 1}
	free, _ := fitNormalNormal(y, s, 0)
	clamped, ok := fitNormalNormal(y, s, 1) // HN(1): τ̂ capped at 2·scale
	if !ok || math.Sqrt(clamped.Tau2) > 2+1e-12 {
		t.Errorf("τ̂=%v should be clamped to ≤ 2 with prior scale 1 (free τ̂=%v)", math.Sqrt(clamped.Tau2), math.Sqrt(free.Tau2))
	}
	if clamped.Regime != EBRegimeClamped {
		t.Errorf("regime=%q want clamped", clamped.Regime)
	}
}

func TestFitNormalNormal_ScaleNeverExceedsQuarterRange(t *testing.T) {
	// Heat lives on 0–100; τ̂ above 25 would spill the normal model outside
	// the bounded scale (Röver 2021 §3.4), so it is capped there regardless
	// of prior scale.
	y := []float64{0, 100, 0, 100, 0, 100, 0, 100}
	s := []float64{1, 1, 1, 1, 1, 1, 1, 1}
	fit, _ := fitNormalNormal(y, s, 1000)
	if math.Sqrt(fit.Tau2) > ebTauMax+1e-12 {
		t.Errorf("τ̂=%v exceeds cap %v", math.Sqrt(fit.Tau2), ebTauMax)
	}
}

func sumFloat(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s
}
