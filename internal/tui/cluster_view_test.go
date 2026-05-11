package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// makeSF builds a ScoredFork wired with the cluster metadata the cluster
// view tests rely on. Pushed time is set to the same recent moment for
// every fork so relativeTimeSince() output stays deterministic.
func makeSF(id string, score float64, clusterID, clusterLabel string, members int) ScoredFork {
	return ScoredFork{
		Fork: forge.T1Data{
			ID:            id,
			Owner:         strings.SplitN(id, "/", 2)[0],
			URL:           "https://example.com/" + id,
			DefaultBranch: "main",
			Stars:         1,
			PushedAt:      time.Now().Add(-1 * time.Hour),
		},
		Heat: heat.HeatResult{
			Score:              score,
			ClusterID:          clusterID,
			ClusterLabel:       clusterLabel,
			ClusterMemberCount: members,
		},
	}
}

// newClusterTestModel returns a Model with parent set and the given
// forks pre-loaded so the table renderer's nil/empty guards do not
// short-circuit during tests.
func newClusterTestModel(forks []ScoredFork) *Model {
	m := Model{
		view:        viewTable,
		clusterMsgs: make(chan tea.Msg, 8),
		parent: &forge.ParentData{
			FullName:      "owner/repo",
			DefaultBranch: "main",
			PushedAt:      time.Now().Add(-24 * time.Hour),
		},
		forks:  forks,
		width:  100,
		height: 40,
	}
	return &m
}

func TestHandleTableKey_GTogglesClusterGrouping(t *testing.T) {
	forks := []ScoredFork{
		makeSF("a/one", 80, "c0", "label-a", 2),
		makeSF("b/two", 70, "c0", "label-a", 2),
		makeSF("c/three", 60, "c1", "label-b", 1),
	}
	m := newClusterTestModel(forks)
	if m.groupByCluster {
		t.Fatal("default groupByCluster must be false")
	}
	_, _ = m.handleTableKey("g")
	if !m.groupByCluster {
		t.Fatal("after first 'g' press, groupByCluster must be true")
	}
	_, _ = m.handleTableKey("g")
	if m.groupByCluster {
		t.Fatal("after second 'g' press, groupByCluster must be false")
	}
}

func TestHandleTableKey_GWithoutClusterDataStillFlips(t *testing.T) {
	// No fork carries a ClusterID, so hasClusterData() returns false.
	// The toggle still flips (so tests can observe state) and renders
	// fall back to flat without crashing.
	forks := []ScoredFork{
		makeSF("a/one", 80, "", "", 0),
		makeSF("b/two", 70, "", "", 0),
	}
	m := newClusterTestModel(forks)
	_, _ = m.handleTableKey("g")
	if !m.groupByCluster {
		t.Fatal("toggle should flip even without cluster data")
	}
	// Render with grouping enabled — must not panic and must produce
	// some output (i.e. fall back to flat-ish rendering).
	out := m.viewTable()
	if out == "" {
		t.Fatal("viewTable returned empty string after toggle on empty cluster data")
	}
	// Footer should reflect the "no clusters available" hint.
	if m.errMsg != "no clusters available" {
		t.Fatalf("errMsg = %q; want %q", m.errMsg, "no clusters available")
	}
}

func TestGroupedTableRendersClusterHeaders(t *testing.T) {
	forks := []ScoredFork{
		makeSF("a/one", 80, "c0", "frontend", 2),
		makeSF("b/two", 70, "c0", "frontend", 2),
		makeSF("c/three", 60, "c1", "backend", 2),
		makeSF("d/four", 50, "c1", "backend", 2),
		makeSF("e/five", 40, "noise", "", 1),
	}
	m := newClusterTestModel(forks)
	m.groupByCluster = true
	m.sortForksByCluster()

	out := m.viewTable()

	// 3 header rows expected: c0:frontend, c1:backend, noise.
	expected := []string{
		"c0: frontend (2 members)",
		"c1: backend (2 members)",
		"noise (1 members)",
	}
	for _, want := range expected {
		if !strings.Contains(out, want) {
			t.Errorf("output missing header substring %q\n---\n%s\n---", want, out)
		}
	}

	// Order check: c0 before c1 before noise.
	idxC0 := strings.Index(out, "c0: frontend")
	idxC1 := strings.Index(out, "c1: backend")
	idxNoise := strings.Index(out, "noise (1 members)")
	if !(idxC0 < idxC1 && idxC1 < idxNoise) {
		t.Errorf("header order wrong: c0=%d c1=%d noise=%d", idxC0, idxC1, idxNoise)
	}
}

func TestSortForksByCluster_NoiseAndEmptyLast(t *testing.T) {
	forks := []ScoredFork{
		makeSF("a/one", 50, "noise", "", 1),
		makeSF("b/two", 80, "c1", "two", 1),
		makeSF("c/three", 90, "c0", "one", 2),
		makeSF("d/four", 60, "", "", 0),
		makeSF("e/five", 85, "c0", "one", 2),
	}
	m := newClusterTestModel(forks)
	m.sortForksByCluster()
	gotIDs := make([]string, len(m.forks))
	for i, sf := range m.forks {
		gotIDs[i] = sf.Fork.ID
	}
	// c0 first (heat 90 then 85), then c1, then "", then noise.
	want := []string{"c/three", "e/five", "b/two", "d/four", "a/one"}
	for i, id := range want {
		if gotIDs[i] != id {
			t.Errorf("position %d: got %s, want %s (full=%v)", i, gotIDs[i], id, gotIDs)
		}
	}
}

