// Package forksops provides a library-callable streaming fork-discovery pipeline.
// It is the read-path for "spn forks list" NDJSON output.
// The existing internal/dump package is NOT modified by this package.
package forksops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/pathmatch"
	"github.com/svnbjrn/spoon/internal/priors"
)

// Options controls the streaming pipeline.
type Options struct {
	Refresh      bool
	Tier         int // 1 = surface only, 2 = + compare, 3 = + contributors. 0 = full (3).
	TopN         int
	BotAllowlist map[string]bool
	HeatWeights  map[string]float64

	// Now is an optional run clock. Nil uses time.Now. Callers that need
	// reproducible stream records may supply one fixed instant.
	Now func() time.Time

	// Budget caps how many forks get the expensive compare (T2) / contributors
	// (T3) calls. When > 0, the forks to spend on are chosen by an
	// optimal-stopping ("secretary problem") gate over a cheap divergence
	// signal, instead of the top-TopN-by-surface-score slice — so the budget
	// lands on forks that likely diverged, not the most popular ones. 0 = no
	// cap (the TopN / full-tier behavior applies).
	Budget int

	// CommitFiles enriches each ahead commit with its changed files. It forces
	// collect-then-emit semantics so one global budget can be applied by heat.
	CommitFiles      bool
	CommitFileBudget int

	// CommitFileRunBudget, when non-nil, is a run-scoped remaining counter shared
	// across every Stream call in one invocation. Topic mode calls Stream once
	// per selected repo, so a per-call counter would grant each repo a fresh
	// budget and issue up to N x the documented cap; a shared counter makes the
	// budget span the whole run. When nil, the per-call CommitFileBudget applies.
	CommitFileRunBudget *atomic.Int64

	// ShortlistN, when > 0, buffers all results, computes each fork's Robbins
	// expected rank (with confidence) over the enriched set, and emits only the
	// top-N by expected rank. Forces collect-then-emit semantics.
	ShortlistN int
	// ShortlistRule selects which forks make the shortlist: ShortlistRuleExpected
	// (default, top-N by expected rank) or ShortlistRuleMembership (top-N by
	// P(rank ≤ N), then ordered by expected rank). Empty means expected.
	ShortlistRule string
	// RankReport, when non-nil, receives the pool-level rank summary (pool
	// size, POTH, cPOTH_k) after the channel closes. Only filled when
	// ShortlistN > 0.
	RankReport *RankReport
	// RankDiagnostics additionally computes each ranked fork's POTH residual
	// (RankStats.PothResidual). Requires ShortlistN > 0.
	RankDiagnostics bool
	// EB, when true, fits the empirical-Bayes normal–normal model over the
	// ranked forks with heat > 0 and ranks on the shrunken score and
	// posterior sigma instead of raw heat and the tier sigma. Requires
	// ShortlistN > 0. Zero-heat forks are excluded from the fit and left
	// unshrunk; pools below ebMinPool or with τ̂² = 0 leave ranking unchanged
	// and report the regime.
	EB bool
	// PriorScale is the half-normal scale on τ for EB (caps τ̂ at 2×scale);
	// 0 selects the robust MAD-based default.
	PriorScale float64
	// RankKeepAll returns the whole ranked pool (ordered by the shortlist
	// rule) instead of cutting it to ShortlistN; ShortlistN still sets k for
	// P(rank ≤ k) and cPOTH_k. Used by offline evaluation.
	RankKeepAll bool

	// Query, when non-empty, scores every enriched fork's change digest
	// (commit messages + touched paths) against this free-text intent and
	// sorts the output by that relevance instead of heat. Scoring uses
	// QueryScorer; forces collect-then-emit semantics.
	Query string

	// QueryScorer performs the relevance scoring for Query. Nil → the
	// built-in lexical scorer. The CLI passes the Voyage cross-encoder here
	// when an API key is configured.
	QueryScorer embed.QueryScorer

	// Priors, when non-nil, scores every collected fork against a curated
	// interest spec (paths/keywords/languages/owner allow-deny) using only
	// already-fetched data — no network. It never mutates heat and never
	// drops a record; with neither Query nor ShortlistN set it splits output
	// into a matched-then-unmatched lane. Forces collect-then-emit semantics.
	Priors *priors.Spec

	// Touching, when non-empty, annotates every result with a TouchMatch
	// (see touching.go) computed from T2.Diffs — merge-base-relative by
	// construction, never tip-vs-tip. Forces collect-then-emit. Stream never
	// drops a result; the CLI decides what to print. Under Touching the
	// dispatch order compares pushed-after-fork forks before never-pushed
	// ones so the rate reserve is spent where a match is possible.
	Touching []string
	// TouchReport, when non-nil, receives the run tally.
	TouchReport *TouchSummary

	// ReserveDisabled turns off the automatic rate-limit reserve floor
	// (ReserveHeadroom). By default the pipeline stops enriching when the forge's
	// headroom drops below the reserve and marks the remaining forks degraded
	// (Result.BudgetSkip), so a scan can't drain the rate window to zero. Set
	// this (e.g. from SPOON_NO_RESERVE=1) to drain the full budget in one pass.
	ReserveDisabled bool

	// Cluster configures the optional post-T2 cluster pipeline. When
	// Cluster.Enabled is true and at least one fork is eligible, Stream will
	// collect every T1/T2 result, run the shared cluster.RunPipeline, and
	// emit each fork with its cluster fields populated. When false, Stream
	// emits forks as soon as their T2 enrichment completes (true streaming).
	//
	// Trade-off (vs. the dump path): clustering is a batch operation —
	// embeddings require all candidate forks present. When clustering is
	// enabled, Stream collapses to collect-then-emit semantics. The NDJSON
	// contract (one record per fork) is preserved.
	Cluster ClusterOptions

	// OwnerProfileCap bounds how many distinct owner-history calls this
	// run will make (P3). 0 → ownerProfileDefaultCap (30). The cap
	// protects the 5000/h rate budget on popular repos (3000+ forks
	// imply 3000+ distinct owners). When the cap is hit, remaining
	// forks are scored with OwnerProfile == nil (no penalty).
	OwnerProfileCap int

	// OwnerCacheTTL overrides the owner-profile on-disk cache TTL.
	// 0 disables cache reads (always fetch live). Set to a negative
	// value to force a fresh fetch (the test/refresh path). The CLI
	// translates an unset flag to 24h before calling Stream().
	OwnerCacheTTL time.Duration

	// MomentumSnapshots enables the 30-day on-disk surface-history cache used
	// to derive Result.Momentum. Default false so library callers and tests do
	// not write to the user's real cache unless they opt in.
	MomentumSnapshots bool

	// Logger receives cluster-pipeline progress and warnings. May be nil
	// (defaults to io.Discard).
	Logger io.Writer

	// CachedT2, when non-nil, returns a stored compare for a fork (nil = miss).
	// The caller keys validity on the fork's current pushed_at, so a hit is
	// authoritative: it skips the Compare API call and the rate-reserve gate.
	// Left nil on --refresh.
	CachedT2 func(forge.T1Data) *forge.T2Data

	// Report receives the terminal acquisition metadata after the fork channel
	// closes. The pointer is caller-owned; Stream copies the provider's report
	// into it after the channel closes. Nil means the caller does not want the
	// report. The report is also emitted on the ForkMsg channel so callers that
	// do not use Stream() can still observe it.
	Report *forge.AcquisitionReport

	// NoBatchCompare disables the pre-dispatch GraphQL divergence batch
	// (see batchcompare.go) even when the provider supports it, forcing
	// every eligible fork through the per-fork REST Compare path as before
	// batching existed. Set from a CLI escape hatch; false is the default
	// (use the batch whenever tier and provider allow it).
	NoBatchCompare bool

	// CompareReport, when non-nil, receives the run's compare-source tally
	// (cache hits, batch-synthesised zeros, live REST compares, diff
	// fallbacks) after the fork channel closes. Caller-owned, like
	// TouchReport.
	CompareReport *CompareSummary

	// NetworkScope controls fork-list breadth: "direct" (default) or "all".
	// When "all", ListForksBounded is used if the provider implements it.
	NetworkScope string
	// NetworkMaxNodes caps distinct repos visited. 0 → 5000.
	NetworkMaxNodes int
	// NetworkMaxDepth caps traversal depth from root. 0 → 3.
	NetworkMaxDepth int
	// NetworkMaxPages caps GraphQL pages. 0 → 200.
	NetworkMaxPages int
	// NetworkMaxElapsed caps wall-clock time. 0 → 2 minutes.
	NetworkMaxElapsed time.Duration
}

