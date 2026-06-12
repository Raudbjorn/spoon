package embed

import (
	"math"
	"testing"
)

// fixture: batch=2, seq=3, hidden=2.
// item 0: tokens [1,2,3] all attended.
// item 1: tokens [10,20] attended, third position padded.
var (
	poolHidden = []float32{
		// batch 0
		1, 2, // t0 (CLS)
		3, 4, // t1
		5, 6, // t2
		// batch 1
		10, 20, // t0 (CLS)
		30, 40, // t1 (last attended)
		99, 99, // t2 (padding — must be ignored)
	}
	poolMask = []int64{
		1, 1, 1,
		1, 1, 0,
	}
)

func TestPoolHiddenStates_CLS(t *testing.T) {
	got := PoolHiddenStates(PoolingCLS, poolHidden, poolMask, 2, 3, 2)
	want := [][]float32{{1, 2}, {10, 20}}
	assertVectors(t, got, want)
}

func TestPoolHiddenStates_Mean(t *testing.T) {
	got := PoolHiddenStates(PoolingMean, poolHidden, poolMask, 2, 3, 2)
	// batch 0: mean of all three positions; batch 1: mean of first two only.
	want := [][]float32{{3, 4}, {20, 30}}
	assertVectors(t, got, want)
}

func TestPoolHiddenStates_Last(t *testing.T) {
	got := PoolHiddenStates(PoolingLast, poolHidden, poolMask, 2, 3, 2)
	// Last attended position: t2 for batch 0, t1 for batch 1.
	want := [][]float32{{5, 6}, {30, 40}}
	assertVectors(t, got, want)
}

func TestPoolHiddenStates_AllMaskedMeanDoesNotDivideByZero(t *testing.T) {
	mask := []int64{0, 0, 0, 1, 1, 1}
	got := PoolHiddenStates(PoolingMean, poolHidden, mask, 2, 3, 2)
	for _, x := range got[0] {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			t.Fatalf("all-masked row produced non-finite value: %v", got[0])
		}
	}
}

func TestL2NormalizeAll(t *testing.T) {
	vs := []Vector{{3, 4}, {0, 0}}
	L2NormalizeAll(vs)
	if math.Abs(float64(vs[0][0])-0.6) > 1e-6 || math.Abs(float64(vs[0][1])-0.8) > 1e-6 {
		t.Errorf("normalize: got %v", vs[0])
	}
	if vs[1][0] != 0 || vs[1][1] != 0 {
		t.Errorf("zero vector must stay zero: %v", vs[1])
	}
}

func TestParsePooling(t *testing.T) {
	cases := map[string]Pooling{
		"": PoolingCLS, "cls": PoolingCLS, "CLS": PoolingCLS,
		"mean": PoolingMean, "MEAN": PoolingMean,
		"last": PoolingLast, "LAST": PoolingLast,
	}
	for in, want := range cases {
		got, ok := ParsePooling(in)
		if !ok || got != want {
			t.Errorf("ParsePooling(%q) = %v, %v; want %v, true", in, got, ok, want)
		}
	}
	if _, ok := ParsePooling("avg"); ok {
		t.Error("ParsePooling should reject unknown modes")
	}
}

func assertVectors(t *testing.T, got []Vector, want [][]float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d vectors, want %d", len(got), len(want))
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("vector %d: dim %d, want %d", i, len(got[i]), len(want[i]))
		}
		for j := range want[i] {
			if math.Abs(float64(got[i][j]-want[i][j])) > 1e-6 {
				t.Fatalf("vector %d: got %v, want %v", i, got[i], want[i])
			}
		}
	}
}
