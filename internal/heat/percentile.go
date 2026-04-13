package heat

import "sort"

// ForkStats holds the metrics needed for percentile ranking across forks.
type ForkStats struct {
	ForkID   int64
	Stars    int
	SubForks int
}

// PercentileTable pre-computes sorted distributions for percentile lookups.
type PercentileTable struct {
	sortedStars    []float64
	sortedSubForks []float64
	starsByID      map[int64]int
	subForksByID   map[int64]int
}

// NewPercentileTable builds a percentile table from fork stats.
func NewPercentileTable(stats []ForkStats) *PercentileTable {
	n := len(stats)
	pt := &PercentileTable{
		sortedStars:    make([]float64, n),
		sortedSubForks: make([]float64, n),
		starsByID:      make(map[int64]int, n),
		subForksByID:   make(map[int64]int, n),
	}

	for i, s := range stats {
		pt.sortedStars[i] = float64(s.Stars)
		pt.sortedSubForks[i] = float64(s.SubForks)
		pt.starsByID[s.ForkID] = s.Stars
		pt.subForksByID[s.ForkID] = s.SubForks
	}

	sort.Float64s(pt.sortedStars)
	sort.Float64s(pt.sortedSubForks)

	return pt
}

// StarsPercentile returns the percentile rank [0,1] for a fork's star count.
func (pt *PercentileTable) StarsPercentile(forkID int64) float64 {
	stars, ok := pt.starsByID[forkID]
	if !ok {
		return 0
	}
	return Percentile(float64(stars), pt.sortedStars)
}

// SubForksPercentile returns the percentile rank [0,1] for a fork's sub-fork count.
func (pt *PercentileTable) SubForksPercentile(forkID int64) float64 {
	sf, ok := pt.subForksByID[forkID]
	if !ok {
		return 0
	}
	return Percentile(float64(sf), pt.sortedSubForks)
}