// ownerProfileDefaultCap is the per-run cap on the number of distinct
// owner-profile fetches. 30 strikes a balance: enough to characterize
// the most-promising fork owners in a typical run, low enough to
// leave the 5000/h rate budget untouched for the rest of the pipeline.
const ownerProfileDefaultCap = 30

// touchingNeverPushedDemotion pushes never-pushed forks behind every pushed
// one in dispatch order under --touching: they are still compared if
// headroom remains, but the rate reserve is spent where a match is
// possible first.
const touchingNeverPushedDemotion = 1e6

// ClusterOptions is the spn-side options struct for the cluster pipeline.
// Mirrors cluster.PipelineOptions but keeps the test seam unexported.
type ClusterOptions struct {
	Enabled        bool
	TopN           int
	Epsilon        float64
	MinClusterSize int
	Refresh        bool

	// CentralityBackend is forwarded to cluster.PipelineOptions. "" or
	// "directory" → directory-centrality proxy. "mdg" → Module Dependency
	// Graph. See `--full-mdg` on `spn forks list`.
	CentralityBackend string

	// CentralityHeadSHA is forwarded to cluster.PipelineOptions. It pins
	// the MDG cache to the upstream's current default-branch tip SHA so
	// the 24h fast path is actually used. When empty (e.g., a Parent
	// call that did not resolve a SHA), the cache is skipped and the MDG
	// builds from scratch on every run.
	CentralityHeadSHA string

	// StrictMDG, when true, surfaces a non-fatal MDG build/cache failure
	// as a ClusterSkip with code "mdg_unavailable" instead of silently
	// falling back to the directory proxy. Off by default — the silent
	// fallback is the right behavior for ordinary `--full-mdg` runs.
	// See `--strict-mdg` on `spn forks list`.
	StrictMDG bool

	// Categorize enables zero-shot category assignment for embedded forks.
	Categorize bool

	// Embedder, when non-nil, replaces the built-in lexical embedder
	// (fastembed when the CLI installed it, else the test seam; nil →
	// built-in lexical embedder). EmbedderID must identify it for cache keying.
	Embedder   embed.Embedder
	EmbedderID string

	// SiblingSimEnabled forwards cluster.PipelineOptions.SiblingSimEnabled.
	// P2 is on by default for standard runs (low cost), opt-in for
	// topic mode (5x cost multiplier per upstream).
	SiblingSimEnabled bool

	// SiblingSimMode forwards cluster.PipelineOptions.SiblingSimMode.
	// Empty preserves upstream_readme.
	SiblingSimMode cluster.SiblingSimMode

	// SiblingSearcher forwards cluster.PipelineOptions.SiblingSearcher.
	// The CLI constructs a real GHSiblingSearcher when --sibling-sim
	// is set; for tests, a fake searcher can be wired in directly.
	SiblingSearcher cluster.SiblingSearcher
}

// SetEmbedderForTest installs an embedder stub on ClusterOptions for tests.
func (o *ClusterOptions) SetEmbedderForTest(e embed.Embedder) { o.Embedder = e }

// Lineage describes a fork's position in the fork network tree.
type Lineage struct {
	NetworkRoot   string // network root owner/repo
	DirectParent  string // direct parent owner/repo
	DepthFromRoot int    // edges from root: 1=direct child, 0=unknown
}

// Coverage reports how completely the fork list covers the network.
type Coverage struct {
	DirectTotalCount      int // forks.totalCount from GraphQL root (direct children)
	WholeNetworkForkCount int // repository.forkCount from GraphQL root (whole network)
	Unresolved            int // whole - direct; negative clamped to 0
}

// Result is a single fork's outcome. Fork is always populated; Err and the
// T2/T3 pointers may be nil depending on tier and per-fork errors.
type Result struct {
	Fork forge.T1Data
	T2   *forge.T2Data
	T3   *forge.T3Data
	Heat heat.HeatResult
	Err  *Error

	// T2FromCache marks a compare served by Options.CachedT2 rather than
	// fetched live. Persistence must then leave the stored compare rows alone:
	// cached T2s carry no patch text (the store's read path skips it), and
	// re-persisting them would overwrite full rows with patch-less ones.
	T2FromCache bool

	// ExpectedRank / RankConfidence are set only when ShortlistN > 0 (Robbins
	// expected-rank shortlist). Lower ExpectedRank ≈ more likely the best fork;
	// RankConfidence mirrors Heat.Confidence (tier reached).
	ExpectedRank   float64
	RankConfidence float64
	// Rank carries the full rank summary (P-score, P(rank ≤ k), P(rank = 1),
	// 95% rank interval) for the same pool; nil when ShortlistN == 0.
	Rank *RankStats
	// EB carries the empirical-Bayes shrinkage summary; nil unless
	// Options.EB applied to this fork (heat > 0, fit succeeded).
	EB *EBStats

	// QueryScore is the fork's relevance to Options.Query in [0,1];
	// QueryMethod records how it was computed ("voyage" cross-encoder or
	// "lexical" cosine fallback). Both zero when no query was given.
	QueryScore  float64
	QueryMethod string

	// PriorScore is the fork's match to Options.Priors in [0,1]; PriorReasons
	// lists the stable, sorted facts behind it. Both zero/nil when no priors
	// spec was given. Never affects heat.
	PriorScore   float64
	PriorReasons []string

	// Touching is the --touching verdict; nil when the option was not set.
	// Presentation and CLI filtering only: never feeds heat or rank.
	Touching *TouchMatch

	Visibility  VisibilityDecision
	Degraded    []DegradedStage
	NetworkRank *NetworkRank
	Momentum    MomentumInfo

	// ClusterSkip is set when the cluster pipeline was enabled but skipped
	// for a non-fatal reason (embedder unreachable, no model, etc.). Only the
	// first Result in the batch carries it; downstream consumers fan out a
	// single user-facing warning. Nil when clustering ran or was disabled.
	ClusterSkip *ClusterSkip

	// T3Skip is set when the contributors (T3) enrichment was skipped for a
	// non-fatal reason — most commonly GitHub's stats endpoint returning 202
	// (stats not yet computed). The fork is still emitted with its T1+T2 data
	// and heat; consumers surface a warning rather than dropping the fork.
	// Nil when T3 ran, was not requested, or failed fatally (rate limit).
	T3Skip *StageSkip

	// BudgetSkip is set when a fork that WOULD have been enriched was skipped
	// because the rate-limit reserve floor (ReserveHeadroom) was reached. The
	// fork is emitted with T1 data only; consumers should mark it un-enriched /
	// the run degraded rather than reporting its (absent) divergence as zero.
	// Re-running after the rate window resets backfills it from cache. Nil when
	// the fork was enriched, or was never eligible for enrichment by design.
	BudgetSkip *StageSkip

	// OwnerProfileSkip is set when the owner-profile fetch (P3) was
	// skipped for a non-fatal reason (rate-limit reserve reached, hard
	// cap exhausted, or non-GH provider). The fork is still emitted
	// with Fork.OwnerProfile == nil; consumers surface a warning
	// rather than reporting a missing penalty as zero.
	OwnerProfileSkip *StageSkip

	// SiblingSimSkip is set when the sibling-similarity search (P2) was
	// skipped for a non-fatal reason (empty candidate set, embedder
	// unavailable, or the run reached the cluster pass without
	// SiblingSimEnabled). The fork is emitted with Heat.SiblingSim = 0;
	// no post-hoc bonus is applied.
	SiblingSimSkip      *StageSkip
	CommitFilesComplete bool
	CommitFilesSkip     *StageSkip

	// Lineage describes the fork's position in the fork network tree.
	Lineage Lineage

	// Coverage reports the direct vs whole-network fork counts.
	Coverage Coverage
}

