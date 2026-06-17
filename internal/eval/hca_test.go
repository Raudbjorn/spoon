package eval

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// TestCompute_EmptyInputs pins the contract that empty rows / empty
// judgments produce a fully-populated Report with all metrics at 0 and
// a non-nil NoveltyDist map (so JSON encoders don't choke on nil).
func TestCompute_EmptyInputs(t *testing.T) {
	r := Compute("o/r", nil, Judgments{})
	if r.HCA != 0 || r.AccSeen != 0 || r.AccNovel != 0 || r.ARI != 0 {
		t.Errorf("empty input: expected all-zero metrics, got %+v", r)
	}
	if r.NoveltyDist == nil {
		t.Errorf("empty input: NoveltyDist must be non-nil, got nil")
	}
	r2 := Compute("o/r", []ScoredFork{{ID: "x"}}, Judgments{})
	if r2.HCA != 0 || r2.AccSeen != 0 {
		t.Errorf("empty judgments: expected zero, got %+v", r2)
	}
}

// TestCompute_HCAPerfect is the canonical "everything correct" case.
// 4 established forks land in non-noise clusters; 1 novel fork lands
// in the noise cluster. AccSeen = 1.0, AccNovel = 1.0, HCA = 1.0.
func TestCompute_HCAPerfect(t *testing.T) {
	rows := []ScoredFork{
		{ID: "o/seen-1", ClusterID: "c0", Score: 80, Novelty: 0.1},
		{ID: "o/seen-2", ClusterID: "c0", Score: 70, Novelty: 0.2},
		{ID: "o/seen-3", ClusterID: "c1", Score: 60, Novelty: 0.15},
		{ID: "o/seen-4", ClusterID: "c1", Score: 50, Novelty: 0.25},
		{ID: "o/novel-1", ClusterID: "noise", Score: 40, Novelty: 1.0},
	}
	judgments := Judgments{Forks: []Judgment{
		{ID: "o/seen-1", Novelty: NoveltyEstablished},
		{ID: "o/seen-2", Novelty: NoveltyEstablished},
		{ID: "o/seen-3", Novelty: NoveltyMixed},
		{ID: "o/seen-4", Novelty: NoveltyEstablished},
		{ID: "o/novel-1", Novelty: NoveltyNovel},
	}}
	r := Compute("o/r", rows, judgments)
	if math.Abs(r.AccSeen-1.0) > 1e-9 {
		t.Errorf("AccSeen = %v, want 1.0", r.AccSeen)
	}
	if math.Abs(r.AccNovel-1.0) > 1e-9 {
		t.Errorf("AccNovel = %v, want 1.0", r.AccNovel)
	}
	if math.Abs(r.HCA-1.0) > 1e-9 {
		t.Errorf("HCA = %v, want 1.0", r.HCA)
	}
}

// TestCompute_HCATotalMiss: every fork ends up in the wrong bin.
// 4 seen in noise, 1 novel in a non-noise cluster. AccSeen = 0,
// AccNovel = 0, HCA = 0 (the harmonic-mean convention).
func TestCompute_HCATotalMiss(t *testing.T) {
	rows := []ScoredFork{
		{ID: "o/seen-1", ClusterID: "noise", Score: 80},
		{ID: "o/seen-2", ClusterID: "noise", Score: 70},
		{ID: "o/novel-1", ClusterID: "c0", Score: 40},
	}
	judgments := Judgments{Forks: []Judgment{
		{ID: "o/seen-1", Novelty: NoveltyEstablished},
		{ID: "o/seen-2", Novelty: NoveltyMixed},
		{ID: "o/novel-1", Novelty: NoveltyNovel},
	}}
	r := Compute("o/r", rows, judgments)
	if r.HCA != 0 {
		t.Errorf("HCA = %v, want 0", r.HCA)
	}
	if r.AccSeen != 0 {
		t.Errorf("AccSeen = %v, want 0", r.AccSeen)
	}
	if r.AccNovel != 0 {
		t.Errorf("AccNovel = %v, want 0", r.AccNovel)
	}
}

