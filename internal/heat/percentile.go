package heat

import "sort"

// ForkStats holds the metrics needed for percentile ranking across forks.
type ForkStats struct {
	ForkID   int64
	Stars    int
	SubForks int
	// PushedDays is days since the fork's last push (lower = more recent).
	PushedDays float64
}

// PercentileTable pre-computes sorted distributions for percentile lookups.
type PercentileTable struct {
	sortedStars    []float64
	sortedSubForks []float64
	sortedPushed   []float64
	starsByID      map[int64]int
	subForksByID   map[int64]int
	pushedByID     map[int64]float64
}

// NewPercentileTable builds a percentile table from fork stats.
func NewPercentileTable(stats []ForkStats) *PercentileTable {
	n := len(stats)
	pt := &PercentileTable{
		sortedStars:    make([]float64, n),
		sortedSubForks: make([]float64, n),
		sortedPushed:   make([]float64, n),
		starsByID:      make(map[int64]int, n),
		subForksByID:   make(map[int64]int, n),
		pushedByID:     make(map[int64]float64, n),
	}

	for i, s := range stats {
		pt.sortedStars[i] = float64(s.Stars)
		pt.sortedSubForks[i] = float64(s.SubForks)
		pt.sortedPushed[i] = s.PushedDays
		pt.starsByID[s.ForkID] = s.Stars
		pt.subForksByID[s.ForkID] = s.SubForks
		pt.pushedByID[s.ForkID] = s.PushedDays
	}

	sort.Float64s(pt.sortedStars)
	sort.Float64s(pt.sortedSubForks)
	sort.Float64s(pt.sortedPushed)

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

// RecencyPercentile returns the recency rank [0,1] for a fork — 1 means
// pushed most recently in the set. Computed as the inverse percentile of
// days-since-push.
func (pt *PercentileTable) RecencyPercentile(forkID int64) float64 {
	days, ok := pt.pushedByID[forkID]
	if !ok {
		return 0
	}
	return 1 - Percentile(days, pt.sortedPushed)
}
