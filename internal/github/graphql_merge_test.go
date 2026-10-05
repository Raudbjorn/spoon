package github

import "testing"

// The GraphQL partial usually has spare capacity after paginated appends. An
// append-based merge then aliases it, the in-place stars sort reorders the
// partial, and extras (parallel to the unsorted partial) attach to the wrong
// fork. REST rows with more stars than the GraphQL ones force a real reorder.
func TestMergePartialWithREST_ExtrasStayWithTheirFork(t *testing.T) {
	forks := make([]ForkInfo, 0, 8) // spare capacity: aliasing is reachable
	extras := make([]T1Extra, 0, 8)
	for i, id := range []int64{1, 2, 3} {
		forks = append(forks, ForkInfo{ID: id, Stars: 10 - i}) // 10, 9, 8
		extras = append(extras, T1Extra{DefaultTipSHA: "tip-" + string(rune('a'+i))})
	}
	rest := []ForkInfo{{ID: 4, Stars: 100}, {ID: 5, Stars: 50}, {ID: 2, Stars: 9}}
	streamed := map[int64]struct{}{1: {}, 2: {}, 3: {}}

	merged, byID := mergePartialWithREST(forks, extras, rest, streamed)

	if len(merged) != 5 {
		t.Fatalf("merged %d forks, want 5 (repeated REST row dropped)", len(merged))
	}
	for i := 1; i < len(merged); i++ {
		if merged[i-1].Stars < merged[i].Stars {
			t.Fatalf("merged not stars-sorted at %d: %d then %d", i, merged[i-1].Stars, merged[i].Stars)
		}
	}
	want := map[int64]string{1: "tip-a", 2: "tip-b", 3: "tip-c"}
	if len(byID) != len(want) {
		t.Fatalf("extras for %d forks, want %d (REST-only rows carry none)", len(byID), len(want))
	}
	for id, tip := range want {
		if byID[id].DefaultTipSHA != tip {
			t.Errorf("fork %d extras tip = %q, want %q", id, byID[id].DefaultTipSHA, tip)
		}
	}
	// The partial the caller passed in must not have been reordered.
	for i, id := range []int64{1, 2, 3} {
		if forks[i].ID != id {
			t.Errorf("input forks[%d].ID = %d, want %d (caller's slice was mutated)", i, forks[i].ID, id)
		}
	}
}