// StageSkip describes a non-fatal, per-fork enrichment skip. Unlike Error it
// does not drop the fork from output — the fork is emitted with whatever data
// did resolve, and the skip is surfaced as a warning.
type StageSkip struct {
	Stage  string // enrichment stage, e.g. "contributors"
	ForkID string
	Reason string
}

// ClusterSkip describes why the cluster pipeline was skipped for this run.
// Surfaced once per Stream invocation on the first Result.
type ClusterSkip struct {
	Code     string
	Message  string
	Endpoint string
	Model    string
}

// Error is the per-fork error reported on the stream. Distinct from a fatal
// error which is returned synchronously from Stream() itself.
type Error struct {
	Code    string
	Message string
	Details map[string]any
}

// Stream returns a channel that yields one Result per fork. The channel is
// closed when enumeration completes or ctx is cancelled.
//
// Fatal errors (auth, Parent fetch failure, ctx cancel before any output)
// are returned synchronously. Per-fork errors are surfaced via Result.Err.
//
// When opts.Cluster.Enabled is true, Stream switches from true-streaming to
// collect-then-emit semantics: every T1+T2-enriched result is buffered, the
// cluster pipeline runs over the batch, and each Result is then emitted with
// its cluster fields populated. See Options.Cluster for the trade-off rationale.
func Stream(ctx context.Context, provider forge.Forge, owner, repo string, opts Options) (<-chan Result, error) {
	touching := len(opts.Touching) > 0
	var touchMatcher pathmatch.Matcher
	if touching {
		m, err := pathmatch.Compile(opts.Touching)
		if err != nil {
			return nil, fmt.Errorf("--touching: %w", err)
		}
		touchMatcher = m
	}

	parent, err := provider.Parent(ctx, owner, repo)
	if err != nil {
		var rl *gh.RateLimitError
		if errors.As(err, &rl) {
			return nil, fmt.Errorf("rate_limited: reset_at=%s retry_after_seconds=%d: %w",
				rl.ResetAt.UTC().Format(time.RFC3339), rl.RetryAfterSeconds(), err)
		}
		return nil, fmt.Errorf("fetch parent: %w", err)
	}
	// Bounded whole-network dispatch: use ListForksBounded when scope is "all"
	// and the provider implements it; fall back to ListForks for all other cases.
	var t1ch <-chan forge.ForkMsg
	var listErr error
	if opts.NetworkScope == "all" {
		if bounded, ok := provider.(forge.ListForksBoundedProvider); ok {
			boundedOpts := forge.BoundedOptions{
				MaxNodes:   opts.NetworkMaxNodes,
				MaxDepth:   opts.NetworkMaxDepth,
				MaxPages:   opts.NetworkMaxPages,
				MaxElapsed: opts.NetworkMaxElapsed,
			}
			// Default caps
			if boundedOpts.MaxNodes <= 0 {
				boundedOpts.MaxNodes = 5000
			}
			if boundedOpts.MaxDepth <= 0 {
				boundedOpts.MaxDepth = 3
			}
			if boundedOpts.MaxPages <= 0 {
				boundedOpts.MaxPages = 200
			}
			if boundedOpts.MaxElapsed <= 0 {
				boundedOpts.MaxElapsed = 2 * time.Minute
			}
			t1ch, listErr = bounded.ListForksBounded(ctx, owner, repo, boundedOpts)
		} else {
			// Provider doesn't implement bounded; fall back to ListForks
			t1ch, listErr = provider.ListForks(ctx, owner, repo)
		}
	} else {
		t1ch, listErr = provider.ListForks(ctx, owner, repo)
	}
	if listErr != nil {
		var rl *gh.RateLimitError
		if errors.As(listErr, &rl) {
			return nil, fmt.Errorf("rate_limited: reset_at=%s retry_after_seconds=%d: %w",
				rl.ResetAt.UTC().Format(time.RFC3339), rl.RetryAfterSeconds(), listErr)
		}
		return nil, fmt.Errorf("list forks: %w", listErr)
	}

	logger := opts.Logger
	if logger == nil {
		logger = io.Discard
	}

	out := make(chan Result)
	go func() {
		defer close(out)

		var t1Forks []forge.T1Data
		var providerReport *forge.AcquisitionReport
	drain:
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-t1ch:
				if !ok {
					break drain
				}
				if msg.Report != nil {
					// Terminal acquisition metadata: capture the last one
					// (providers may emit at most one) and do NOT add it to
					// the fork list. It is intentionally never a Result.
					providerReport = msg.Report
					continue
				}
				if msg.Err != nil {
					continue
				}
				if heat.IsGhostFork(msg.Fork.PushedAt, parent.PushedAt, msg.Fork.IsArchived) {
					continue
				}
				t1Forks = append(t1Forks, msg.Fork)
			}
		}

		// After the channel closes, copy the provider's terminal report into
		// the caller-owned report target so the caller can inspect it without
		// draining the ForkMsg channel itself.
		if opts.Report != nil && providerReport != nil {
			*opts.Report = *providerReport
		}

		now := time.Now()
		if opts.Now != nil {
			now = opts.Now()
		}
		momentumByID := unknownMomentumMap(t1Forks)
		if opts.MomentumSnapshots {
			momentumByID = buildMomentumMap(now, momentumProviderKey(ctx, provider), owner, repo, t1Forks, logger)
		}

		stats := makeStats(t1Forks, now)
		scorer := heat.NewScorerWeighted(stats, opts.HeatWeights)

		type scored struct {
			fork    forge.T1Data
			res     heat.HeatResult
			promise float64 // cheap divergence signal (ComparePromise), survives the sort
		}
		all := make([]scored, len(t1Forks))
		for i, f := range t1Forks {
			input := buildScoreInput(f, parent, now)
			res := scorer.ScoreRaw(input)
			// T1-only finalize: trust + archived/recency penalties. Divergence
			// is unknown at this point, so no-ahead zeroing cannot apply yet.
			scorer.Finalize(&res, int64(i), heat.PenaltyInput{Archived: f.IsArchived})
			all[i] = scored{
				fork:    f,
				res:     res,
				promise: ComparePromise(f, parent.PushedAt),
			}
		}

		// Optimal-stopping ("secretary") compare gate (see secretary.go): when a
		// Budget is set, decide which forks earn the expensive T2/T3 calls by the
		// cheap divergence signal, in enumeration (arrival) order — before the
		// surface-score sort below, which only governs output rank.
		compareByID := make(map[string]bool)
		if opts.Budget > 0 {
			promises := make([]float64, len(all))
			for i, s := range all {
				promises[i] = s.promise
			}
			for i, keep := range SelectByOptimalStopping(promises, opts.Budget) {
				if keep {
					compareByID[all[i].fork.ID] = true
				}
			}
		}

		sort.Slice(all, func(i, j int) bool { return all[i].res.Score > all[j].res.Score })

		topN := opts.TopN
		if topN <= 0 || topN > len(all) {
			topN = len(all)
		}
		// eligible reports whether a fork should get the expensive compare /
		// contributors calls: the secretary-selected set when a Budget is set,
		// otherwise the top-N-by-surface-score slice.
		eligible := func(forkID string, i int) bool {
			if opts.Budget > 0 {
				return compareByID[forkID]
			}
			return i < topN
		}
		tier := opts.Tier
		if tier == 0 {
			tier = 3
		}

		// Pre-dispatch GraphQL divergence batch (see batchcompare.go): resolve
		// ahead/behind for every fork that would otherwise get a REST compare,
		// in one batched call, before any worker starts. A fork the batch finds
		// has nothing ahead never reaches a REST compare at all; a divergent
		// fork carries a single pre-chosen branch into the worker instead of
		// REST's own per-fork branch scan.
		var pending []forge.T1Data
		var divergence map[string]forge.ForkDivergence
		var batchStats forge.BatchStats
		batchRan := false
		if tier >= 2 && !opts.NoBatchCompare {
			for i, s := range all {
				if !eligible(s.fork.ID, i) {
					continue
				}
				if opts.CachedT2 != nil && opts.CachedT2(s.fork) != nil {
					continue
				}
				pending = append(pending, s.fork)
			}
			if bp, ok := provider.(forge.BatchCompareProvider); ok && len(pending) > 0 {
				var berr error
				divergence, batchStats, berr = bp.BatchCompare(ctx, pending)
				switch {
				case berr == nil:
					batchRan = true
				case errors.Is(berr, forge.ErrBatchCompareUnavailable):
					// Silent fallback: no GraphQL backend available. Not a
					// batchRan condition -- keep the pre-batch estimate line.
					divergence = nil
				default:
					fmt.Fprintf(logger, "[triage] batch compare degraded: %v; falling back to per-fork REST\n", berr)
					if opts.CompareReport != nil {
						opts.CompareReport.BatchError = berr.Error()
					}
					// Keep whatever map came back (may be partial or nil) --
					// forks it did resolve still skip REST or get a chosen
					// branch; the rest fall back to Compare in the worker.
					batchRan = true
				}
			}
		}

		// Up-front request estimate + headroom heads-up, so the user sees the
		// scope without having to count forks. The live reserve floor below, not
		// this estimate, governs when enrichment actually stops.
		if tier >= 2 && len(all) > 0 {
			if batchRan {
				divergent, resolved, restEstimate := batchEstimate(pending, divergence, tier)
				fmt.Fprintf(logger, "[triage] %d forks; ~%d REST requests (%d divergent of %d resolved by one GraphQL batch); rate headroom %.0f%%\n",
					len(all), restEstimate, divergent, resolved, provider.Headroom()*100)
			} else {
				fmt.Fprintf(logger, "[triage] %d forks; ~%d+ API requests to enrich at tier %d; rate headroom %.0f%%\n",
					len(all), EstimateRequests(len(all), tier), tier, provider.Headroom()*100)
			}
		}

		concurrency := 4
		if a, _ := provider.Auth(ctx); a.Concurrency > 0 {
			concurrency = a.Concurrency
		}

		// EVPR best-first dispatch (see secretary.go DispatchPriority): workers
		// claim forks in descending expected-value-per-request order, so the
		// expensive compare/contributors budget — and any rate window — is spent
		// on the most-divergent forks first. This is decoupled from `all`'s
		// surface-score order: dispatchOrder is a separate permutation, so the
		// eligible() index `i` (and thus the top-N path) is unchanged. A lock-free
		// atomic cursor hands each position to exactly one worker.
		priorities := make([]float64, len(all))
		for i, s := range all {
			priorities[i] = DispatchPriority(s.fork, parent.PushedAt, s.res.Score)
			if touching && NeverPushed(s.fork) {
				priorities[i] -= touchingNeverPushedDemotion
			}
		}
		dispatchOrder := make([]int, len(all))
		for i := range dispatchOrder {
			dispatchOrder[i] = i
		}
		sort.SliceStable(dispatchOrder, func(a, b int) bool {
			return priorities[dispatchOrder[a]] > priorities[dispatchOrder[b]]
		})
		var cursor atomic.Int64

		// In streaming mode (no clustering) we forward results to `out` as
		// they finish. In batch mode (clustering enabled) we instead collect
		// them into `collected` (mu-guarded) and emit at the end.
		// Clustering and the expected-rank shortlist both require all enriched
		// results in hand, so either forces collect-then-emit.
		batchMode := opts.Cluster.Enabled || opts.ShortlistN > 0 || opts.Query != "" || opts.Priors != nil || opts.CommitFiles || len(opts.Touching) > 0
		var (
			collectedMu sync.Mutex
			collected   []Result
		)
		var wg sync.WaitGroup
		var budgetSkipped atomic.Int64
		var ownerProfileCalls atomic.Int64
		// Compare-source tally for Options.CompareReport; written from
		// workers, so atomic rather than mutex-guarded like `collected`.
		var compareCached atomic.Int64
		var compareBatch atomic.Int64
		var compareREST atomic.Int64
		var compareDiffFallback atomic.Int64
		for w := 0; w < concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					if ctx.Err() != nil {
						return
					}
					pos := int(cursor.Add(1) - 1)
					if pos >= len(dispatchOrder) {
						return
					}
					i := dispatchOrder[pos]
					s := all[i]
					r := Result{
						Fork:     s.fork,
						Heat:     s.res,
						Momentum: momentumByID[s.fork.ID],
						Lineage: Lineage{
							NetworkRoot:   s.fork.SourceFullPath,
							DirectParent:  s.fork.ParentFullPath,
							DepthFromRoot: s.fork.DepthFromRoot,
						},
						Coverage: Coverage{
							DirectTotalCount:      s.fork.DirectTotalCount,
							WholeNetworkForkCount: s.fork.WholeNetworkForkCount,
							Unresolved:            coverageUnresolved(s.fork, opts.Report),
						},
					}

					// Auto-budget: an eligible fork is enriched only while the
					// rate-limit reserve holds. Because dispatch is best-first
					// (DispatchPriority), the forks enriched before the floor is
					// reached are the most promising; the rest are flagged
					// degraded (BudgetSkip), not silently zeroed.
					enrich := eligible(s.fork.ID, i)
					// Owner-profile fetch (P3): best-first dispatch, capped at
					// OwnerProfileCap calls per run. The remaining forks are
					// scored with Fork.OwnerProfile == nil (no penalty).
					// The fetch is gated on the same headroom floor as
					// compare/contributors; a low-headroom run skips owner
					// hard bound; headroom is the courtesy floor.
					if s.fork.Owner != "" {
						ownerCap := opts.OwnerProfileCap
						if ownerCap <= 0 {
							ownerCap = ownerProfileDefaultCap
						}
						ghp, isGH := provider.(*gh.GHProvider)
						if !isGH {
							r.OwnerProfileSkip = &StageSkip{
								Stage:  "owner_profile",
								ForkID: s.fork.ID,
								Reason: "owner profile fetch only implemented for the GitHub provider",
							}
						} else if int(ownerProfileCalls.Load()) >= ownerCap {
							r.OwnerProfileSkip = &StageSkip{
								Stage:  "owner_profile",
								ForkID: s.fork.ID,
								Reason: fmt.Sprintf("owner-profile cap (%d) exhausted for this run; remaining forks scored with no P3 signal", ownerCap),
							}
						} else if !opts.ReserveDisabled && provider.Headroom() < ReserveHeadroom {
							r.OwnerProfileSkip = &StageSkip{
								Stage:  "owner_profile",
								ForkID: s.fork.ID,
								Reason: "rate-limit reserve reached; re-run after the window resets to enrich (cached results resume)",
							}
						} else if ghp.Client() == nil {
							r.OwnerProfileSkip = &StageSkip{
								Stage:  "owner_profile",
								ForkID: s.fork.ID,
								Reason: "GitHub client not initialized for this provider; owner profile fetch skipped",
							}
						} else {
							rec, cached, ferr := ghp.Client().FetchUserRepos(
								ctx, s.fork.Owner, opts.OwnerCacheTTL,
							)
							if rec != nil {
								s.fork.OwnerProfile = &forge.OwnerProfile{
									Login:            rec.Login,
									TotalPublicRepos: rec.TotalPublicRepos,
									ForkCount:        rec.ForkCount,
									SignalForkCount:  rec.SignalForkCount,
									NonForkRepoCount: rec.NonForkRepoCount,
									FetchedAt:        rec.FetchedAt,
								}
								// Mirror onto r.Fork so the emitted Result
								// matches what scoring saw. The worker
								// snapshot for the result was already taken
								// above; copying here keeps the public
								// record and the in-flight fork in sync.
								r.Fork.OwnerProfile = s.fork.OwnerProfile
							}
							// Only count a real API call against the cap:
							//   - cache hit (cached=true): no rate-budget
							//     was burned, so the slot is free
							//   - rate-limit 403: the attempt failed
							//     before consuming a slot; we want the
							//     slot to remain for the next fork
							var rl *gh.RateLimitError
							if !cached && !errors.As(ferr, &rl) {
								ownerProfileCalls.Add(1)
							}
						}
					}
					// A stored compare costs no API budget, so it is consulted
					// before the rate-reserve gate: even a drained window can
					// serve cached divergence.
					if enrich && tier >= 2 && opts.CachedT2 != nil {
						if t2 := opts.CachedT2(s.fork); t2 != nil {
							r.T2 = t2
							r.T2FromCache = true
							compareCached.Add(1)
						}
					}
					// Batch-resolved divergence costs no per-fork API budget
					// either, so it too is consulted before the reserve gate: a
					// fork the batch found has nothing ahead is fully resolved
					// here (synthesised T2, no REST needed) and never reaches
					// the gate at all. A divergent fork keeps its pre-chosen
					// branch (sel) for the REST step below; the reserve gate
					// still applies to it like any other REST-bound fork.
					var sel *forge.BranchSelection
					if enrich && tier >= 2 && r.T2 == nil && divergence != nil {
						if res, ok := resolveFromBatch(divergence, s.fork.ID); ok {
							if res.T2 != nil {
								r.T2 = res.T2
								compareBatch.Add(1)
							} else {
								sel = res.Selection
							}
						}
					}
					if enrich && tier >= 2 && r.T2 == nil && !opts.ReserveDisabled && provider.Headroom() < ReserveHeadroom {
						enrich = false
						r.BudgetSkip = &StageSkip{
							Stage:  "compare",
							ForkID: s.fork.ID,
							Reason: "rate-limit reserve reached; re-run after the window resets to enrich (cached results resume)",
						}
						budgetSkipped.Add(1)
					}
					if tier >= 2 && enrich && r.T2 == nil {
						t2, terr := compareFork(ctx, provider, s.fork, sel, logger)
						if terr != nil {
							var rl *gh.RateLimitError
							if errors.As(terr, &rl) {
								r.Err = &Error{
									Code:    "rate_limited",
									Message: "rate limit exceeded",
									Details: map[string]any{
										"fork":                s.fork.ID,
										"stage":               "compare",
										"reset_at":            rl.ResetAt.UTC().Format(time.RFC3339),
										"retry_after_seconds": rl.RetryAfterSeconds(),
									},
								}
							} else {
								r.Err = &Error{Code: "upstream_error", Message: terr.Error(), Details: map[string]any{"fork": s.fork.ID, "stage": "compare"}}
							}
						} else {
							r.T2 = &t2
							compareREST.Add(1)
							if t2.FilesComplete {
								compareDiffFallback.Add(1)
							}
						}
					}
					if tier >= 3 && enrich && r.Err == nil {
						t3, terr := provider.Contributors(ctx, s.fork)
						if terr != nil {
							var rl *gh.RateLimitError
							if errors.As(terr, &rl) {
								r.Err = &Error{
									Code:    "rate_limited",
									Message: "rate limit exceeded",
									Details: map[string]any{
										"fork":                s.fork.ID,
										"stage":               "contributors",
										"reset_at":            rl.ResetAt.UTC().Format(time.RFC3339),
										"retry_after_seconds": rl.RetryAfterSeconds(),
									},
								}
							} else {
								// Contributors is optional enrichment (it only feeds
								// CommitSpanDays into scoring). A failure here —
								// notably GitHub's 202 "still computing stats" — must
								// not drop an otherwise-good fork from the output.
								// Degrade gracefully: skip T3, flag it, keep the fork.
								r.T3Skip = &StageSkip{
									Stage:  "contributors",
									ForkID: s.fork.ID,
									Reason: terr.Error(),
								}
							}
						} else {
							r.T3 = &t3
						}
					}
					r.Heat = rescore(scorer, int64(i), s.fork, parent, now, r.T2, r.T3)
					if !batchMode {
						// In batch mode these are derived in the tail, after the
						// cluster pass sets final Heat and any ClusterSkip; deriving
						// here is redundant and runs on soon-to-be-overwritten Heat.
						r.Visibility = deriveVisibility(r)
						r.Degraded = collectDegradedStages(r)
					}
					if batchMode {
						collectedMu.Lock()
						collected = append(collected, r)
						collectedMu.Unlock()
						continue
					}
					select {
					case out <- r:
					case <-ctx.Done():
						return
					}
				}
			}()
		}
		wg.Wait()

		if n := budgetSkipped.Load(); n > 0 {
			fmt.Fprintf(logger, "[triage] degraded: %d/%d forks left un-enriched at the rate-limit reserve; re-run after the window resets to backfill (cached results resume)\n",
				n, len(all))
		}

		if opts.CompareReport != nil {
			opts.CompareReport.Cached = int(compareCached.Load())
			opts.CompareReport.Batch = int(compareBatch.Load())
			opts.CompareReport.REST = int(compareREST.Load())
			opts.CompareReport.DiffFallback = int(compareDiffFallback.Load())
			opts.CompareReport.BatchQueries = batchStats.Queries
			opts.CompareReport.BatchCost = batchStats.Cost
			// LastTouchSkipped stays 0 here; a later stage (--touching's
			// last-touch attribution pass) increments it.
		}
		if tier >= 2 && len(all) > 0 {
			fmt.Fprintf(logger, "[triage] compare sources: cached %d · graphql_batch %d · rest %d · diff_fallback %d · last_touch_skipped %d (batch: %d queries, cost %d)\n",
				compareCached.Load(), compareBatch.Load(), compareREST.Load(), compareDiffFallback.Load(), 0,
				batchStats.Queries, batchStats.Cost)
		}

		if !batchMode {
			return
		}

		if opts.CommitFiles {
			enrichCommitFiles(ctx, provider, collected, opts)
		}

		// Cluster pass: build EnrichedFork pointers over `collected`, run the
		// shared pipeline, then emit each Result. Heat is mutated in place via
		// the EnrichedFork pointer back into collected[i].Heat.
		var skip *ClusterSkip
		if opts.Cluster.Enabled {
			skip = runForksClusterPipeline(ctx, provider, &parent, owner, repo, collected, opts.Cluster, logger)
		}

		if skip != nil && len(collected) > 0 {
			collected[0].ClusterSkip = skip
		}

		var touchSummary TouchSummary
		if touching {
			// Same backend and caches as the cluster pass: a hit is free, a
			// miss costs what clustering would have paid. Failure degrades
			// to zero centrality, never to a missing verdict.
			// NB: Stream's parameter named `repo` shadows the internal/repo
			// package here, so do not name the repo.Centrality type in this
			// scope; take the interface value straight from the call.
			envInputs, envOpts := clusterEnv(ctx, provider, &parent, owner, repo, opts.Cluster)
			centrality, ok, cerr := cluster.LoadOrComputeCentrality(ctx, envOpts, envInputs, logger)
			if !ok {
				centrality = nil
				if cerr != nil {
					fmt.Fprintf(logger, "[touching] centrality unavailable (%v); files reported without impact\n", cerr)
				}
			}
			touchSummary = scoreTouching(touchMatcher, centrality, collected)
			if opts.TouchReport != nil {
				*opts.TouchReport = touchSummary
			}
			fmt.Fprintf(logger, "[touching] matched %d (%d partial: file list capped at %d) · unmatched %d · unknown %d · never_pushed %d · centrality=%q\n",
				touchSummary.Matched, touchSummary.Partial, forge.CompareFilesCap, touchSummary.Unmatched, touchSummary.Unknown, touchSummary.NeverPushed, touchSummary.CentralityMethod)
			if touchSummary.Unknown > 0 {
				fmt.Fprintf(logger, "[touching] %d forks have no usable compare; re-run after the rate window resets to backfill\n", touchSummary.Unknown)
			}
		}

		for i := range collected {
			collected[i].Visibility = deriveVisibility(collected[i])
			collected[i].Degraded = collectDegradedStages(collected[i])
		}

		// Query relevance pass: one batched scoring call over every enriched
		// fork's digest. Failures degrade to unscored output with a log line —
		// a broken scorer should not kill the listing.
		if opts.Query != "" {
			scoreQuery(ctx, opts, collected, logger)
		}

		// Prior relevance pass: pure/deterministic scoring of every collected
		// fork against the curated interest spec. No network calls.
		if opts.Priors != nil {
			scorePriors(opts, collected)
		}

		assignNetworkRanks(collected)

		if opts.ShortlistN > 0 {
			// Robbins expected-rank shortlist: compute over the final heat (after
			// clustering, so novelty is included). Under --touching the rank pool
			// is the matched subset; unmatched results are appended after it —
			// Stream still emits them, the CLI decides what to print.
			var report RankReport
			if touching {
				matched, rest := splitTouching(collected)
				matched, report = RankResults(matched, opts)
				collected = append(matched, rest...)
			} else {
				collected, report = RankResults(collected, opts)
			}
			if opts.RankReport != nil {
				*opts.RankReport = report
			}
		} else if opts.Query != "" {
			// Query mode: most relevant first; heat breaks ties.
			sort.SliceStable(collected, func(i, j int) bool {
				if collected[i].QueryScore != collected[j].QueryScore {
					return collected[i].QueryScore > collected[j].QueryScore
				}
				return collected[i].Heat.Score > collected[j].Heat.Score
			})
		} else if opts.Priors != nil {
			// Priors lane split: matched (PriorScore > 0) before unmatched,
			// each internally in heat order. Never hides a fork; network rank
			// (assigned above) stays heat-relative regardless of lane.
			sort.SliceStable(collected, func(i, j int) bool {
				im, jm := collected[i].PriorScore > 0, collected[j].PriorScore > 0
				if im != jm {
					return im
				}
				return collected[i].Heat.Score > collected[j].Heat.Score
			})
		} else {
			// Re-sort emitted output by heat desc to match dump's ordering.
			sort.SliceStable(collected, func(i, j int) bool {
				return collected[i].Heat.Score > collected[j].Heat.Score
			})
		}

		if touching {
			sortTouchingLanes(collected)
		}

		for _, r := range collected {
			select {
			case out <- r:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// runForksClusterPipeline runs the cluster pipeline over collected forks.
// Heat is mutated in place via the EnrichedFork pointer back into collected[i].Heat.
// Returns a non-nil ClusterSkip when the pipeline was non-fatally skipped.
func enrichCommitFiles(ctx context.Context, provider forge.Forge, collected []Result, opts Options) {
	fileProvider, supported := provider.(forge.CommitFileProvider)
	if !supported {
		for i := range collected {
			collected[i].CommitFilesSkip = &StageSkip{Stage: "commit_files", ForkID: collected[i].Fork.ID, Reason: "provider does not support per-commit file enrichment"}
		}
		return
	}
	budget := opts.CommitFileBudget
	if budget <= 0 {
		budget = 100
	}
	// claim reserves one unit of the commit-file budget, returning false when it
	// is exhausted. A run-scoped counter (topic mode) is shared across repos; the
	// per-call fallback keeps single-repo runs unchanged.
	runBudget := opts.CommitFileRunBudget
	used := 0
	claim := func() bool {
		if runBudget != nil {
			// CAS-decrement only while positive: no transient negative is ever
			// observable, so concurrent Stream calls sharing the budget can't
			// each see it exhausted or under-count a valid claim.
			for {
				current := runBudget.Load()
				if current <= 0 {
					return false
				}
				if runBudget.CompareAndSwap(current, current-1) {
					return true
				}
			}
		}
		if used >= budget {
			return false
		}
		used++
		return true
	}
	order := make([]int, len(collected))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return collected[order[i]].Heat.Score > collected[order[j]].Heat.Score
	})
	for _, index := range order {
		result := &collected[index]
		if result.T2 == nil {
			result.CommitFilesSkip = &StageSkip{Stage: "commit_files", ForkID: result.Fork.ID, Reason: "compare data unavailable"}
			continue
		}
		sort.SliceStable(result.T2.Commits, func(i, j int) bool {
			return result.T2.Commits[i].Timestamp.Before(result.T2.Commits[j].Timestamp)
		})
		complete := true
		for commitIndex := range result.T2.Commits {
			if !opts.ReserveDisabled && provider.Headroom() < ReserveHeadroom {
				complete = false
				result.CommitFilesSkip = &StageSkip{Stage: "commit_files", ForkID: result.Fork.ID, Reason: "rate-limit reserve reached"}
				break
			}
			if !claim() {
				complete = false
				result.CommitFilesSkip = &StageSkip{Stage: "commit_files", ForkID: result.Fork.ID, Reason: fmt.Sprintf("global commit-file budget (%d) exhausted", budget)}
				break
			}
			files, err := fileProvider.CommitFiles(ctx, result.Fork, result.T2.Commits[commitIndex].SHA)
			if err != nil {
				complete = false
				result.CommitFilesSkip = &StageSkip{Stage: "commit_files", ForkID: result.Fork.ID, Reason: err.Error()}
				break
			}
			result.T2.Commits[commitIndex].Files = files
		}
		result.CommitFilesComplete = complete
	}
}

