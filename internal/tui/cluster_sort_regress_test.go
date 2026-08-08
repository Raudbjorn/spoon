package tui

import (
	"testing"
)

// clusterBlocks returns the sequence of cluster IDs as they appear in row
// order, with consecutive repeats collapsed — "c0,c1,c0" means cluster c0 was
// split into two runs.
func clusterBlocks(m *Model) []string {
	var out []string
	for _, sf := range m.forks {
		id := sf.Heat.ClusterID
		if len(out) == 0 || out[len(out)-1] != id {
			out = append(out, id)
		}
	}
	return out
}

func assertClustersContiguous(t *testing.T, m *Model) {
	t.Helper()
	seen := make(map[string]bool)
	for _, id := range clusterBlocks(m) {
		if seen[id] {
			t.Fatalf("cluster %q split into multiple runs; row order: %v", id, clusterBlocks(m))
		}
		seen[id] = true
	}
}

// Several call sites used to invoke sortForks() directly instead of
// reapplySort(). With cluster grouping toggled on, each of them flat-sorted
// the list while the view still drew cluster headers — stapling a header onto
// nearly every row. Heats here are chosen so a flat heat sort interleaves the
// two clusters (80 c0, 70 c1, 60 c0); only the cluster-aware sort keeps the
// blocks whole.

func TestCycleSortColumn_RespectsClusterGrouping(t *testing.T) {
	m := newClusterTestModel([]ScoredFork{
		makeSF("a/one", 80, "c0", "label-a", 2),
		makeSF("c/three", 70, "c1", "label-b", 1),
		makeSF("b/two", 60, "c0", "label-a", 2),
	})
	m.groupByCluster = true
	m.sortCol = "heat"

	m.cycleSortColumn()

	assertClustersContiguous(t, m)
}

func TestSortDirectionToggle_RespectsClusterGrouping(t *testing.T) {
	m := newClusterTestModel([]ScoredFork{
		makeSF("a/one", 80, "c0", "label-a", 2),
		makeSF("c/three", 70, "c1", "label-b", 1),
		makeSF("b/two", 60, "c0", "label-a", 2),
	})
	m.groupByCluster = true
	m.sortCol = "heat"

	m.handleTableKey("S")

	assertClustersContiguous(t, m)
}

func TestHandleBranchDivergence_RespectsClusterGrouping(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	m := newClusterTestModel([]ScoredFork{
		makeSF("a/one", 80, "c0", "label-a", 2),
		makeSF("c/three", 70, "c1", "label-b", 1),
		makeSF("b/two", 60, "c0", "label-a", 2),
	})
	m.groupByCluster = true
	m.sortCol = "heat"

	m.handleBranchDivergence(branchDivergenceMsg{
		counts: map[string]int{"a/one": 1},
	})

	assertClustersContiguous(t, m)
}

// The header renders ▲/▼ from sortCol/sortAsc whether or not cluster grouping
// is on, so the within-cluster ordering must honor them — a hard-coded
// heat-descending comparator made the indicator claim a sort the rows did not
// have, and s/S silently did nothing in grouped mode.
func TestSortForksByCluster_HonorsActiveComparator(t *testing.T) {
	mk := func(id, cluster string, stars int, score float64) ScoredFork {
		sf := makeSF(id, score, cluster, "label-"+cluster, 3)
		sf.Fork.Stars = stars
		return sf
	}
	fresh := func() *Model {
		m := newClusterTestModel([]ScoredFork{
			mk("a/one", "c0", 5, 80),
			mk("b/two", "c0", 1, 70),
			mk("c/three", "c0", 3, 60),
			mk("d/four", "c1", 2, 50),
			mk("e/five", "c1", 4, 40),
		})
		m.groupByCluster = true
		return m
	}

	starsOf := func(m *Model) []int {
		out := make([]int, len(m.forks))
		for i, sf := range m.forks {
			out[i] = sf.Fork.Stars
		}
		return out
	}

	m := fresh()
	m.sortCol = "stars"
	m.sortAsc = true
	m.sortForksByCluster()
	assertClustersContiguous(t, m)
	if got, want := starsOf(m), []int{1, 3, 5, 2, 4}; !slicesEqual(got, want) {
		t.Errorf("stars ascending within clusters = %v, want %v", got, want)
	}

	// Flipping the direction must actually reorder the blocks.
	m.sortAsc = false
	m.sortForksByCluster()
	assertClustersContiguous(t, m)
	if got, want := starsOf(m), []int{5, 3, 1, 4, 2}; !slicesEqual(got, want) {
		t.Errorf("stars descending within clusters = %v, want %v", got, want)
	}

	// The default (heat, descending) ordering is unchanged from the old
	// hard-coded comparator.
	m = fresh()
	m.sortCol = "heat"
	m.sortAsc = false
	m.sortForksByCluster()
	assertClustersContiguous(t, m)
	if got, want := starsOf(m), []int{5, 1, 3, 2, 4}; !slicesEqual(got, want) {
		t.Errorf("heat descending within clusters = %v (by stars), want %v", got, want)
	}
}

func slicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHandleClusterResult_RespectsClusterGrouping(t *testing.T) {
	m := newClusterTestModel([]ScoredFork{
		makeSF("a/one", 80, "c0", "label-a", 2),
		makeSF("c/three", 70, "c1", "label-b", 1),
		makeSF("b/two", 60, "c0", "label-a", 2),
	})
	m.groupByCluster = true
	m.sortCol = "heat"

	m.handleClusterResult(clusterResultMsg{})

	assertClustersContiguous(t, m)
}