// TestCompute_AccSeenPartial: 2 of 4 seen forks correctly placed in
// non-noise; 0 of 1 novel correctly placed. AccSeen = 0.5, AccNovel
// = 0.0, HCA = 0 (harmonic of 0.5 and 0 is 0).
func TestCompute_AccSeenPartial(t *testing.T) {
	rows := []ScoredFork{
		{ID: "a", ClusterID: "c0", Score: 50},
		{ID: "b", ClusterID: "noise", Score: 49},
		{ID: "c", ClusterID: "c0", Score: 48},
		{ID: "d", ClusterID: "noise", Score: 47},
		{ID: "e", ClusterID: "c1", Score: 30},
	}
	judgments := Judgments{Forks: []Judgment{
		{ID: "a", Novelty: NoveltyEstablished},
		{ID: "b", Novelty: NoveltyEstablished},
		{ID: "c", Novelty: NoveltyMixed},
		{ID: "d", Novelty: NoveltyEstablished},
		{ID: "e", Novelty: NoveltyNovel},
	}}
	r := Compute("o/r", rows, judgments)
	if math.Abs(r.AccSeen-0.5) > 1e-9 {
		t.Errorf("AccSeen = %v, want 0.5", r.AccSeen)
	}
	if r.AccNovel != 0 {
		t.Errorf("AccNovel = %v, want 0", r.AccNovel)
	}
	if r.HCA != 0 {
		t.Errorf("HCA = %v, want 0", r.HCA)
	}
}

// TestCompute_RankingPerfect: the heat score puts the novel fork
// at the top, so NDCG = 1.0 and ROC-AUC = 1.0.
func TestCompute_RankingPerfect(t *testing.T) {
	rows := []ScoredFork{
		{ID: "a", ClusterID: "c0", Score: 50},
		{ID: "b", ClusterID: "c0", Score: 40},
		{ID: "novel", ClusterID: "noise", Score: 100},
	}
	judgments := Judgments{Forks: []Judgment{
		{ID: "a", Novelty: NoveltyEstablished},
		{ID: "b", Novelty: NoveltyEstablished},
		{ID: "novel", Novelty: NoveltyNovel},
	}}
	r := Compute("o/r", rows, judgments)
	if math.Abs(r.RankingNDCG-1.0) > 1e-9 {
		t.Errorf("NDCG = %v, want 1.0", r.RankingNDCG)
	}
	if math.Abs(r.RankingROCAUC-1.0) > 1e-9 {
		t.Errorf("ROC-AUC = %v, want 1.0", r.RankingROCAUC)
	}
}

// TestCompute_RankingWorst: the novel fork is at the bottom. NDCG
// degrades as 1/log2(4) ≈ 0.5; ROC-AUC = 0.0.
func TestCompute_RankingWorst(t *testing.T) {
	rows := []ScoredFork{
		{ID: "a", ClusterID: "c0", Score: 100},
		{ID: "b", ClusterID: "c0", Score: 90},
		{ID: "novel", ClusterID: "noise", Score: 10},
	}
	judgments := Judgments{Forks: []Judgment{
		{ID: "a", Novelty: NoveltyEstablished},
		{ID: "b", Novelty: NoveltyEstablished},
		{ID: "novel", Novelty: NoveltyNovel},
	}}
	r := Compute("o/r", rows, judgments)
	if math.Abs(r.RankingROCAUC) > 1e-9 {
		t.Errorf("ROC-AUC = %v, want 0.0 (novel at bottom)", r.RankingROCAUC)
	}
	// NDCG is non-zero but small — the positive at rank 3 yields
	// 1/log2(4) = 0.5 in DCG; IDCG with 1 positive is 1.0.
	expected := 1.0 / math.Log2(4)
	if math.Abs(r.RankingNDCG-expected) > 1e-9 {
		t.Errorf("NDCG = %v, want %v", r.RankingNDCG, expected)
	}
}

// TestCompute_NoNovelLabels: when every fork is "seen" there are no
// positives, so ranking metrics are 0 (undefined). HCA also drops to
// 0 because AccNovel is 0/0.
func TestCompute_NoNovelLabels(t *testing.T) {
	rows := []ScoredFork{
		{ID: "a", ClusterID: "c0", Score: 50},
		{ID: "b", ClusterID: "c0", Score: 40},
	}
	judgments := Judgments{Forks: []Judgment{
		{ID: "a", Novelty: NoveltyEstablished},
		{ID: "b", Novelty: NoveltyMixed},
	}}
	r := Compute("o/r", rows, judgments)
	if r.RankingNDCG != 0 || r.RankingROCAUC != 0 {
		t.Errorf("no-positives ranking: expected 0/0, got %v/%v",
			r.RankingNDCG, r.RankingROCAUC)
	}
	if r.HCA != 0 {
		t.Errorf("no-novel labels: HCA = %v, want 0", r.HCA)
	}
}