// clusterEnv builds the cluster.PipelineInputs / cluster.PipelineOptions
// shared by the cluster pass (runForksClusterPipeline) and the --touching
// centrality lookup: same provider/tree/commit sources, same MDG cache pin,
// so a centrality fetch made for --touching alone is a cache hit once
// clustering has already run, and costs the same as clustering would have
// paid otherwise. The caller sets inputs.Forks and inputs.Upstream —
// clusterEnv doesn't have a collected-forks slice to draw Forks from, and
// leaving Upstream to the caller keeps this function usable from a context
// that only wants centrality, not a full cluster pass.
func clusterEnv(ctx context.Context, provider forge.Forge, parent *forge.ParentData, owner, repoName string, opts ClusterOptions) (cluster.PipelineInputs, cluster.PipelineOptions) {
	inputs := cluster.PipelineInputs{
		Provider:      providerName(ctx, provider),
		UpstreamOwner: owner,
		UpstreamRepo:  repoName,
	}
	if ghp, ok := provider.(*gh.GHProvider); ok {
		client := ghp.Client()
		if client != nil {
			defaultBranch := parent.DefaultBranch
			inputs.TreeSource = &gh.TreeSourceForRepo{Client: client, Ref: defaultBranch}
			inputs.CommitSource = &gh.CommitSourceForRepo{Client: client}
			inputs.ReadmeFetcher = client
		}
	}

	// Resolve the MDG-cache pin: prefer the caller's explicit
	// CentralityHeadSHA override (tests, future pinning beyond the
	// upstream's HEAD), and fall back to parent.HeadSHA when the caller
	// didn't supply one. Mirrors the same resolution in
	// internal/tui/cluster_bridge.go so both entry points honour the
	// documented contract on ClusterOptions.CentralityHeadSHA.
	pin := parent.HeadSHA
	if opts.CentralityHeadSHA != "" {
		pin = opts.CentralityHeadSHA
	}
	pipelineOpts := cluster.PipelineOptions{
		Enabled:           opts.Enabled,
		TopN:              opts.TopN,
		Epsilon:           opts.Epsilon,
		MinClusterSize:    opts.MinClusterSize,
		MinimumCandidates: 10,
		Refresh:           opts.Refresh,
		CentralityBackend: opts.CentralityBackend,
		CentralityHeadSHA: pin,
		StrictMDG:         opts.StrictMDG,
	}
	return inputs, pipelineOpts
}

