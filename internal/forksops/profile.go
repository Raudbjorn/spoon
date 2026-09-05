package forksops

import (
	"sort"

	"github.com/svnbjrn/spoon/internal/heat"
)

// DeriveProfile returns a deterministic one-word profile label plus the
// ordered facts that produced it, derived only from already-computed fields
// on r. Presentation-only: it never feeds scoring, ordering, or visibility.
// Precedence is first-match-wins; "standard" is the floor, so the label is
// always non-empty.
func DeriveProfile(r Result) (string, []string) {
	// 0. Pinned wins: the user asked for forks touching a path; say so.
	if r.Visibility.Status == VisibilityPinned {
		return "touches_target", sortedCopy(r.Visibility.Reasons)
	}
	// 1. Hidden wins outright: an upstreamed / no-ahead fork is non-actionable.
	if r.Visibility.Status == VisibilityHidden {
		return "hidden", sortedCopy(r.Visibility.Reasons)
	}
	// 2. Lone-wolf archetype (only the three actionable archetypes map;
	//    ArchetypeNone / undetected falls through to the next rule).
	if r.Heat.LoneWolfV2 != nil && r.Heat.LoneWolfV2.Detected {
		switch r.Heat.LoneWolfV2.Archetype {
		case heat.ArchetypeSniper:
			return "focused_change", []string{"archetype:" + r.Heat.LoneWolfV2.Label}
		case heat.ArchetypeFeatureBuilder:
			return "focused_feature", []string{"archetype:" + r.Heat.LoneWolfV2.Label}
		case heat.ArchetypeDrifter:
			return "broad_maintenance", []string{"archetype:" + r.Heat.LoneWolfV2.Label}
		}
	}
	// 3. Momentum: rising or newly-observed.
	if r.Momentum.Status == MomentumRising || r.Momentum.Status == MomentumNew {
		return "emerging_active", []string{"momentum:" + string(r.Momentum.Status)}
	}
	// 4. Stale: archived / low-recency penalties.
	if stale := matchingPenalties(r.Heat.Penalties, "archived", "low_recency"); len(stale) > 0 {
		return "stale", stale
	}
	// 5. Matches curated priors.
	if r.PriorScore > 0 {
		return "matches_priors", sortedCopy(r.PriorReasons)
	}
	// 6. Floor.
	return "standard", nil
}

// matchingPenalties returns the sorted subset of penalties that appear in
// want (nil when none match).
func matchingPenalties(penalties []string, want ...string) []string {
	set := make(map[string]bool, len(want))
	for _, w := range want {
		set[w] = true
	}
	var out []string
	for _, p := range penalties {
		if set[p] {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// sortedCopy returns a sorted copy of in (nil when empty), leaving the
// source slice untouched.
func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