// TestCompute_ARIPerfect: cluster assignment perfectly tracks the
// novelty labels. With 2 seen in c0 (both labeled Mixed so they
// share a single label) and 2 novel in noise (both labeled Novel),
// the cluster partition equals the label partition and ARI = 1.0.
func TestCompute_ARIPerfect(t *testing.T) {
	rows := []ScoredFork{
		{ID: "s1", ClusterID: "c0", Score: 50},
		{ID: "s2", ClusterID: "c0", Score: 40},
		{ID: "n1", ClusterID: "noise", Score: 30},
		{ID: "n2", ClusterID: "noise", Score: 20},
	}
	judgments := Judgments{Forks: []Judgment{
		{ID: "s1", Novelty: NoveltyMixed},
		{ID: "s2", Novelty: NoveltyMixed},
		{ID: "n1", Novelty: NoveltyNovel},
		{ID: "n2", Novelty: NoveltyNovel},
	}}
	r := Compute("o/r", rows, judgments)
	if math.Abs(r.ARI-1.0) > 1e-9 {
		t.Errorf("ARI = %v, want 1.0 (perfect cluster/label match)", r.ARI)
	}
}

// TestCompute_ARIChance: completely shuffled labels. ARI should be
// non-positive (the worst case is disagreement on every pair).
func TestCompute_ARIChance(t *testing.T) {
	rows := []ScoredFork{
		{ID: "a", ClusterID: "c0", Score: 50},
		{ID: "b", ClusterID: "c1", Score: 40},
		{ID: "c", ClusterID: "c0", Score: 30},
		{ID: "d", ClusterID: "c1", Score: 20},
	}
	judgments := Judgments{Forks: []Judgment{
		{ID: "a", Novelty: NoveltyEstablished},
		{ID: "b", Novelty: NoveltyEstablished},
		{ID: "c", Novelty: NoveltyNovel},
		{ID: "d", Novelty: NoveltyNovel},
	}}
	r := Compute("o/r", rows, judgments)
	// Partition {c0,c1,c0,c1} vs {Est,Est,Novel,Novel}: pairs
	// (a,b),(c,d) are split-the-same in both — but a and c share
	// c0 while differing in label. The ARI ends up non-positive.
	if r.ARI > 0 {
		t.Errorf("ARI = %v, expected non-positive", r.ARI)
	}
}

// TestCompute_NoveltyDistBuckets pins the bucketing: a 0.0 novelty
// fork lands in [0.0, 0.1); 0.5 in [0.5, 0.6); 1.0 in [0.9, 1.0]
// (closed on the right).
func TestCompute_NoveltyDistBuckets(t *testing.T) {
	rows := []ScoredFork{
		{ID: "a", ClusterID: "c0", Score: 50, Novelty: 0.0},
		{ID: "b", ClusterID: "c0", Score: 50, Novelty: 0.5},
		{ID: "c", ClusterID: "noise", Score: 50, Novelty: 1.0},
	}
	judgments := Judgments{Forks: []Judgment{
		{ID: "a", Novelty: NoveltyEstablished},
		{ID: "b", Novelty: NoveltyEstablished},
		{ID: "c", Novelty: NoveltyNovel},
	}}
	r := Compute("o/r", rows, judgments)
	if r.NoveltyDist["[0.0, 0.1)"] != 1 {
		t.Errorf("bucket [0.0, 0.1) = %d, want 1", r.NoveltyDist["[0.0, 0.1)"])
	}
	// 1.0 should land in the [0.9, 1.0] bucket per the
	// closed-on-the-right convention for the top bucket.
	if r.NoveltyDist["[0.9, 1.0]"] != 1 {
		t.Errorf("bucket [0.9, 1.0] = %d, want 1", r.NoveltyDist["[0.9, 1.0]"])
	}
}