func TestDetailView_ClusterBlockShowsSiblings_Cluster6(t *testing.T) {
	// A cluster of 6 — selected fork is one of them — expect 5 siblings
	// shown plus "... and 0 more" should NOT appear.
	forks := make([]ScoredFork, 6)
	for i := range forks {
		id := []string{"a/one", "b/two", "c/three", "d/four", "e/five", "f/six"}[i]
		forks[i] = makeSF(id, float64(90-i), "c0", "cluster-label", 6)
	}
	m := newClusterTestModel(forks)
	m.cursor = 0 // a/one

	out := m.viewDetail()

	if !strings.Contains(out, "Siblings (5):") {
		t.Errorf("expected 'Siblings (5):' in output\n%s", out)
	}
	// All 5 other forks should be listed.
	for _, sib := range []string{"b/two", "c/three", "d/four", "e/five", "f/six"} {
		if !strings.Contains(out, sib) {
			t.Errorf("expected sibling %q in detail output", sib)
		}
	}
	// 5 shown out of 5 → no "and N more" line should appear.
	if strings.Contains(out, "... and") {
		t.Errorf("did not expect '... and N more' for cluster of 6 (5 siblings exactly)\n%s", out)
	}
}

func TestDetailView_ClusterBlockShowsSiblings_Cluster8(t *testing.T) {
	// Cluster of 8 — expect 5 listed + "... and 2 more".
	ids := []string{"a/1", "b/2", "c/3", "d/4", "e/5", "f/6", "g/7", "h/8"}
	forks := make([]ScoredFork, 8)
	for i, id := range ids {
		forks[i] = makeSF(id, float64(100-i*5), "c0", "big-cluster", 8)
	}
	m := newClusterTestModel(forks)
	m.cursor = 0

	out := m.viewDetail()

	if !strings.Contains(out, "Siblings (7):") {
		t.Errorf("expected 'Siblings (7):' header\n%s", out)
	}
	if !strings.Contains(out, "... and 2 more") {
		t.Errorf("expected '... and 2 more' line\n%s", out)
	}
	// First 5 (sorted by Heat desc) should appear; last 2 (g/7, h/8) should not.
	for _, sib := range []string{"b/2", "c/3", "d/4", "e/5", "f/6"} {
		if !strings.Contains(out, sib) {
			t.Errorf("expected sibling %q in detail output\n%s", sib, out)
		}
	}
}

func TestDetailView_NoiseClusterRendersMinimal(t *testing.T) {
	forks := []ScoredFork{
		makeSF("a/one", 50, "noise", "", 1),
		makeSF("b/two", 40, "noise", "", 1),
	}
	m := newClusterTestModel(forks)
	m.cursor = 0

	out := m.viewDetail()

	if !strings.Contains(out, "Cluster: noise") {
		t.Errorf("expected 'Cluster: noise' line\n%s", out)
	}
	if strings.Contains(out, "Siblings") {
		t.Errorf("noise cluster must NOT render siblings list\n%s", out)
	}
	if strings.Contains(out, "members)") {
		t.Errorf("noise cluster must NOT show member-count\n%s", out)
	}
}

func TestRenderClusterHeader_Variants(t *testing.T) {
	forks := []ScoredFork{
		makeSF("a/one", 80, "c0", "frontend", 2),
		makeSF("b/two", 70, "c0", "frontend", 2),
		makeSF("c/three", 60, "noise", "", 1),
		makeSF("d/four", 50, "", "", 0),
	}
	m := newClusterTestModel(forks)

	if got := m.renderClusterHeader("c0"); !strings.Contains(got, "c0: frontend (2 members)") {
		t.Errorf("c0 header missing expected substring: %q", got)
	}
	if got := m.renderClusterHeader("noise"); !strings.Contains(got, "noise (1 members)") {
		t.Errorf("noise header: %q", got)
	}
	if got := m.renderClusterHeader(""); !strings.Contains(got, "(ungrouped)") {
		t.Errorf("empty cluster ID header: %q", got)
	}
}

func TestClusterGroupRank_Ordering(t *testing.T) {
	type kv struct {
		id   string
		rank int
	}
	cases := []kv{
		{"c0", 0},
		{"c1", 0},
		{"", 1},
		{"noise", 2},
	}
	for _, c := range cases {
		r, _ := clusterGroupRank(c.id)
		if r != c.rank {
			t.Errorf("clusterGroupRank(%q) rank = %d; want %d", c.id, r, c.rank)
		}
	}
}

func TestToggleGroupByCluster_PreservesSelection(t *testing.T) {
	// Forks ordered by heat: a, b, c, d. b is in a different cluster
	// from a. After toggling on grouping, the cursor should still point
	// at the same fork (a/one) even though its row index may shift.
	forks := []ScoredFork{
		makeSF("a/one", 90, "c1", "two", 1),
		makeSF("b/two", 80, "c0", "one", 2),
		makeSF("c/three", 70, "c0", "one", 2),
		makeSF("d/four", 60, "noise", "", 1),
	}
	m := newClusterTestModel(forks)
	m.cursor = 0 // a/one (in c1, heat 90)

	m.toggleGroupByCluster()

	// After grouping, c0 comes first. a/one should now be at index 2
	// (after b/two, c/three) — but the cursor must have been re-anchored
	// so m.forks[m.cursor].Fork.ID == "a/one".
	if m.cursor < 0 || m.cursor >= len(m.forks) {
		t.Fatalf("cursor out of bounds: %d", m.cursor)
	}
	if m.forks[m.cursor].Fork.ID != "a/one" {
		t.Errorf("cursor moved off original fork: now points to %s",
			m.forks[m.cursor].Fork.ID)
	}
}
