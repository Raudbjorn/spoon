package heat

import (
	"math"
	"testing"
)

func approxEqual(a, b, tolerance float64) bool {
	return math.Abs(a-b) < tolerance
}

func TestLogNorm(t *testing.T) {
	tests := []struct {
		name      string
		x         float64
		halfpoint float64
		want      float64
		tol       float64
	}{
		{"zero", 0, 50, 0, 0.001},
		{"at halfpoint", 50, 50, 1.0, 0.05},
		{"small", 5, 50, 0.46, 0.05},
		{"large (clamped)", 1000, 50, 1.0, 0.001},
		{"negative", -10, 50, 0, 0.001},
		{"zero halfpoint", 10, 0, 0, 0.001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LogNorm(tt.x, tt.halfpoint)
			if !approxEqual(got, tt.want, tt.tol) {
				t.Errorf("LogNorm(%v, %v) = %v, want ~%v", tt.x, tt.halfpoint, got, tt.want)
			}
		})
	}
}

func TestDecayNorm(t *testing.T) {
	tests := []struct {
		name     string
		days     float64
		halflife float64
		want     float64
		tol      float64
	}{
		{"today", 0, 180, 1.0, 0.001},
		{"at halflife", 180, 180, 0.5, 0.02},
		{"double halflife", 360, 180, 0.25, 0.02},
		{"negative days", -5, 180, 1.0, 0.001},
		{"zero halflife", 10, 0, 0, 0.001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecayNorm(tt.days, tt.halflife)
			if !approxEqual(got, tt.want, tt.tol) {
				t.Errorf("DecayNorm(%v, %v) = %v, want ~%v", tt.days, tt.halflife, got, tt.want)
			}
		})
	}
}

func TestInverseLogNorm(t *testing.T) {
	got := InverseLogNorm(0, 50)
	if !approxEqual(got, 1.0, 0.001) {
		t.Errorf("InverseLogNorm(0, 50) = %v, want 1.0", got)
	}
	got = InverseLogNorm(50, 50)
	if !approxEqual(got, 0.0, 0.05) {
		t.Errorf("InverseLogNorm(50, 50) = %v, want ~0.0", got)
	}
}

func TestClampRatio(t *testing.T) {
	tests := []struct {
		name               string
		x, low, high, want float64
	}{
		{"middle", 5, 0, 10, 0.5},
		{"below", -5, 0, 10, 0},
		{"above", 15, 0, 10, 1},
		{"at low", 0, 0, 10, 0},
		{"at high", 10, 0, 10, 1},
		{"invalid range", 5, 10, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClampRatio(tt.x, tt.low, tt.high)
			if !approxEqual(got, tt.want, 0.001) {
				t.Errorf("ClampRatio(%v, %v, %v) = %v, want %v", tt.x, tt.low, tt.high, got, tt.want)
			}
		})
	}
}

func TestLogNormRange(t *testing.T) {
	tests := []struct {
		name               string
		x, ceiling, maxPts float64
		want               float64
		tol                float64
	}{
		{"zero", 0, 10000, 12, 0, 0.001},
		{"at ceiling", 10000, 10000, 12, 12, 0.5},
		{"half", 50, 10000, 12, 0, 1.0}, // small relative to ceiling
		{"exceeds ceiling", 50000, 10000, 12, 12, 0.001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LogNormRange(tt.x, tt.ceiling, tt.maxPts)
			if got < 0 || got > tt.maxPts+0.001 {
				t.Errorf("LogNormRange(%v, %v, %v) = %v, out of range [0, %v]", tt.x, tt.ceiling, tt.maxPts, got, tt.maxPts)
			}
		})
	}

	// Monotonicity: more input = more points
	a := LogNormRange(100, 10000, 15)
	b := LogNormRange(1000, 10000, 15)
	c := LogNormRange(10000, 10000, 15)
	if a >= b || b >= c {
		t.Errorf("LogNormRange not monotonic: %v >= %v >= %v", a, b, c)
	}
}

func TestExpDecay(t *testing.T) {
	// ExpDecay should match DecayNorm
	got := ExpDecay(30, 30)
	want := DecayNorm(30, 30)
	if !approxEqual(got, want, 0.001) {
		t.Errorf("ExpDecay(30, 30) = %v, want %v (same as DecayNorm)", got, want)
	}
}

func TestPercentile(t *testing.T) {
	sorted := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	tests := []struct {
		name string
		val  float64
		want float64
		tol  float64
	}{
		{"min value", 1, 0.05, 0.01},
		{"max value", 10, 0.95, 0.01},
		{"median", 5, 0.45, 0.01},
		{"below all", 0, 0, 0.01},
		{"above all", 100, 1.0, 0.01},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Percentile(tt.val, sorted)
			if !approxEqual(got, tt.want, tt.tol) {
				t.Errorf("Percentile(%v, ...) = %v, want ~%v", tt.val, got, tt.want)
			}
		})
	}
}

func TestPercentile_Empty(t *testing.T) {
	got := Percentile(5, nil)
	if got != 0 {
		t.Errorf("Percentile with empty slice = %v, want 0", got)
	}
}

func TestPercentile_Ties(t *testing.T) {
	sorted := []float64{5, 5, 5, 5, 5}
	got := Percentile(5, sorted)
	if !approxEqual(got, 0.5, 0.01) {
		t.Errorf("Percentile(5, [5,5,5,5,5]) = %v, want 0.5", got)
	}
}

func TestPercentile_Single(t *testing.T) {
	sorted := []float64{42}
	got := Percentile(42, sorted)
	if !approxEqual(got, 0.5, 0.01) {
		t.Errorf("Percentile(42, [42]) = %v, want 0.5", got)
	}
}
