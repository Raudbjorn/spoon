package embed

import (
	"math"
	"testing"
)

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
