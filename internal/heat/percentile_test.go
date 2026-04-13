package heat

import "testing"

func TestPercentileTable_Stars(t *testing.T) {
	stats := []ForkStats{
		{ForkID: 1, Stars: 0, SubForks: 0},
		{ForkID: 2, Stars: 5, SubForks: 1},
		{ForkID: 3, Stars: 10, SubForks: 2},
		{ForkID: 4, Stars: 50, SubForks: 5},
		{ForkID: 5, Stars: 100, SubForks: 10},
	}
	pt := NewPercentileTable(stats)

	// Fork 1 (0 stars) should be lowest percentile
	p1 := pt.StarsPercentile(1)
	// Fork 5 (100 stars) should be highest percentile
	p5 := pt.StarsPercentile(5)

	if p1 >= p5 {
		t.Errorf("Stars percentile not monotonic: fork1=%v >= fork5=%v", p1, p5)
	}
	if p5 < 0.8 {
		t.Errorf("Top star fork should be high percentile, got %v", p5)
	}
	if p1 > 0.2 {
		t.Errorf("Zero star fork should be low percentile, got %v", p1)
	}
}

func TestPercentileTable_SubForks(t *testing.T) {
	stats := []ForkStats{
		{ForkID: 1, Stars: 0, SubForks: 0},
		{ForkID: 2, Stars: 0, SubForks: 3},
		{ForkID: 3, Stars: 0, SubForks: 10},
	}
	pt := NewPercentileTable(stats)

	p1 := pt.SubForksPercentile(1)
	p3 := pt.SubForksPercentile(3)

	if p1 >= p3 {
		t.Errorf("SubForks percentile not monotonic: fork1=%v >= fork3=%v", p1, p3)
	}
}

func TestPercentileTable_UnknownFork(t *testing.T) {
	pt := NewPercentileTable([]ForkStats{{ForkID: 1, Stars: 5, SubForks: 2}})
	if pt.StarsPercentile(999) != 0 {
		t.Error("Unknown fork should return 0 for stars percentile")
	}
	if pt.SubForksPercentile(999) != 0 {
		t.Error("Unknown fork should return 0 for sub-forks percentile")
	}
}

func TestPercentileTable_Empty(t *testing.T) {
	pt := NewPercentileTable(nil)
	if pt.StarsPercentile(1) != 0 {
		t.Error("Empty table should return 0")
	}
}

func TestPercentileTable_AllEqual(t *testing.T) {
	stats := []ForkStats{
		{ForkID: 1, Stars: 5, SubForks: 2},
		{ForkID: 2, Stars: 5, SubForks: 2},
		{ForkID: 3, Stars: 5, SubForks: 2},
	}
	pt := NewPercentileTable(stats)

	// All equal → percentile should be 0.5
	p := pt.StarsPercentile(1)
	if !approxEqual(p, 0.5, 0.01) {
		t.Errorf("All-equal stars percentile = %v, want 0.5", p)
	}
}
