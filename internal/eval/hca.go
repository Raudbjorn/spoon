// Package eval implements binary novelty, ARI, and ranking metrics.
//
// The metrics here are intentionally small and self-contained. They take a
// flat list of judgments and scored cluster assignments and return a Report.
// There is no network, I/O, time, or concurrency, which keeps the computation
// deterministic and easy to reuse.
package eval

import (
	"fmt"
	"math"
	"sort"
)

// ScoredFork is the minimal view of a forked HeatResult that the
// metrics need. It is decoupled from internal/heat.HeatResult so the
// eval package does not import the rest of the codebase and stays
// trivial to test.
type ScoredFork struct {
	ID        string  // forge.T1Data.ID
	ClusterID string  // "c0", "c1", ..., "noise", or "" (clustering disabled)
	Novelty   float64 // 0..1
	Score     float64 // heat score, used for ranking metrics
}

// Compute runs the eval against a flat slice of (fork, judgment) pairs.
// The caller is responsible for joining the HeatResult rows to the
// judgments by T1Data.ID before calling Compute — the package does
// not own that join.
// The function is total: an empty `rows` or empty `judgments` returns
// a Report with all metrics at 0 and a non-nil but empty NoveltyDist
// map (allocated with capacity 10 so JSON encoders downstream don't
// blow up on a nil map). The map has no keys in the empty-input
// case; consumers should fill in zero counts client-side if they
// need a fixed 10-bucket output.
func Compute(upstream string, rows []ScoredFork, judgments Judgments) Report {
	report := Report{
		Upstream:    upstream,
		NoveltyDist: make(map[string]int, 10),
	}
	if len(rows) == 0 || len(judgments.Forks) == 0 {
		return report
	}

	// Index judgments by ID for O(1) lookup.
	jtmtByID := make(map[string]Judgment, len(judgments.Forks))
	for _, j := range judgments.Forks {
		jtmtByID[j.ID] = j
	}

	// Filter rows to those present in the judgment set; unmatched
	// rows are irrelevant for the labeled metrics.
	labeled := make([]ScoredFork, 0, len(judgments.Forks))
	for _, r := range rows {
		if _, ok := jtmtByID[r.ID]; ok {
			labeled = append(labeled, r)
		}
	}
	if len(labeled) == 0 {
		return report
	}

	// Binary novelty classification: positive means labeled novel, and a
	// noise or empty cluster assignment is the positive prediction.
	var tp, fp, fn, tn int
	for _, r := range labeled {
		j := jtmtByID[r.ID]
		isNovel := j.Novelty == NoveltyNovel
		isNoise := r.ClusterID == "noise" || r.ClusterID == ""

		if isNovel {
			if isNoise {
				tp++
			} else {
				fn++
			}
			continue
		}
		if isNoise {
			fp++
		} else {
			tn++
		}
	}

	report.NoveltyPrecision = safeRatio(tp, tp+fp)
	report.NoveltyRecall = safeRatio(tp, tp+fn)
	report.NoveltyF1 = harmonicMean(report.NoveltyPrecision, report.NoveltyRecall)
	specificity := safeRatio(tn, tn+fp)
	report.BalancedAccuracy = (report.NoveltyRecall + specificity) / 2

	// ARI: adjusted Rand index between cluster labels (group by
	// ClusterID) and label pseudo-clusters (group by Novelty).
	report.ARI = adjustedRandIndex(labeled, jtmtByID)

	// Ranking metrics: NDCG and ROC-AUC against the novelty labels.
	report.RankingNDCG, report.RankingROCAUC = rankingMetrics(labeled, jtmtByID)

	// NoveltyDist: 10 buckets in [0, 1] by 0.1.
	for _, r := range labeled {
		bucket := bucketKey(r.Novelty)
		report.NoveltyDist[bucket]++
	}

	return report
}

func safeRatio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func harmonicMean(a, b float64) float64 {
	if a == 0 || b == 0 {
		return 0
	}
	return 2 * a * b / (a + b)
}

// bucketKey returns the "[lo, hi)" bucket label for a novelty score
// in [0, 1]. The top bucket ([0.9, 1.0]) is closed on the right so
// a novelty of exactly 1.0 falls into it rather than overflowing.
func bucketKey(novelty float64) string {
	if novelty < 0 {
		novelty = 0
	}
	if novelty > 1 {
		novelty = 1
	}
	idx := int(math.Floor(novelty * 10))
	if idx < 0 {
		idx = 0
	}
	if idx > 9 {
		idx = 9
	}
	if idx == 9 {
		return fmt.Sprintf("[0.%d, 1.0]", idx)
	}
	return fmt.Sprintf("[0.%d, 0.%d)", idx, idx+1)
}

