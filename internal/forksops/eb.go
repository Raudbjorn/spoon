package forksops

import (
	"math"
	"sort"
)

// Empirical-Bayes normal–normal layer for the shortlist pool.
//
// Each fork's heat y_i is treated as a noisy measurement of a latent utility
// θ_i with known measurement sd σ_i (the tier sigma), and the θ_i as draws
// from a common N(m, τ²). This is the NICE TSD2 random-effects model with one
// arm per fork (Röver et al. 2021 for the prior discussion). The between-fork
// heterogeneity τ² is estimated by DerSimonian–Laird, then every fork is
// shrunk toward m in proportion to how noisy it is:
//
//	B_i = τ² / (τ² + σ_i²)
//	θ̂_i = m + B_i (y_i − m)
//	Var_i = 1 / (σ_i⁻² + τ⁻²)
//
// Shrinking the mean is half the correction; the posterior sd is the other
// half (Laird & Louis 1989). Constant tier sigmas alone leave every rank
// estimator equal to a plain sort by heat within a tier, and the Φ(·)
// probabilities uncalibrated — τ̂ ties the score scale to the observed
// spread.

// EB regimes reported in RankReport. Only EBRegimeHeterogeneous and
// EBRegimeClamped apply shrinkage; the others leave mu/sigma untouched.
const (
	EBRegimeInsufficient  = "insufficient"  // pool below ebMinPool
	EBRegimePooled        = "pooled"        // τ̂² truncated to 0: no heterogeneity beyond measurement noise
	EBRegimeHeterogeneous = "heterogeneous" // τ̂² > 0, shrinkage applied
	EBRegimeClamped       = "clamped"       // τ̂ hit the prior-scale or bounded-scale cap
)

// ebMinPool is the smallest pool on which τ² is estimated (DL needs k ≥ 3
// for a usable Q; Röver 2021 §3.1).
const ebMinPool = 3

// ebTauMax caps τ̂ at a quarter of the heat range (0–100): beyond that the
// normal model spills outside the bounded scale (Röver 2021 §3.4.1).
const ebTauMax = 25.0

// ebMADConsistency makes MAD a consistent sd estimator under normality.
const ebMADConsistency = 1.4826

// ebPriorQuantile turns a half-normal scale s into the τ̂ cap 2s, the
// ≈95% quantile of HN(s) — "implausible heterogeneity gets ≈5% prior mass".
const ebPriorQuantile = 2.0

// ebFit is the fitted normal–normal model over one pool.
type ebFit struct {
	M      float64 // pooled mean (precision-weighted under τ̂²)
	Tau2   float64 // between-fork variance estimate
	Regime string
	// Per fork, aligned with the input slices.
	Theta     []float64 // shrunken score
	PostSigma []float64 // posterior sd
	B         []float64 // shrinkage factor = leverage
	Residual  []float64 // standardised residual (y − θ̂)/σ at the shrunken fit
	DRes      float64   // Σ residual²: residual deviance at the fitted values, D(θ̂)
	PD        float64   // Σ B_i: effective number of parameters (leverage)
	DBarOverK float64   // (DRes + PD)/k = posterior-mean deviance per fork, ≈ 1 when the model fits
}

// madScale returns 1.4826·MAD(y), a robust sd estimate; 0 for fewer than
// two values or a constant input.
func madScale(y []float64) float64 {
	n := len(y)
	if n < 2 {
		return 0
	}
	med := median(append([]float64(nil), y...))
	dev := make([]float64, n)
	for i, v := range y {
		dev[i] = math.Abs(v - med)
	}
	return ebMADConsistency * median(dev)
}

// median sorts v in place and returns its median.
func median(v []float64) float64 {
	sort.Float64s(v)
	n := len(v)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

// fitNormalNormal fits the model to scores y with measurement sds sigma.
// priorScale is the half-normal scale on τ; 0 selects madScale(y). The
// returned bool reports whether shrinkage applies (regime heterogeneous or
// clamped); on false the fit carries only M, Tau2 and Regime.
func fitNormalNormal(y, sigma []float64, priorScale float64) (ebFit, bool) {
	k := len(y)
	if k < ebMinPool {
		return ebFit{Regime: EBRegimeInsufficient}, false
	}
	// DerSimonian–Laird: fixed-effect weights w = σ⁻².
	sumW, sumW2, sumWY := 0.0, 0.0, 0.0
	for i := range y {
		w := 1 / (sigma[i] * sigma[i])
		sumW += w
		sumW2 += w * w
		sumWY += w * y[i]
	}
	yw := sumWY / sumW
	q := 0.0
	for i := range y {
		d := y[i] - yw
		q += d * d / (sigma[i] * sigma[i])
	}
	tau2 := (q - float64(k-1)) / (sumW - sumW2/sumW)
	if tau2 <= 0 {
		return ebFit{M: yw, Regime: EBRegimePooled}, false
	}
	regime := EBRegimeHeterogeneous
	if priorScale <= 0 {
		priorScale = madScale(y)
	}
	capTau := ebTauMax
	if priorScale > 0 && ebPriorQuantile*priorScale < capTau {
		capTau = ebPriorQuantile * priorScale
	}
	if tau := math.Sqrt(tau2); tau > capTau {
		tau2 = capTau * capTau
		regime = EBRegimeClamped
	}
	// Random-effects pooled mean with weights 1/(σ² + τ²).
	sumV, sumVY := 0.0, 0.0
	for i := range y {
		v := 1 / (sigma[i]*sigma[i] + tau2)
		sumV += v
		sumVY += v * y[i]
	}
	m := sumVY / sumV
	fit := ebFit{
		M: m, Tau2: tau2, Regime: regime,
		Theta:     make([]float64, k),
		PostSigma: make([]float64, k),
		B:         make([]float64, k),
		Residual:  make([]float64, k),
	}
	for i := range y {
		s2 := sigma[i] * sigma[i]
		b := tau2 / (tau2 + s2)
		fit.B[i] = b
		fit.Theta[i] = m + b*(y[i]-m)
		fit.PostSigma[i] = math.Sqrt(1 / (1/s2 + 1/tau2))
		r := (y[i] - fit.Theta[i]) / sigma[i]
		fit.Residual[i] = r
		fit.DRes += r * r
		fit.PD += b
	}
	// At the shrunken fit E[r_i²] = 1 − B_i, so the plug-in deviance alone
	// under-reads; adding the leverage recovers the posterior-mean deviance
	// D̄ = D(θ̂) + pD (TSD2 §6), whose per-fork value should be ≈ 1.
	fit.DBarOverK = (fit.DRes + fit.PD) / float64(k)
	return fit, true
}
