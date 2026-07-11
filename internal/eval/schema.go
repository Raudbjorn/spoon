// Package eval computes binary novelty, ARI, NDCG, and ROC-AUC metrics
// against a hand-labeled judgment file. It is the evaluation surface for
// structural-intelligence changes: every change to the cluster pipeline or
// empty-fork novelty demotion should be checked against the same judgment
// file so regressions surface as measurable metric changes rather than
// gut-feel diffs.
//
// The package is intentionally side-effect-free: callers load the judgment
// file, run the fork pipeline, hand the resulting HeatResult rows to Compute,
// and emit the Report as JSON. The spn CLI verb in cmd/spn/eval.go is a thin
// wrapper over this package.
package eval

// NoveltyLabel is the hand-assigned novelty bucket for a single fork.
// The three values mirror the GCD-for-code "seen / novel / mixed" split,
// applied to the fork-triage use case where "seen" ≈ "established or
// already-upstreamed work" and "novel" ≈ "diverged in a way that
// warrants human review".
type NoveltyLabel string

const (
	// NoveltyEstablished means the fork is a re-implementation of
	// upstream-known work, a stale mirror, or a backport. It is
	// expected to land in or near the upstream's main cluster.
	NoveltyEstablished NoveltyLabel = "established"

	// NoveltyNovel means the fork has diverged in a way the
	// upstream does not reflect: a new subsystem, a fresh approach
	// to a problem, or a meaningful design change. The cluster
	// pipeline should surface it as a singleton / noise point.
	NoveltyNovel NoveltyLabel = "novel"

	// NoveltyMixed means the judgment is genuinely ambiguous — the
	// fork both carries already-upstreamed work AND introduces
	// something new. It is treated as the negative class for binary
	// novelty metrics because the novel portion may not dominate.
	NoveltyMixed NoveltyLabel = "mixed"
)

// Judgment is one row of the hand-labeled fixture. ID matches the
// forge.T1Data.ID (GitHub: nameWithOwner; GitLab: fullPath). The
// labeler is expected to skim the fork's README and recent commit
// messages and pick a single label per fork.
type Judgment struct {
	ID      string       `json:"id"`
	Novelty NoveltyLabel `json:"novelty"`
}

// Judgments is the on-disk fixture shape. The file is committed to
// the repo and versioned alongside the eval code: changes to the
// judgment set are auditable as PR-time diffs.
type Judgments struct {
	Forks []Judgment `json:"forks"`
}

// Report is the eval output. Fields are deliberately all primitive
// (no nested structs) so consumers can pipe them straight into a
// pandas DataFrame or a SQL table without further parsing.
//
// Binary novelty treats a "novel" judgment as the positive class and
// "established" or "mixed" as the negative class. A noise or empty cluster
// assignment predicts novel; a non-noise assignment predicts non-novel.
// These are binary classification metrics, not canonical globally matched
// clustering accuracy (HCA).
//
//   - NoveltyPrecision: of forks predicted novel, the fraction labeled novel.
//   - NoveltyRecall: of forks labeled novel, the fraction predicted novel.
//   - NoveltyF1: harmonic mean of novelty precision and recall.
//   - BalancedAccuracy: mean of novelty recall and true-negative rate.
//   - ARI: Adjusted Rand Index between cluster labels and novelty labels.
//   - RankingNDCG / RankingROCAUC: ranking quality against novelty labels.
//   - NoveltyDist: histogram of NoveltyScore values in 0.1-wide buckets.
type Report struct {
	Upstream         string         `json:"upstream"`
	RankingNDCG      float64        `json:"rankingNDCG"`
	RankingROCAUC    float64        `json:"rankingROCAUC"`
	NoveltyPrecision float64        `json:"noveltyPrecision"`
	NoveltyRecall    float64        `json:"noveltyRecall"`
	NoveltyF1        float64        `json:"noveltyF1"`
	BalancedAccuracy float64        `json:"balancedAccuracy"`
	ARI              float64        `json:"ari"`
	NoveltyDist      map[string]int `json:"noveltyDist"`
}