// adjustedRandIndex computes the ARI between cluster assignment
// (grouped by ClusterID) and label assignment (grouped by Novelty).
// Implementation follows the Hubert & Arabie 1985 definition:
//
//	ARI = (sum_ij C(n_ij,2) - E[RI]) / (max_RI - E[RI])
//
// where:
//   - sum_ij C(n_ij,2) is the number of pairs on which both
//     partitions agree (the RI numerator)
//   - E[RI] = sum_i C(a_i,2) * sum_j C(b_j,2) / C(n,2)
//   - max_RI = (sum_i C(a_i,2) + sum_j C(b_j,2)) / 2
//
// We compute pair counts directly without materializing the O(n^2)
// pair table. Returns 0 for n < 2 or when the two partitions are
// degenerate (all in one cluster AND all one label).
func adjustedRandIndex(rows []ScoredFork, jtmtByID map[string]Judgment) float64 {
	n := len(rows)
	if n < 2 {
		return 0
	}
	clusters := make([]string, 0, n)
	clusterIdx := map[string]int{}
	for _, r := range rows {
		if _, ok := clusterIdx[r.ClusterID]; !ok {
			clusterIdx[r.ClusterID] = len(clusters)
			clusters = append(clusters, r.ClusterID)
		}
	}
	labels := make([]NoveltyLabel, 0, 3)
	labelIdx := map[NoveltyLabel]int{}
	for _, r := range rows {
		j := jtmtByID[r.ID]
		if _, ok := labelIdx[j.Novelty]; !ok {
			labelIdx[j.Novelty] = len(labels)
			labels = append(labels, j.Novelty)
		}
	}
	contingency := make([][]int, len(clusters))
	rowSums := make([]int, len(clusters))
	colSums := make([]int, len(labels))
	for _, r := range rows {
		ci := clusterIdx[r.ClusterID]
		li := labelIdx[jtmtByID[r.ID].Novelty]
		if contingency[ci] == nil {
			contingency[ci] = make([]int, len(labels))
		}
		contingency[ci][li]++
		rowSums[ci]++
		colSums[li]++
	}
	var sumComb int64
	for _, row := range contingency {
		for _, c := range row {
			sumComb += int64(c) * int64(c-1) / 2
		}
	}
	var rowComb, colComb int64
	for _, a := range rowSums {
		rowComb += int64(a) * int64(a-1) / 2
	}
	for _, b := range colSums {
		colComb += int64(b) * int64(b-1) / 2
	}
	totalComb := int64(n) * int64(n-1) / 2
	if totalComb == 0 {
		return 0
	}
	expected := float64(rowComb) * float64(colComb) / float64(totalComb)
	maxIndex := 0.5 * (float64(rowComb) + float64(colComb))
	denom := maxIndex - expected
	if denom == 0 {
		return 0
	}
	return (float64(sumComb) - expected) / denom
}

// rankingMetrics computes NDCG@N and ROC-AUC against the novelty
// labels. The ranker is the heat Score (descending). N is total fork
// count. Returns (0, 0) when there are no positive labels.
func rankingMetrics(rows []ScoredFork, jtmtByID map[string]Judgment) (ndcg, auc float64) {
	positives := 0
	for _, r := range rows {
		if jtmtByID[r.ID].Novelty == NoveltyNovel {
			positives++
		}
	}
	if positives == 0 || positives == len(rows) {
		return 0, 0
	}

	sorted := make([]ScoredFork, len(rows))
	copy(sorted, rows)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Score > sorted[j].Score
	})

	var dcg, idcg float64
	for i, r := range sorted {
		rel := 0.0
		if jtmtByID[r.ID].Novelty == NoveltyNovel {
			rel = 1.0
		}
		dcg += rel / math.Log2(float64(i)+2)
	}
	for i := 0; i < positives; i++ {
		idcg += 1.0 / math.Log2(float64(i)+2)
	}
	if idcg > 0 {
		ndcg = dcg / idcg
	}

	var pairScore float64
	var numPairs int
	for _, pos := range rows {
		if jtmtByID[pos.ID].Novelty != NoveltyNovel {
			continue
		}
		for _, neg := range rows {
			if jtmtByID[neg.ID].Novelty == NoveltyNovel {
				continue
			}
			numPairs++
			switch {
			case pos.Score > neg.Score:
				pairScore++
			case pos.Score == neg.Score:
				pairScore += 0.5
			}
		}
	}
	if numPairs > 0 {
		auc = pairScore / float64(numPairs)
	}
	return ndcg, auc
}