func runForksClusterPipeline(
	ctx context.Context,
	provider forge.Forge,
	parent *forge.ParentData,
	owner, repoName string,
	collected []Result,
	opts ClusterOptions,
	logger io.Writer,
) *ClusterSkip {
	enriched := make([]cluster.EnrichedFork, len(collected))
	for i := range collected {
		enriched[i] = cluster.EnrichedFork{
			T1:   collected[i].Fork,
			T2:   collected[i].T2,
			Heat: &collected[i].Heat,
		}
	}

	inputs, pipelineOpts := clusterEnv(ctx, provider, parent, owner, repoName, opts)
	inputs.Forks = enriched
	inputs.Upstream = *parent

	// Forward the optional embedder / categorizer / label-polisher hooks
	// from the spn-side options into the cluster pipeline. When fastembed is
	// active it is passed here (clustering + zero-shot categories use it);
	// otherwise these stay nil and the pipeline uses the built-in lexical
	// embedder, keeping the cache key's backend identity correct.
	pipelineOpts.Embedder = opts.Embedder
	pipelineOpts.EmbedderID = opts.EmbedderID
	pipelineOpts.Categorize = opts.Categorize
	pipelineOpts.SiblingSimEnabled = opts.SiblingSimEnabled
	pipelineOpts.SiblingSearcher = opts.SiblingSearcher
	pipelineOpts.SiblingSimMode = opts.SiblingSimMode
	skip, err := cluster.RunPipeline(ctx, pipelineOpts, inputs, logger)
	if err != nil {
		fmt.Fprintf(logger, "[cluster] pipeline error: %v (continuing)\n", err)
		return nil
	}
	if skip == nil {
		return nil
	}
	// "disabled" / "no_eligible_forks" are silent — only surface real skips.
	switch skip.Code {
	case "disabled", "no_eligible_forks":
		return nil
	}
	return &ClusterSkip{
		Code:     skip.Code,
		Message:  skip.Message,
		Endpoint: skip.Endpoint,
		Model:    skip.Model,
	}
}

