// Package eval computes clustering quality metrics (HCA, ARI, NDCG,
// ROC-AUC) against a hand-labeled judgment file. It is the evaluation
// surface for the GCD-for-code line of work: every change to the
// cluster pipeline or the empty-fork novelty demotion should be checked
// against the same judgment file so that regressions surface as
// measurable drops in HCA or ARI, not as gut-feel diffs.
//
// The package is intentionally side-effect-free: callers load the
// judgment file, run the fork pipeline, hand the resulting HeatResult
// rows to Compute, and emit the Report as JSON. The spn CLI verb in
// cmd/spn/eval.go is a thin wrapper over this package.
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
	// something new. Treated as "seen" for HCA bookkeeping
	// (since the cluster pipeline is unlikely to surface it as
	// noise unless the novel portion dominates).
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
//   - Upstream: the owner/repo this report was generated against.
//   - RankingNDCG / RankingROCAUC: standard ranking-quality metrics
//     computed against the labeled set; useful for tracking drift in
//     the ranker over time even when cluster assignment is unchanged.
//   - HCA: the headline metric — harmonic mean of AccSeen and
//     AccNovel, both in [0, 1]. 1.0 means every labeled fork was
//     ranked correctly. 0.0 means total miss.
//   - AccSeen / AccNovel: the two components of HCA. AccSeen is the
//     fraction of "established" / "mixed" forks placed in their
//     assigned cluster (or any non-noise cluster) by the pipeline.
//     AccNovel is the fraction of "novel" forks placed in the noise
//     cluster. A pipeline that collapses everyone to noise will
//     score AccNovel=1.0 / AccSeen=0.0 / HCA=0.0 — a clear signal
//     that it is over-segmenting.
//   - ARI: Adjusted Rand Index between the cluster labels and a
//     "label" pseudo-cluster derived from the novelty judgments.
//     Adapted from the standard ARI formula for K vs K partitionings;
//     1.0 is perfect agreement, 0.0 is chance.
//   - NoveltyDist: histogram of NoveltyScore values in
//     [0, 0.1), [0.1, 0.2), ..., [0.9, 1.0]. Useful for
//     distribution-shift detection across runs.
type Report struct {
	Upstream      string         `json:"upstream"`
	RankingNDCG   float64        `json:"rankingNDCG"`
	RankingROCAUC float64        `json:"rankingROCAUC"`
	HCA           float64        `json:"hca"`
	AccSeen       float64        `json:"accSeen"`
	AccNovel      float64        `json:"accNovel"`
	ARI           float64        `json:"ari"`
	NoveltyDist   map[string]int `json:"noveltyDist"`
}
