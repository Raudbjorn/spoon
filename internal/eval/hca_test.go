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
	if r.NoveltyPrecision != 0 || r.NoveltyRecall != 0 || r.NoveltyF1 != 0 ||
		r.BalancedAccuracy != 0 || r.ARI != 0 {
		t.Errorf("empty input: expected all-zero metrics, got %+v", r)
	}
	if r.NoveltyDist == nil {
		t.Error("empty input: NoveltyDist must be non-nil")
	}
	r2 := Compute("o/r", []ScoredFork{{ID: "x"}}, Judgments{})
	if r2.NoveltyPrecision != 0 || r2.NoveltyRecall != 0 || r2.NoveltyF1 != 0 {
		t.Errorf("empty judgments: expected zero, got %+v", r2)
	}
}

// TestCompute_BinaryNoveltyPerfect is the canonical all-correct case.
func TestCompute_BinaryNoveltyPerfect(t *testing.T) {
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
	if math.Abs(r.NoveltyPrecision-1) > 1e-9 ||
		math.Abs(r.NoveltyRecall-1) > 1e-9 ||
		math.Abs(r.NoveltyF1-1) > 1e-9 ||
		math.Abs(r.BalancedAccuracy-1) > 1e-9 {
		t.Errorf("perfect classification: got %+v", r)
	}
}

// TestCompute_BinaryNoveltyTotalMiss puts every fork in the wrong binary class.
func TestCompute_BinaryNoveltyTotalMiss(t *testing.T) {
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
	if r.NoveltyPrecision != 0 || r.NoveltyRecall != 0 ||
		r.NoveltyF1 != 0 || r.BalancedAccuracy != 0 {
		t.Errorf("total miss: expected zero metrics, got %+v", r)
	}
}

// TestCompute_BinaryNoveltyPartial has half the negatives correct and no
// positives correct, yielding balanced accuracy 0.25.
func TestCompute_BinaryNoveltyPartial(t *testing.T) {
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
	if r.NoveltyPrecision != 0 || r.NoveltyRecall != 0 || r.NoveltyF1 != 0 {
		t.Errorf("partial classification: expected zero P/R/F1, got %+v", r)
	}
	if math.Abs(r.BalancedAccuracy-0.25) > 1e-9 {
		t.Errorf("BalancedAccuracy = %v, want 0.25", r.BalancedAccuracy)
	}
}

func TestCompute_BinaryNovelty_Collapse(t *testing.T) {
	rows := []ScoredFork{
		{ID: "o/novel1", ClusterID: "c0", Novelty: 1.0, Score: 90},
		{ID: "o/novel2", ClusterID: "c0", Novelty: 1.0, Score: 85},
		{ID: "o/est1", ClusterID: "c0", Novelty: 0.2, Score: 80},
		{ID: "o/est2", ClusterID: "c0", Novelty: 0.3, Score: 75},
	}
	judgments := Judgments{Forks: []Judgment{
		{ID: "o/novel1", Novelty: NoveltyNovel},
		{ID: "o/novel2", Novelty: NoveltyNovel},
		{ID: "o/est1", Novelty: NoveltyEstablished},
		{ID: "o/est2", Novelty: NoveltyEstablished},
	}}
	r := Compute("o/r", rows, judgments)
	if r.NoveltyPrecision != 0 || r.NoveltyRecall != 0 || r.NoveltyF1 != 0 {
		t.Errorf("collapsed classification: expected zero P/R/F1, got %+v", r)
	}
	if r.BalancedAccuracy != 0.5 {
		t.Errorf("BalancedAccuracy = %.3f, want 0.5", r.BalancedAccuracy)
	}
}

func TestCompute_BinaryNovelty_AllNoise(t *testing.T) {
	rows := []ScoredFork{
		{ID: "o/novel1", ClusterID: "noise", Novelty: 1.0, Score: 90},
		{ID: "o/novel2", ClusterID: "noise", Novelty: 1.0, Score: 85},
		{ID: "o/est1", ClusterID: "noise", Novelty: 1.0, Score: 80},
		{ID: "o/est2", ClusterID: "noise", Novelty: 1.0, Score: 75},
	}
	judgments := Judgments{Forks: []Judgment{
		{ID: "o/novel1", Novelty: NoveltyNovel},
		{ID: "o/novel2", Novelty: NoveltyNovel},
		{ID: "o/est1", Novelty: NoveltyEstablished},
		{ID: "o/est2", Novelty: NoveltyEstablished},
	}}
	r := Compute("o/r", rows, judgments)
	if r.NoveltyPrecision != 0.5 {
		t.Errorf("NoveltyPrecision = %.3f, want 0.5", r.NoveltyPrecision)
	}
	if r.NoveltyRecall != 1 {
		t.Errorf("NoveltyRecall = %.3f, want 1.0", r.NoveltyRecall)
	}
	expectedF1 := 2 * 0.5 / 1.5
	if math.Abs(r.NoveltyF1-expectedF1) > 1e-9 {
		t.Errorf("NoveltyF1 = %.3f, want %.3f", r.NoveltyF1, expectedF1)
	}
	if r.BalancedAccuracy != 0.5 {
		t.Errorf("BalancedAccuracy = %.3f, want 0.5", r.BalancedAccuracy)
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

// TestCompute_NoNovelLabels: when every fork is non-novel there are no
// positives, so ranking and positive-class metrics are 0 (undefined).
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
	if r.NoveltyPrecision != 0 || r.NoveltyRecall != 0 || r.NoveltyF1 != 0 {
		t.Errorf("no-novel labels: expected zero P/R/F1, got %+v", r)
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

// TestHarmonicMean pins the F1 helper math: 2*a*b/(a+b) when both
// inputs are positive, and 0 when either is 0.
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
	// Every binary novelty metric must remain in [0, 1].
	metrics := map[string]float64{
		"NoveltyPrecision": report.NoveltyPrecision,
		"NoveltyRecall":    report.NoveltyRecall,
		"NoveltyF1":        report.NoveltyF1,
		"BalancedAccuracy": report.BalancedAccuracy,
	}
	for name, metric := range metrics {
		if metric < 0 || metric > 1 {
			t.Errorf("%s out of [0,1]: %v", name, metric)
		}
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

func TestReportJSONUsesBinaryNoveltyNames(t *testing.T) {
	data, err := json.Marshal(Report{})
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	for _, name := range []string{
		"noveltyPrecision", "noveltyRecall", "noveltyF1", "balancedAccuracy",
	} {
		if _, ok := fields[name]; !ok {
			t.Errorf("missing JSON field %q in %s", name, data)
		}
	}
	for _, name := range []string{"hca", "accSeen", "accNovel"} {
		if _, ok := fields[name]; ok {
			t.Errorf("obsolete JSON field %q present in %s", name, data)
		}
	}
}
