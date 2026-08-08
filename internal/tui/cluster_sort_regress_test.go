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
