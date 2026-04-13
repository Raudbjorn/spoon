package heat

import "math"

// LogNorm normalizes an unbounded positive value using logarithmic scaling.
// Returns 0.0 for x=0, approaches 1.0 at halfpoint, clamped to [0, 1].
func LogNorm(x float64, halfpoint float64) float64 {
	if x <= 0 || halfpoint <= 0 {
		return 0
	}
	v := math.Log2(1+x) / math.Log2(1+halfpoint)
	if v > 1 {
		return 1
	}
	return v
}

// DecayNorm models recency with exponential decay.
// Returns 1.0 for days=0 (today), 0.5 at days=halflife.
func DecayNorm(days float64, halflife float64) float64 {
	if days <= 0 {
		return 1
	}
	if halflife <= 0 {
		return 0
	}
	return math.Exp(-0.693 * days / halflife)
}

// InverseLogNorm returns 1 - LogNorm(x, halfpoint).
// For metrics where lower is better (e.g., commits behind parent).
func InverseLogNorm(x float64, halfpoint float64) float64 {
	return 1.0 - LogNorm(x, halfpoint)
}

// ClampRatio maps x linearly from [low, high] to [0, 1], clamped.
func ClampRatio(x, low, high float64) float64 {
	if high <= low {
		return 0
	}
	v := (x - low) / (high - low)
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// LogNormRange normalizes x using LogNorm and scales to maxPts.
// Equivalent to LogNorm(x, ceiling) * maxPts — returns a value in [0, maxPts].
func LogNormRange(x, ceiling, maxPts float64) float64 {
	return LogNorm(x, ceiling) * maxPts
}

// ExpDecay is an alias for DecayNorm with clearer naming for the v2 formulas.
func ExpDecay(days, halfLife float64) float64 {
	return DecayNorm(days, halfLife)
}

// Percentile returns the percentile rank of val within a sorted (ascending) slice.
// Returns 0.0-1.0. If sorted is empty, returns 0.
func Percentile(val float64, sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	// Count values strictly less than val
	below := 0
	equal := 0
	for _, v := range sorted {
		if v < val {
			below++
		} else if v == val {
			equal++
		}
	}
	// Use midpoint of equal-rank range
	return (float64(below) + float64(equal)/2.0) / float64(n)
}
