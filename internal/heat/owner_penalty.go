package heat

import (
	"math"

	"github.com/svnbjrn/spoon/internal/forge"
)

// ownerFarmerForkFloor is the minimum number of forks an owner has
// before we treat the "fork farmer" signal as meaningful. Below this
// floor the user's repo mix is too small to characterize.
const ownerFarmerForkFloor = 5

// ownerFarmerSignalThreshold is the maximum signal-fork fraction that
// still triggers the penalty. A fraction above this threshold means
// the owner's forks are doing real work, so the penalty is 0.
const ownerFarmerSignalThreshold = 0.4

// ownerFarmerMaxPenalty caps the magnitude of the penalty at -10.
// Combined with the existing T1-T3 floor, this keeps the post-penalty
// score in [0, 100].
const ownerFarmerMaxPenalty = -10.0

// ComputeForkFarmerPenalty returns the penalty in (-10, 0] for forks
// whose owner has many mostly-fork repos and few of their own.
//
// The penalty is 0 when:
//
//   - profile == nil (no signal available)
//   - !profile.Complete (the sample is a window of the account; "few
//     non-fork repos" cannot be shown from part of it, so a negative
//     classification is not supported)
//   - profile.ForkCount < ownerFarmerForkFloor (too few forks to characterize)
//   - profile.NonForkRepoCount >= 2 (likely a maintainer, not a farmer)
//
// Otherwise the penalty is -10 * (1 - signalFraction / 0.4), so:
//
//   - signalFraction = 0.4 → 1 - 1 = 0 → no penalty
//   - signalFraction = 0.2 → 1 - 0.5 = 0.5 → -5
//   - signalFraction = 0   → 1 - 0 = 1 → -10
//
// SignalForkCount is the number of forks pushed within the last year
// (the "are they doing real work" axis); the ratio is computed over
// ForkCount so a single very-recent fork is not enough to clear the
// penalty when the user has 50 stale forks sitting in their account.
func ComputeForkFarmerPenalty(profile *forge.OwnerProfile) float64 {
	if profile == nil {
		return 0
	}
	if !profile.Complete {
		return 0
	}
	if profile.ForkCount < ownerFarmerForkFloor {
		return 0
	}
	if profile.NonForkRepoCount >= 2 {
		return 0
	}
	forkCount := float64(profile.ForkCount)
	if forkCount <= 0 {
		return 0
	}
	signalFraction := float64(profile.SignalForkCount) / forkCount
	if signalFraction >= ownerFarmerSignalThreshold {
		return 0
	}
	// 0 ≤ signalFraction < 0.4 → 0 < penalty ≤ -10
	magnitude := 1.0 - (signalFraction / ownerFarmerSignalThreshold)
	penalty := ownerFarmerMaxPenalty * magnitude
	if penalty < ownerFarmerMaxPenalty {
		penalty = ownerFarmerMaxPenalty
	}
	if math.IsNaN(penalty) {
		return 0
	}
	return penalty
}

// forkFarmerFromWeights scales ComputeForkFarmerPenalty by the user's
// weight for "fork_farmer". factor in [0, 2]; missing → 1 (no
// scaling). A non-positive raw result is a no-op.
func forkFarmerFromWeights(profile *forge.OwnerProfile, weights map[string]float64) float64 {
	raw := ComputeForkFarmerPenalty(profile)
	if raw >= 0 {
		return 0
	}
	return raw * weightFor(weights, "fork_farmer")
}