// providerName returns the canonical provider string ("github" / "gitlab")
// derived from forge.AuthInfo. Falls back to "other" when Auth fails or
// the provider is unknown — the cluster cache keys on this value, so it
// must be stable per provider.
func providerName(ctx context.Context, p forge.Forge) string {
	if p == nil {
		return "other"
	}
	auth, err := p.Auth(ctx)
	if err != nil {
		return "other"
	}
	switch auth.Provider {
	case forge.ProviderGitHub:
		return "github"
	case forge.ProviderGitLab:
		return "gitlab"
	default:
		return "other"
	}
}

// makeStats builds the input slice for heat.NewScorer from T1 fork data.
// ForkStats only needs ForkID, Stars, and SubForks for percentile ranking.
// ForkID is the slice index (int64) — a synthetic stable key used solely
// within this scorer instance; it is not a forge-level identifier.
func makeStats(forks []forge.T1Data, now time.Time) []heat.ForkStats {
	stats := make([]heat.ForkStats, len(forks))
	for i, f := range forks {
		stats[i] = heat.ForkStats{
			ForkID:     int64(i),
			Stars:      f.Stars,
			SubForks:   f.SubForkCount,
			PushedDays: now.Sub(f.PushedAt).Hours() / 24,
		}
	}
	return stats
}

