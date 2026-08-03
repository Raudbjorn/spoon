package tui

import (
	"fmt"
	"sort"
)

// Duplicate detection: a fork network routinely lists one body of work several
// times, because a popular fork gets re-forked or several forks branched from
// the same commit. Observed in qvr/nonraid, where emtee40, ghenry22 and jsebean
// all reported 10 ahead / 356 behind with identical heat and sat at the top of
// the list as if they were three independent contributions.
//
// Grouping runs on the model rather than on the export DTO so the screen and
// the exported JSON cannot disagree about what is a duplicate.

// forkSiblingKey returns the identity used to decide that two forks carry the
// same work, and whether the fork is eligible for grouping at all.
//
// Three keys in descending order of proof:
//
//	f:  branch fingerprint — the tip OIDs of every ahead-of-upstream branch.
//	    Conclusive, and the only key that sees work on branches other than the
//	    one that happened to be compared.
//	h:  head SHA of the compared branch. Conclusive for that branch alone.
//	d:  diff shape. Strong but circumstantial — two unrelated forks could in
//	    principle produce the same ahead/files/additions/deletions tuple — so
//	    the prefix keeps it distinguishable by consumers.
//
// Forks with no divergence are never grouped: every unmodified mirror would
// otherwise collapse into one meaningless bucket.
func forkSiblingKey(sf ScoredFork) (string, bool) {
	if fp := sf.Fork.BranchFingerprint; fp != "" {
		return "f:" + fp, true
	}
	if sf.T2 == nil || !sf.T2.Performed || sf.T2.AheadCount == 0 {
		return "", false
	}
	if sha := sf.T2.HeadSHA; sha != "" {
		return "h:" + sha, true
	}
	totalAdds, totalDels := 0, 0
	for _, d := range sf.T2.Diffs {
		totalAdds += d.Additions
		totalDels += d.Deletions
	}
	return fmt.Sprintf("d:%d/%d/%d/%d",
		sf.T2.AheadCount, len(sf.T2.Diffs), totalAdds, totalDels), true
}

// assignDuplicateGroups tags forks carrying identical work. The highest-scoring
// member of each group becomes the primary, so a consumer can lead with one row
// and treat the rest as copies. Groups of one are left untagged.
//
// Tagging is recomputed from scratch every call: enrichment and the branch
// sweep land asynchronously, so a fork's key can sharpen from "d:" to "f:" part
// way through a run, and stale tags from the weaker key must not survive.
func (m *Model) assignDuplicateGroups() {
	groups := make(map[string][]int, len(m.forks))
	for i := range m.forks {
		m.forks[i].SiblingGroup = ""
		m.forks[i].SiblingCount = 0
		m.forks[i].SiblingPrimary = false
		if key, ok := forkSiblingKey(m.forks[i]); ok {
			groups[key] = append(groups[key], i)
		}
	}

	for key, idxs := range groups {
		if len(idxs) < 2 {
			continue
		}
		primary := idxs[0]
		for _, i := range idxs[1:] {
			if m.forks[i].Heat.Score > m.forks[primary].Heat.Score {
				primary = i
			}
		}
		for _, i := range idxs {
			m.forks[i].SiblingGroup = key
			m.forks[i].SiblingCount = len(idxs)
			m.forks[i].SiblingPrimary = i == primary
		}
	}
}

// gatherDuplicateGroups makes every duplicate group contiguous, primary first,
// over the half-open range [lo, hi) of m.forks.
//
// Sorting alone cannot be relied on for this. Duplicates usually share a score,
// so they usually land adjacent — but ties are not ordered by group, so an
// unrelated fork with the same heat/ahead/behind can sort between two members
// and fall inside the group's gutter line, which would assert an identity it
// does not have. Gathering anchors each group at the sorted position of
// whichever member is reached first — not necessarily the primary — and pulls
// the rest up behind it with the primary placed first, leaving every other
// fork's relative order untouched.
func (m *Model) gatherDuplicateGroups(lo, hi int) {
	if hi-lo < 2 {
		return
	}
	section := m.forks[lo:hi]

	// Members in their current (sorted) order, per group.
	members := make(map[string][]int, len(section))
	for i := range section {
		if g := section[i].SiblingGroup; g != "" {
			members[g] = append(members[g], i)
		}
	}
	if len(members) == 0 {
		return
	}

	out := make([]ScoredFork, 0, len(section))
	emitted := make(map[string]bool, len(members))
	for i := range section {
		g := section[i].SiblingGroup
		if g == "" {
			out = append(out, section[i])
			continue
		}
		if emitted[g] {
			continue // already flushed with its group
		}
		// Anchor at the first member reached in sorted order, then emit the
		// primary ahead of the rest so the group reads primary-first without
		// disturbing where the group as a whole sits.
		emitted[g] = true
		idxs := members[g]
		// Emit by index rather than by re-testing SiblingPrimary, so every
		// member is emitted exactly once regardless of how many are flagged.
		// assignDuplicateGroups marks exactly one, but a second flagged member
		// would otherwise be dropped here and copy() below would silently
		// truncate, leaving stale rows in the tail of the section.
		primary := -1
		for _, j := range idxs {
			if section[j].SiblingPrimary {
				out = append(out, section[j])
				primary = j
				break
			}
		}
		for _, j := range idxs {
			if j != primary {
				out = append(out, section[j])
			}
		}
	}

	if len(out) != len(section) {
		// Unreachable by construction; bail rather than truncate the list.
		return
	}
	copy(section, out)
}

// gutterOrdinals numbers duplicate groups by their order of appearance in the
// list, so the colour cycle can guarantee that adjacent groups differ.
func gutterOrdinals(forks []ScoredFork) map[string]int {
	out := make(map[string]int)
	next := 0
	for _, sf := range forks {
		if sf.SiblingGroup == "" {
			continue
		}
		if _, seen := out[sf.SiblingGroup]; seen {
			continue
		}
		out[sf.SiblingGroup] = next
		next++
	}
	return out
}

// duplicateGutter returns the left-gutter cell for a row: a solid vertical bar
// for a duplicate-group member, a space otherwise. Every member of a contiguous
// group draws the same glyph and colour, so the group reads as one unbroken
// line; ordinals ensures the next group along is a different colour.
//
// Under cluster grouping (sortForksByCluster), gatherDuplicateGroups runs
// per cluster block, so a group whose members land in two different clusters
// renders as two separate runs that happen to share a colour — ordinals is
// keyed by group across the whole list, not per fragment. This is deliberate:
// the two runs genuinely are the same group, just split by a cluster boundary
// that already breaks the visual line with a header row in between, so there
// is nothing for the colour to falsely claim continuity with.
//
// Always exactly one cell wide, or every column to the right would shift.
func duplicateGutter(sf ScoredFork, ordinals map[string]int) string {
	if sf.SiblingGroup == "" {
		return " "
	}
	return gutterStyleFor(ordinals[sf.SiblingGroup]).Render("┃")
}

// sortedGroupKeys is a deterministic helper for tests and for any consumer that
// needs a stable iteration order over duplicate groups.
func sortedGroupKeys(forks []ScoredFork) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, sf := range forks {
		if sf.SiblingGroup == "" {
			continue
		}
		if _, dup := seen[sf.SiblingGroup]; dup {
			continue
		}
		seen[sf.SiblingGroup] = struct{}{}
		out = append(out, sf.SiblingGroup)
	}
	sort.Strings(out)
	return out
}
