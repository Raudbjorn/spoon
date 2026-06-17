package heat

import (
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

// TestComputeForkFarmerPenalty_NilProfile covers the no-signal path:
// a nil profile yields 0 (no penalty).
func TestComputeForkFarmerPenalty_NilProfile(t *testing.T) {
	if got := ComputeForkFarmerPenalty(nil); got != 0 {
		t.Errorf("nil profile: got %v, want 0", got)
	}
}

// TestComputeForkFarmerPenalty_TooFewForks covers the floor: a user
// with < 5 forks is too small to characterize. 0 penalty.
func TestComputeForkFarmerPenalty_TooFewForks(t *testing.T) {
	prof := &forge.OwnerProfile{ForkCount: 3, SignalForkCount: 0, NonForkRepoCount: 0}
	if got := ComputeForkFarmerPenalty(prof); got != 0 {
		t.Errorf("too-few-forks: got %v, want 0", got)
	}
}

// TestComputeForkFarmerPenalty_LooksLikeMaintainer covers the
// maintainer path: a user with ≥ 2 non-fork repos is probably a
// developer, not a farmer. 0 penalty.
func TestComputeForkFarmerPenalty_LooksLikeMaintainer(t *testing.T) {
	prof := &forge.OwnerProfile{ForkCount: 15, SignalForkCount: 8, NonForkRepoCount: 5}
	if got := ComputeForkFarmerPenalty(prof); got != 0 {
		t.Errorf("maintainer: got %v, want 0", got)
	}
}

// TestComputeForkFarmerPenalty_FarmerFull covers the strongest
// farmer signal: 15 forks, 0 maintained, 0 non-fork repos.
// signalFraction = 0/15 = 0 → magnitude = 1 - 0/0.4 = 1 → -10.
func TestComputeForkFarmerPenalty_FarmerFull(t *testing.T) {
	prof := &forge.OwnerProfile{ForkCount: 15, SignalForkCount: 0, NonForkRepoCount: 0}
	if got := ComputeForkFarmerPenalty(prof); got != -10 {
		t.Errorf("full farmer: got %v, want -10", got)
	}
}

// TestComputeForkFarmerPenalty_FarmerPartial covers the partial
// signal: 15 forks, 3 maintained, 0 non-fork.
// signalFraction = 3/15 = 0.2 → magnitude = 1 - 0.2/0.4 = 0.5 → -5.
func TestComputeForkFarmerPenalty_FarmerPartial(t *testing.T) {
	prof := &forge.OwnerProfile{ForkCount: 15, SignalForkCount: 3, NonForkRepoCount: 0}
	if got := ComputeForkFarmerPenalty(prof); got != -5 {
		t.Errorf("partial farmer: got %v, want -5", got)
	}
}

// TestComputeForkFarmerPenalty_AboveThreshold covers the threshold:
// 15 forks, 6 maintained, 0 non-fork.
// signalFraction = 6/15 = 0.4 → no penalty.
func TestComputeForkFarmerPenalty_AboveThreshold(t *testing.T) {
	prof := &forge.OwnerProfile{ForkCount: 15, SignalForkCount: 6, NonForkRepoCount: 0}
	if got := ComputeForkFarmerPenalty(prof); got != 0 {
		t.Errorf("above threshold: got %v, want 0", got)
	}
}

// TestApplyPenalties_ForkFarmerRecorded covers the integration: the
// penalty is appended to result.Penalties, and the score is reduced
// by 10.
func TestApplyPenalties_ForkFarmerRecorded(t *testing.T) {
	result := HeatResult{Score: 60}
	prof := &forge.OwnerProfile{ForkCount: 15, SignalForkCount: 0, NonForkRepoCount: 0}
	ApplyPenalties(&result, PenaltyInput{
		AheadKnown:       true,
		AheadAllBranches: 5,
		RecencyPct:       0.5,
		OwnerProfile:     prof,
	}, nil)
	if result.Score != 50 {
		t.Errorf("Score: got %v, want 50", result.Score)
	}
	found := false
	for _, p := range result.Penalties {
		if p == "fork_farmer" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'fork_farmer' in Penalties, got %v", result.Penalties)
	}
}

// TestApplyPenalties_ForkFarmerDisabledByWeight covers the user-off
// path: weight 0 disables the penalty.
func TestApplyPenalties_ForkFarmerDisabledByWeight(t *testing.T) {
	result := HeatResult{Score: 60}
	prof := &forge.OwnerProfile{ForkCount: 15, SignalForkCount: 0, NonForkRepoCount: 0}
	ApplyPenalties(&result, PenaltyInput{
		AheadKnown:       true,
		AheadAllBranches: 5,
		RecencyPct:       0.5,
		OwnerProfile:     prof,
	}, map[string]float64{"fork_farmer": 0})
	if result.Score != 60 {
		t.Errorf("disabled penalty: Score=%v, want 60", result.Score)
	}
	for _, p := range result.Penalties {
		if p == "fork_farmer" {
			t.Errorf("disabled penalty should not record 'fork_farmer'")
		}
	}
}