// buildScoreInput maps a T1Data fork and its parent to a heat.ScoreInput.
func buildScoreInput(f forge.T1Data, parent forge.ParentData, now time.Time) heat.ScoreInput {
	return heat.ScoreInput{
		T1: heat.Tier1ParamsV2{
			Stars:             f.Stars,
			SubForks:          f.SubForkCount,
			ReleaseCount:      f.ReleaseCount,
			DaysSincePush:     now.Sub(f.PushedAt).Hours() / 24,
			DaysSinceUpstream: now.Sub(parent.PushedAt).Hours() / 24,
		},
	}
}

// rescore rebuilds a ScoreInput including T2/T3 data and returns the
// updated HeatResult. Used to refresh Heat after enrichment.
func rescore(scorer *heat.Scorer, forkID int64, f forge.T1Data, parent forge.ParentData, now time.Time, t2 *forge.T2Data, t3 *forge.T3Data) heat.HeatResult {
	input := buildScoreInput(f, parent, now)
	if t2 != nil {
		input.T2 = &heat.Tier2ParamsV2{
			MNA:                t2.MNA,
			AheadBy:            t2.AheadCount,
			BehindBy:           t2.BehindCount,
			FeatureCommitRatio: t2.FeatureCommitRatio,
		}
	}
	if t3 != nil {
		input.T3 = &heat.Tier3ParamsV2{}
	}
	// Wire v2 lone wolf when we have commits to analyze.
	if t2 != nil && len(t2.Commits) > 0 {
		lw := buildLoneWolfInput(f, now, t2)
		if input.T3 == nil {
			input.T3 = &heat.Tier3ParamsV2{}
		}
		input.T3.LoneWolf = heat.DetectLoneWolfV2(lw)
		input.T3.CommitSpanDays = float64(forge.CommitSpanDays(t2.Commits))
	}
	result := scorer.ScoreRaw(input)
	// Propagate the lone wolf result to the top-level HeatResult field so
	// callers (TUI, dump, JSON) can access it without digging into T3 params
	// — and so ApplyTrust's lone-wolf boost can see it.
	if input.T3 != nil && input.T3.LoneWolf != nil {
		result.LoneWolfV2 = input.T3.LoneWolf
	}
	penalty := heat.PenaltyInput{Archived: f.IsArchived}
	if t2 != nil {
		penalty.AheadKnown = true
		penalty.AheadAllBranches = t2.AheadCount
		penalty.Upstreamed = t2.Upstreamed
	}
	penalty.ForkTopics = f.Topics
	penalty.ParentTopics = parent.Topics
	penalty.OwnerProfile = f.OwnerProfile
	scorer.Finalize(&result, forkID, penalty)
	return result
}

// buildLoneWolfInput adapts forge T2Data into the input shape DetectLoneWolfV2 expects.
func buildLoneWolfInput(f forge.T1Data, now time.Time, t2 *forge.T2Data) heat.LoneWolfInput {
	commits := make([]heat.LWCommitInfo, 0, len(t2.Commits))
	authors := make([]string, 0, len(t2.Commits))
	for _, c := range t2.Commits {
		login := c.AuthorLogin
		if login == "" {
			login = c.AuthorEmail
		}
		commits = append(commits, heat.LWCommitInfo{
			AuthorLogin: login,
			Message:     c.Message,
			Date:        c.Timestamp,
		})
		// Only count a real identifier as a contributor. An empty login would be
		// treated as a distinct human by heat.filterBots, producing false-positive
		// lone-wolf detections; the commit still feeds message analysis above.
		if login != "" {
			authors = append(authors, login)
		}
	}
	files := make([]heat.FileChange, 0, len(t2.Diffs))
	for _, d := range t2.Diffs {
		files = append(files, heat.FileChange{
			Filename:  d.Path,
			Additions: d.Additions,
			Deletions: d.Deletions,
		})
	}
	return heat.LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  authors,
		AheadBy:       t2.AheadCount,
		DaysSincePush: now.Sub(f.PushedAt).Hours() / 24,
	}
}

func coverageUnresolved(f forge.T1Data, report *forge.AcquisitionReport) int {
	if report != nil && report.Scope == "all" && report.Unresolved > 0 {
		return report.Unresolved
	}
	return max(f.WholeNetworkForkCount-f.DirectTotalCount, 0)
}

