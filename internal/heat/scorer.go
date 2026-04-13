package heat

// Scorer is the shared scoring orchestrator used by both TUI and dump modes.
// It holds the percentile table (built after T1 completes) and fork count.
type Scorer struct {
	pctTable  *PercentileTable
	forkCount int
}

// NewScorer creates a Scorer from fork stats.
func NewScorer(stats []ForkStats) *Scorer {
	return &Scorer{
		pctTable:  NewPercentileTable(stats),
		forkCount: len(stats),
	}
}

// IsTinySet returns true if the fork count is below the percentile threshold.
func (s *Scorer) IsTinySet() bool {
	return s.forkCount < 10
}

// ForkCount returns the number of forks tracked by this scorer.
func (s *Scorer) ForkCount() int {
	return s.forkCount
}

// ScoreRaw computes the raw additive score (no trust, no penalties).
func (s *Scorer) ScoreRaw(input ScoreInput) HeatResult {
	if s.IsTinySet() {
		return TinySetScore(input.T1.Stars, input.T1.DaysSincePush)
	}
	return RawScore(input)
}

// Finalize applies trust and penalties to a raw result.
func (s *Scorer) Finalize(result *HeatResult, forkID int64, penalty PenaltyInput) {
	if s.IsTinySet() {
		// Tiny set: only apply the no-ahead penalty, skip trust/percentile
		if penalty.AheadAllBranches == 0 && !result.IsTinySet {
			result.Score = 0
			result.Penalties = append(result.Penalties, "no_ahead")
		}
		return
	}

	starsPct := s.pctTable.StarsPercentile(forkID)
	subForksPct := s.pctTable.SubForksPercentile(forkID)
	ApplyTrust(result, starsPct, subForksPct)
	ApplyPenalties(result, penalty)
}
