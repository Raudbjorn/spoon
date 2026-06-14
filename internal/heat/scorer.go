package heat

// Scorer is the shared scoring orchestrator used by both the TUI and spn
// paths. It holds the percentile table (built after T1 completes), the fork
// count, and the user's component weights.
type Scorer struct {
	pctTable  *PercentileTable
	forkCount int
	weights   map[string]float64
}

// NewScorer creates a Scorer from fork stats with default weights.
func NewScorer(stats []ForkStats) *Scorer {
	return NewScorerWeighted(stats, nil)
}

// NewScorerWeighted creates a Scorer with per-component weight overrides
// (see RawScoreWeighted). nil/empty weights means defaults.
func NewScorerWeighted(stats []ForkStats, weights map[string]float64) *Scorer {
	// Copy the caller's map so later mutations (or sharing it across
	// goroutines) can't change scoring behavior mid-run.
	var w map[string]float64
	if len(weights) > 0 {
		w = make(map[string]float64, len(weights))
		for k, v := range weights {
			w[k] = v
		}
	}
	return &Scorer{
		pctTable:  NewPercentileTable(stats),
		forkCount: len(stats),
		weights:   w,
	}
}

// IsTinySet returns true if the fork count is below the percentile threshold.
// Tiny sets score on the same additive tiers as everyone else; they only
// skip the percentile-based trust multiplier in Finalize (percentiles over
// <10 samples are noise).
func (s *Scorer) IsTinySet() bool {
	return s.forkCount < 10
}

// ForkCount returns the number of forks tracked by this scorer.
func (s *Scorer) ForkCount() int {
	return s.forkCount
}

// ScoreRaw computes the raw additive score (no trust, no penalties).
func (s *Scorer) ScoreRaw(input ScoreInput) HeatResult {
	result := RawScoreWeighted(input, s.weights)
	result.IsTinySet = s.IsTinySet()
	return result
}

// Finalize applies trust and penalties to a raw result. Trust requires
// meaningful percentiles and is skipped for tiny sets; penalties (no-ahead
// zeroing, archived cap, low-recency dampening) always apply.
func (s *Scorer) Finalize(result *HeatResult, forkID int64, penalty PenaltyInput) {
	if !s.IsTinySet() {
		starsPct := s.pctTable.StarsPercentile(forkID)
		subForksPct := s.pctTable.SubForksPercentile(forkID)
		ApplyTrust(result, starsPct, subForksPct)
		penalty.RecencyPct = s.pctTable.RecencyPercentile(forkID)
	} else {
		// Percentile recency is noise on tiny sets; exempt them from the
		// low-recency penalty rather than feed it garbage.
		penalty.RecencyPct = 1
	}
	ApplyPenalties(result, penalty)
}

// LoadWeights reads a heat-weights JSON file ({"component": factor, ...})
// and validates keys and ranges. Factors must be in [0, 2]; valid keys are
// the component names: recency, stars, sub_forks, releases, mna,
// sync_ratio, feature_ratio, lone_wolf, span, novelty.
func LoadWeights(path string) (map[string]float64, error) {
	return loadWeightsFile(path)
}