// scoreQuery computes Result.QueryScore for every collected fork in one
// batched scorer call. Forks without T2 data score 0 (nothing to judge).
func scoreQuery(ctx context.Context, opts Options, collected []Result, logger io.Writer) {
	scorer := opts.QueryScorer
	if scorer == nil {
		scorer = embed.LexicalQueryScorer{}
	}
	// The label comes from the scorer, never from a constant here: a hardcoded
	// "openvino" outlived the implementation it named and mislabeled every
	// injected scorer until this was fixed.
	method := scorer.Method()
	idx := make([]int, 0, len(collected))
	docs := make([]string, 0, len(collected))
	for i := range collected {
		d := embed.QueryDigest(collected[i].T2)
		if d == "" {
			continue
		}
		idx = append(idx, i)
		docs = append(docs, d)
	}
	if len(docs) == 0 {
		return
	}
	scores, err := scorer.Rerank(ctx, opts.Query, docs)
	if err != nil {
		fmt.Fprintf(logger, "[query] scoring failed: %v (emitting unscored)\n", err)
		return
	}
	if len(scores) != len(docs) {
		fmt.Fprintf(logger, "[query] scoring failed: returned %d scores, want %d (emitting unscored)\n", len(scores), len(docs))
		return
	}
	for j, i := range idx {
		collected[i].QueryScore = scores[j]
		collected[i].QueryMethod = method
	}
}

// scorePriors computes Result.PriorScore/PriorReasons for every collected
// fork against opts.Priors. Pure and deterministic — it reuses the query
// digest (lowercased) and the fork's T2 diff paths, and issues no provider
// calls. Never mutates heat.
func scorePriors(opts Options, collected []Result) {
	for i := range collected {
		r := &collected[i]
		digest := strings.ToLower(embed.QueryDigest(r.T2))
		var paths []string
		if r.T2 != nil {
			paths = make([]string, 0, len(r.T2.Diffs))
			for _, d := range r.T2.Diffs {
				paths = append(paths, d.Path)
			}
		}
		m := opts.Priors.Score(r.Fork.Language, r.Fork.Owner, paths, digest)
		r.PriorScore = m.Score
		r.PriorReasons = m.Reasons
	}
}

// applyEB fits the normal–normal model over the forks in pool with heat > 0,
// overwrites mu/sigma in place with the shrunken score and posterior sigma
// for those forks, attaches Result.EB, and returns the EB part of the
// RankReport. Zero-heat forks (no-ahead / upstreamed zeroing) are neither
// fitted nor shrunk: they carry no evidence about heterogeneity and would
// drag the pooled mean to 0.
func applyEB(pool []Result, mu, sigma []float64, priorScale float64) RankReport {
	if len(mu) < len(pool) || len(sigma) < len(pool) {
		return RankReport{EBRegime: EBRegimeInsufficient}
	}
	idx := make([]int, 0, len(pool))
	for i := range pool {
		if mu[i] > 0 {
			idx = append(idx, i)
		}
	}
	y := make([]float64, len(idx))
	s := make([]float64, len(idx))
	for k, i := range idx {
		y[k] = mu[i]
		s[k] = sigma[i]
	}
	scale := priorScale
	if scale <= 0 {
		scale = madScale(y)
	}
	fit, ok := fitNormalNormal(y, s, scale)
	report := RankReport{EBRegime: fit.Regime, EBPool: len(idx), TauHat: math.Sqrt(fit.Tau2), EBMean: fit.M, PriorScale: scale}
	if !ok {
		return report
	}
	report.DBarOverK = fit.DBarOverK
	report.PD = fit.PD
	for k, i := range idx {
		st := EBStats{
			Theta:     fit.Theta[k],
			PostSigma: fit.PostSigma[k],
			Residual:  fit.Residual[k],
			Leverage:  fit.B[k],
		}
		st.Flag = st.Residual*st.Residual+st.Leverage > ebFlagContour
		pool[i].EB = &st
		mu[i] = st.Theta
		sigma[i] = st.PostSigma
	}
	return report
}

// RankResults ranks pool under the Gaussian utility model and returns the
// shortlist (ordered by expected rank) plus the pool-level report. It is
// the whole of the --shortlist path, exposed so offline evaluation can
// rank synthetic Results (heat score + confidence) without a Stream.
//
// The O(n²)–O(n³) rank pass is bounded to the strongest rankPoolCap forks
// by heat — a fork outside that pool would not make a small shortlist
// anyway. Options fields read: ShortlistN, ShortlistRule, RankDiagnostics,
// EB, PriorScale. Entries of pool within the cap are mutated (Rank, EB,
// ExpectedRank, RankConfidence set) and the returned slice aliases them.
func RankResults(pool []Result, opts Options) ([]Result, RankReport) {
	sort.SliceStable(pool, func(i, j int) bool {
		return pool[i].Heat.Score > pool[j].Heat.Score
	})
	n := len(pool)
	if n > rankPoolCap {
		n = rankPoolCap
	}
	pool = pool[:n]
	mu := make([]float64, n)
	sigma := make([]float64, n)
	for i := range pool {
		mu[i] = pool[i].Heat.Score
		sigma[i] = rankSigma(pool[i].Heat.Confidence)
	}
	var ebReport RankReport
	if opts.EB {
		ebReport = applyEB(pool, mu, sigma, opts.PriorScale)
	}
	p := winProbs(mu, sigma)
	stats := computeRankStatsFrom(p, opts.ShortlistN)
	var residuals []float64
	if opts.RankDiagnostics {
		residuals = pothResiduals(p)
	}
	for i := range pool {
		rs := stats[i]
		if residuals != nil {
			r := residuals[i]
			rs.PothResidual = &r
		}
		pool[i].Rank = &rs
		pool[i].ExpectedRank = rs.ExpectedRank
		pool[i].RankConfidence = pool[i].Heat.Confidence
	}
	rule := opts.ShortlistRule
	if rule == "" {
		rule = ShortlistRuleExpected
	}
	// picked is always the top ShortlistN under rule, in rule order — it
	// both drives CPOTHk (which must reflect the actual shortlist, never
	// the kept-all remainder) and, under RankKeepAll, seeds the front of
	// the returned slice so the rule's selection is still visible instead
	// of being flattened by a final by-expected-rank sort over everything.
	picked := selectShortlist(stats, rule, opts.ShortlistN)
	chosen := picked
	if opts.RankKeepAll {
		pickedSet := make(map[int]bool, len(picked))
		for _, i := range picked {
			pickedSet[i] = true
		}
		rest := make([]int, 0, n-len(picked))
		for i := 0; i < n; i++ {
			if !pickedSet[i] {
				rest = append(rest, i)
			}
		}
		sort.SliceStable(rest, func(x, y int) bool {
			a, b := rest[x], rest[y]
			if stats[a].ExpectedRank != stats[b].ExpectedRank {
				return stats[a].ExpectedRank < stats[b].ExpectedRank
			}
			return a < b
		})
		chosen = append(append([]int(nil), picked...), rest...)
	}
	nonzero := 0
	for _, m := range mu {
		if m > 0 {
			nonzero++
		}
	}
	report := RankReport{
		PoolSize:      n,
		NonzeroPool:   nonzero,
		ShortlistN:    opts.ShortlistN,
		ShortlistRule: rule,
		POTH:          poth(p),
		CPOTHk:        subsetPoth(p, picked),
		EBRegime:      ebReport.EBRegime,
		EBPool:        ebReport.EBPool,
		TauHat:        ebReport.TauHat,
		EBMean:        ebReport.EBMean,
		PriorScale:    ebReport.PriorScale,
		DBarOverK:     ebReport.DBarOverK,
		PD:            ebReport.PD,
	}
	bands := tieBands(mu, sigma, chosen, tieBandFactor)
	shortlist := make([]Result, 0, len(chosen))
	for k, i := range chosen {
		pool[i].Rank.TieBand = bands[k]
		shortlist = append(shortlist, pool[i])
	}
	return shortlist, report
}
