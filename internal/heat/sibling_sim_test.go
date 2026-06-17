package heat

import "testing"

// TestApplySiblingSimilarityToScore_NilResult covers the no-op path:
// a nil HeatResult is safe and does nothing.
func TestApplySiblingSimilarityToScore_NilResult(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil result should not panic, got %v", r)
		}
	}()
	ApplySiblingSimilarityToScore(nil)
}

// TestApplySiblingSimilarityToScore_ZeroSim covers the no-signal
// path: a zero or negative SiblingSim is a no-op.
func TestApplySiblingSimilarityToScore_ZeroSim(t *testing.T) {
	hr := &HeatResult{Score: 50, SiblingSim: 0}
	ApplySiblingSimilarityToScore(hr)
	if hr.Score != 50 {
		t.Errorf("zero sim: Score=%v, want 50 (unchanged)", hr.Score)
	}
}

// TestApplySiblingSimilarityToScore_AddsFiveAtMax covers the
// maximum bonus path: sim=1.0 with no NoveltyScore yields +5
// (well below the 7.5 cap).
func TestApplySiblingSimilarityToScore_AddsFiveAtMax(t *testing.T) {
	hr := &HeatResult{
		Score:      50,
		SiblingSim: 1.0,
	}
	ApplySiblingSimilarityToScore(hr)
	if hr.Score != 55 {
		t.Errorf("max sim: Score=%v, want 55", hr.Score)
	}
}

// TestApplySiblingSimilarityToScore_CapKicksIn covers the combined
// cap: when NoveltyScore=1.0 (cluster bonus = +5) and SiblingSim=1.0
// (would add another +5), the combined cap of 7.5 trims the sibling
// contribution to 2.5. ApplySiblingSimilarityToScore only adds the
// sibling portion — the cluster bonus was already applied via
// ApplyNoveltyToScore. So a starting score of 50 (which is BEFORE
// novelty is applied) lands at 52.5 (50 + sibling share = 2.5).
func TestApplySiblingSimilarityToScore_CapKicksIn(t *testing.T) {
	hr := &HeatResult{
		Score:        50,
		SiblingSim:   1.0,
		NoveltyScore: 1.0,
	}
	ApplySiblingSimilarityToScore(hr)
	// clusterBonus = 5; sibBonus capped at 7.5 - 5 = 2.5
	// net = 50 + 2.5 = 52.5 (only sibling portion added here)
	if hr.Score != 52.5 {
		t.Errorf("capped: Score=%v, want 52.5", hr.Score)
	}
}

// TestApplySiblingSimilarityToScore_WeightScales covers the weight
// passthrough: with siblingSimWeight=0.5, a sim=0.5 yields +1.25
// (0.5 * 5 * 0.5).
func TestApplySiblingSimilarityToScore_WeightScales(t *testing.T) {
	hr := &HeatResult{
		Score:               50,
		SiblingSim:          0.5,
		siblingSimWeight:    0.5,
		siblingSimWeightSet: true,
	}
	ApplySiblingSimilarityToScore(hr)
	if hr.Score != 51.25 {
		t.Errorf("weight-scaled: Score=%v, want 51.25", hr.Score)
	}
}

// TestApplySiblingSimilarityToScore_CapsAtHundred covers the upper
// cap: with a starting score already near 100, the cap math trims
// the sibling contribution so the total stays at 100.
func TestApplySiblingSimilarityToScore_CapsAtHundred(t *testing.T) {
	hr := &HeatResult{
		Score:        98,
		SiblingSim:   1.0,
		NoveltyScore: 1.0,
	}
	ApplySiblingSimilarityToScore(hr)
	if hr.Score > 100 {
		t.Errorf("Score > 100: got %v, want capped at 100", hr.Score)
	}
	if hr.Score != 100 {
		t.Errorf("expected 100, got %v", hr.Score)
	}
}