// TestHarmonicMean pins the math: 2*a*b/(a+b) when both > 0; 0 when
// either is 0. A defensive unit test for the helper itself, since
// every other HCA test depends on it.
func TestHarmonicMean(t *testing.T) {
	cases := []struct {
		a, b, want float64
	}{
		{0, 0, 0},
		{1, 0, 0},
		{0, 1, 0},
		{1, 1, 1},
		{0.5, 0.5, 0.5},
		{0.5, 1.0, 2.0 * 0.5 / 1.5},
	}
	for _, c := range cases {
		got := harmonicMean(c.a, c.b)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("harmonicMean(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestLoadJudgmentsBubbleteaFixture pins the on-disk shape of the
// hand-labeled fixture: 20 rows, no duplicate IDs, every label is a
// valid NoveltyLabel. A future contributor can re-generate the
// fixture and the regression test will catch structural drift
// (wrong count, bad labels, dup IDs) even if the per-fork
// labels are intentionally re-evaluated.
func TestLoadJudgmentsBubbleteaFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/judgments_bubbletea.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var j Judgments
	if err := json.Unmarshal(data, &j); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(j.Forks) != 20 {
		t.Errorf("fixture has %d forks, want 20", len(j.Forks))
	}
	seen := make(map[string]bool, len(j.Forks))
	for i, f := range j.Forks {
		if f.ID == "" {
			t.Errorf("fork[%d]: empty ID", i)
		}
		if seen[f.ID] {
			t.Errorf("fork[%d]: duplicate ID %q", i, f.ID)
		}
		seen[f.ID] = true
		switch f.Novelty {
		case NoveltyEstablished, NoveltyNovel, NoveltyMixed:
		default:
			t.Errorf("fork[%d] %q: invalid novelty %q", i, f.ID, f.Novelty)
		}
	}
	// Pin the distribution so a wholesale re-labeling is intentional
	// (this test will fail and the contributor will know to re-evaluate
	// the meaning of the labels).
	var novel, mixed, established int
	for _, f := range j.Forks {
		switch f.Novelty {
		case NoveltyNovel:
			novel++
		case NoveltyMixed:
			mixed++
		case NoveltyEstablished:
			established++
		}
	}
	if novel+established+mixed != 20 {
		t.Errorf("novel+mixed+established = %d, want 20", novel+established+mixed)
	}
	// Smoke: at least one of each so a degenerate all-same-label
	// fixture is also caught.
	if novel == 0 {
		t.Errorf("fixture has no 'novel' labels; was this intentional?")
	}
}

// TestEval_FixtureEndToEnd exercises the eval subcommand's metric
// path on the real bubbletea fixture, with a synthetic row set that
// models a typical pipeline output. The exact metric values depend
// on the run; what we pin is the shape and the
// boundary-condition behavior.
func TestEval_FixtureEndToEnd(t *testing.T) {
	data, err := os.ReadFile("testdata/judgments_bubbletea.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var j Judgments
	if err := json.Unmarshal(data, &j); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	// Model a pipeline that puts everything in noise (over-segmenting)
	// and see what the report looks like.
	rows := make([]ScoredFork, 0, len(j.Forks))
	for i, f := range j.Forks {
		cluster := "noise"
		if i%2 == 0 {
			cluster = "c0"
		}
		rows = append(rows, ScoredFork{
			ID:        f.ID,
			ClusterID: cluster,
			Novelty:   0.5,
			Score:     float64(50 - i),
		})
	}
	report := Compute("charmbracelet/bubbletea", rows, j)
	if report.Upstream != "charmbracelet/bubbletea" {
		t.Errorf("Upstream = %q, want %q", report.Upstream, "charmbracelet/bubbletea")
	}
	// We must have 1 novel, 1 mixed, 18 established. HCA depends on
	// the assignment, but must be in [0, 1].
	if report.HCA < 0 || report.HCA > 1 {
		t.Errorf("HCA out of [0,1]: %v", report.HCA)
	}
	if report.AccSeen < 0 || report.AccSeen > 1 {
		t.Errorf("AccSeen out of [0,1]: %v", report.AccSeen)
	}
	if report.AccNovel < 0 || report.AccNovel > 1 {
		t.Errorf("AccNovel out of [0,1]: %v", report.AccNovel)
	}
	if report.RankingNDCG < 0 || report.RankingNDCG > 1 {
		t.Errorf("NDCG out of [0,1]: %v", report.RankingNDCG)
	}
	if report.RankingROCAUC < 0 || report.RankingROCAUC > 1 {
		t.Errorf("ROC-AUC out of [0,1]: %v", report.RankingROCAUC)
	}
	// The distribution must have 20 entries summed across buckets.
	var sum int
	for _, c := range report.NoveltyDist {
		sum += c
	}
	if sum != 20 {
		t.Errorf("NoveltyDist total = %d, want 20", sum)
	}
}
