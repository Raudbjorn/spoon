package heat

import "testing"

func TestScorer_ScoreRaw(t *testing.T) {
	stats := []ForkStats{
		{ForkID: 1, Stars: 10, SubForks: 1},
		{ForkID: 2, Stars: 50, SubForks: 5},
		{ForkID: 3, Stars: 100, SubForks: 10},
		{ForkID: 4, Stars: 200, SubForks: 20},
		{ForkID: 5, Stars: 500, SubForks: 50},
		{ForkID: 6, Stars: 0, SubForks: 0},
		{ForkID: 7, Stars: 30, SubForks: 3},
		{ForkID: 8, Stars: 75, SubForks: 7},
		{ForkID: 9, Stars: 150, SubForks: 12},
		{ForkID: 10, Stars: 300, SubForks: 30},
	}
	s := NewScorer(stats)

	if s.IsTinySet() {
		t.Error("10 forks should not be tiny set")
	}

	input := ScoreInput{
		T1: Tier1ParamsV2{Stars: 50, SubForks: 5, DaysSincePush: 10, ReleaseCount: 2},
	}
	result := s.ScoreRaw(input)
	if result.Score <= 0 {
		t.Errorf("Expected positive score, got %v", result.Score)
	}
	if result.IsTinySet {
		t.Error("Should not be tiny set with 10 forks")
	}
}

func TestScorer_TinySet(t *testing.T) {
	stats := []ForkStats{
		{ForkID: 1, Stars: 10, SubForks: 1},
		{ForkID: 2, Stars: 50, SubForks: 5},
	}
	s := NewScorer(stats)

	if !s.IsTinySet() {
		t.Error("2 forks should be tiny set")
	}

	input := ScoreInput{
		T1: Tier1ParamsV2{Stars: 50, SubForks: 5, DaysSincePush: 10},
	}
	result := s.ScoreRaw(input)
	if !result.IsTinySet {
		t.Error("Should be tiny set")
	}
}

func TestScorer_Finalize(t *testing.T) {
	stats := make([]ForkStats, 20)
	for i := range stats {
		stats[i] = ForkStats{
			ForkID:   int64(i + 1),
			Stars:    i * 10,
			SubForks: i,
		}
	}
	s := NewScorer(stats)

	result := HeatResult{Score: 50}
	s.Finalize(&result, 15, PenaltyInput{AheadAllBranches: 10, RecencyPct: 0.5})

	// Fork 15 has Stars=140, SubForks=14 — should be high percentile
	// Trust should be > 0.5
	if result.Trust <= 0.5 {
		t.Errorf("High-stats fork should have trust > 0.5, got %v", result.Trust)
	}
	if result.Score <= 0 {
		t.Errorf("Score should be positive after finalize, got %v", result.Score)
	}
}

func TestScorer_Finalize_NoAheadPenalty(t *testing.T) {
	stats := make([]ForkStats, 15)
	for i := range stats {
		stats[i] = ForkStats{ForkID: int64(i + 1), Stars: i * 5, SubForks: i}
	}
	s := NewScorer(stats)

	result := HeatResult{Score: 60}
	s.Finalize(&result, 5, PenaltyInput{AheadAllBranches: 0})

	if result.Score != 0 {
		t.Errorf("No ahead should zero score, got %v", result.Score)
	}
}

func TestScorer_Finalize_TinySetSkipsTrust(t *testing.T) {
	stats := []ForkStats{
		{ForkID: 1, Stars: 10, SubForks: 1},
		{ForkID: 2, Stars: 50, SubForks: 5},
	}
	s := NewScorer(stats)

	result := HeatResult{Score: 40, IsTinySet: true}
	s.Finalize(&result, 1, PenaltyInput{AheadAllBranches: 5, RecencyPct: 0.5})

	// Tiny set should skip trust, score unchanged
	if result.Score != 40 {
		t.Errorf("Tiny set finalize should not modify score, got %v", result.Score)
	}
	if result.Trust != 0 {
		t.Errorf("Tiny set should have 0 trust, got %v", result.Trust)
	}
}

func TestScorer_ForkCount(t *testing.T) {
	stats := make([]ForkStats, 7)
	for i := range stats {
		stats[i] = ForkStats{ForkID: int64(i + 1)}
	}
	s := NewScorer(stats)
	if s.ForkCount() != 7 {
		t.Errorf("ForkCount = %d, want 7", s.ForkCount())
	}
}
