package forksops

// Rate-budget reserve.
//
// Enrichment (the expensive T2 compare / T3 contributors calls) stops as soon
// as the forge's rate-limit headroom falls below ReserveHeadroom, leaving a
// buffer so a large scan never drains the window to zero. Draining to zero is
// what produced the infamous "every fork looks inert" export: a second run in
// the same rate window got nothing but silent all-zero divergence.
//
// This makes budgeting automatic — the user never has to look up the fork count
// to decide a --budget. Best-first dispatch (DispatchPriority) ensures the forks
// that DO get enriched before the floor is hit are the ones most likely to have
// diverged, and forks skipped because of the floor are marked degraded
// (Result.BudgetSkip), never silently zeroed.
//
// 0.10 of 5,000/hr ≈ 500 requests held in reserve — enough for other tooling
// and a safety margin against GitHub's secondary limits. A run that fits well
// under budget never reaches the floor and enriches everything.
const ReserveHeadroom = 0.10

// perRequestCost is a rough lower-bound estimate of API requests per enriched
// fork by tier, used ONLY for the up-front estimate logged to the user — never
// for control flow (the live headroom floor is authoritative). T2 ≈ compare +
// behind-compare + branch-tip resolve; T3 adds contributors. Pagination on
// high-divergence forks pushes the real number higher, so this under-counts.
func perRequestCost(tier int) int {
	switch {
	case tier >= 3:
		return 5
	case tier == 2:
		return 3
	default:
		return 0 // T1 is already resolved during enumeration
	}
}

// EstimateRequests returns a rough lower bound on the API requests an enrichment
// pass of nForks at the given tier will cost. Used for the informational
// "~N requests" heads-up; the headroom reserve, not this number, governs when
// enrichment actually stops.
func EstimateRequests(nForks, tier int) int {
	return nForks * perRequestCost(tier)
}
