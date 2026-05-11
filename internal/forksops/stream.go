// Package forksops provides a library-callable streaming fork-discovery pipeline.
// It is the read-path for "spn forks list" NDJSON output.
// The existing internal/dump package is NOT modified by this package.
package forksops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/heat"
)

// Options controls the streaming pipeline.
type Options struct {
	Refresh      bool
	Tier         int // 1 = surface only, 2 = + compare, 3 = + contributors. 0 = full (3).
	TopN         int
	BotAllowlist map[string]bool
	HeatWeights  map[string]float64

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

	// Logger receives cluster-pipeline progress and warnings. May be nil
	// (defaults to io.Discard).
	Logger io.Writer
}

// ClusterOptions is the spn-side options struct for the cluster pipeline.
// Mirrors cluster.PipelineOptions but keeps the test seam unexported.
type ClusterOptions struct {
	Enabled         bool
	TopN            int
	Endpoint        string
	ModelOverride   string
	LabelerEndpoint string
	LabelerModel    string
	Epsilon         float64
	MinClusterSize  int
	AutoPull        bool
	NoPrompt        bool
	NonInteractive  bool
	Refresh         bool

	// Labeler, when non-nil, overrides automatic construction from
	// LabelerEndpoint + LabelerModel. Tests inject a stub directly via this
	// field; production callers usually pass LabelerEndpoint instead.
	Labeler cluster.Labeler

	// embedderForTest is the test seam for cluster integration tests. Tests
	// inject a stub embed.Embedder via SetEmbedderForTest; the field is
	// unexported so production callers cannot bypass SelectEmbedder.
	embedderForTest embed.Embedder
}

// defaultLabelerModel is used when --labeler is set but --labeler-model is not.
// Aliases cluster.DefaultLabelerModel so the literal lives in exactly one
// place — see n4 in the round-3 review.
const defaultLabelerModel = cluster.DefaultLabelerModel

// SetEmbedderForTest installs an embedder stub on ClusterOptions for tests.
// Production callers must not use this — they should go through SelectEmbedder.
func (o *ClusterOptions) SetEmbedderForTest(e embed.Embedder) { o.embedderForTest = e }

// Result is a single fork's outcome. Fork is always populated; Err and the
// T2/T3 pointers may be nil depending on tier and per-fork errors.
type Result struct {
	Fork forge.T1Data
	T2   *forge.T2Data
	T3   *forge.T3Data
	Heat heat.HeatResult
	Err  *Error

	// ClusterSkip is set when the cluster pipeline was enabled but skipped
	// for a non-fatal reason (embedder unreachable, no model, etc.). Only the
	// first Result in the batch carries it; downstream consumers fan out a
	// single user-facing warning. Nil when clustering ran or was disabled.
	ClusterSkip *ClusterSkip
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
	parent, err := provider.Parent(ctx, owner, repo)
	if err != nil {
		var rl *gh.RateLimitError
		if errors.As(err, &rl) {
			return nil, fmt.Errorf("rate_limited: reset_at=%s retry_after_seconds=%d: %w",
				rl.ResetAt.UTC().Format(time.RFC3339), rl.RetryAfterSeconds(), err)
		}
		return nil, fmt.Errorf("fetch parent: %w", err)
	}
	t1ch, err := provider.ListForks(ctx, owner, repo)
	if err != nil {
		var rl *gh.RateLimitError
		if errors.As(err, &rl) {
			return nil, fmt.Errorf("rate_limited: reset_at=%s retry_after_seconds=%d: %w",
				rl.ResetAt.UTC().Format(time.RFC3339), rl.RetryAfterSeconds(), err)
		}
		return nil, fmt.Errorf("list forks: %w", err)
	}

	logger := opts.Logger
	if logger == nil {
		logger = io.Discard
	}

	out := make(chan Result)
	go func() {
		defer close(out)

		var t1Forks []forge.T1Data
	drain:
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-t1ch:
				if !ok {
					break drain
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

		stats := makeStats(t1Forks)
		scorer := heat.NewScorer(stats)

		type scored struct {
			fork forge.T1Data
			res  heat.HeatResult
		}
		all := make([]scored, len(t1Forks))
		now := time.Now()
		for i, f := range t1Forks {
			input := buildScoreInput(f, parent, now)
			all[i] = scored{fork: f, res: scorer.ScoreRaw(input)}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].res.Score > all[j].res.Score })

		topN := opts.TopN
		if topN <= 0 || topN > len(all) {
			topN = len(all)
		}
		tier := opts.Tier
		if tier == 0 {
			tier = 3
		}
		concurrency := 4
		if a, _ := provider.Auth(ctx); a.Concurrency > 0 {
			concurrency = a.Concurrency
		}

		type rank struct {
			idx int
			s   scored
		}
		jobs := make(chan rank)
		go func() {
			defer close(jobs)
			for i, s := range all {
				select {
				case <-ctx.Done():
					return
				case jobs <- rank{idx: i, s: s}:
				}
			}
		}()

		// In streaming mode (no clustering) we forward results to `out` as
		// they finish. In batch mode (clustering enabled) we instead collect
		// them into `collected` (mu-guarded) and emit at the end.
		batchMode := opts.Cluster.Enabled
		var (
			collectedMu sync.Mutex
			collected   []Result
		)

		var wg sync.WaitGroup
		for w := 0; w < concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range jobs {
					i := j.idx
					s := j.s
					r := Result{Fork: s.fork, Heat: s.res}
					if tier >= 2 && i < topN {
						t2, terr := provider.Compare(ctx, s.fork, s.fork.DefaultBranch)
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
						}
					}
					if tier >= 3 && i < topN && r.Err == nil {
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
								r.Err = &Error{Code: "upstream_error", Message: terr.Error(), Details: map[string]any{"fork": s.fork.ID, "stage": "contributors"}}
							}
						} else {
							r.T3 = &t3
						}
					}
					r.Heat = rescore(scorer, s.fork, parent, now, r.T2, r.T3)
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

		if !batchMode {
			return
		}

		// Cluster pass: build EnrichedFork pointers over `collected`, run the
		// shared pipeline, then emit each Result. Heat is mutated in place via
		// the EnrichedFork pointer back into collected[i].Heat.
		skip := runForksClusterPipeline(ctx, provider, &parent, owner, repo, collected, opts.Cluster, logger)

		// Re-sort emitted output by heat desc to match dump's ordering.
		sort.SliceStable(collected, func(i, j int) bool {
			return collected[i].Heat.Score > collected[j].Heat.Score
		})

		for i, r := range collected {
			if i == 0 && skip != nil {
				r.ClusterSkip = skip
			}
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

	inputs := cluster.PipelineInputs{
		Provider:      providerName(ctx, provider),
		UpstreamOwner: owner,
		UpstreamRepo:  repoName,
		Upstream:      *parent,
		Forks:         enriched,
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

	labeler := opts.Labeler
	if labeler == nil && opts.LabelerEndpoint != "" {
		model := opts.LabelerModel
		if model == "" {
			model = defaultLabelerModel
		}
		labeler = &cluster.OllamaChatLabeler{
			Endpoint: opts.LabelerEndpoint,
			Model:    model,
		}
	}

	pipelineOpts := cluster.PipelineOptions{
		Enabled:         opts.Enabled,
		TopN:            opts.TopN,
		Endpoint:        opts.Endpoint,
		ModelOverride:   opts.ModelOverride,
		LabelerEndpoint: opts.LabelerEndpoint,
		Epsilon:         opts.Epsilon,
		MinClusterSize:  opts.MinClusterSize,
		AutoPull:        opts.AutoPull,
		NoPrompt:        opts.NoPrompt,
		NonInteractive:  opts.NonInteractive,
		Refresh:         opts.Refresh,
		Labeler:         labeler,
	}
	if opts.embedderForTest != nil {
		pipelineOpts.EmbedderForTest = opts.embedderForTest
	}

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
func makeStats(forks []forge.T1Data) []heat.ForkStats {
	stats := make([]heat.ForkStats, len(forks))
	for i, f := range forks {
		stats[i] = heat.ForkStats{
			ForkID:   int64(i),
			Stars:    f.Stars,
			SubForks: f.SubForkCount,
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
			Archived:          f.IsArchived,
			Now:               now,
		},
	}
}

// rescore rebuilds a ScoreInput including T2/T3 data and returns the
// updated HeatResult. Used to refresh Heat after enrichment.
func rescore(scorer *heat.Scorer, f forge.T1Data, parent forge.ParentData, now time.Time, t2 *forge.T2Data, t3 *forge.T3Data) heat.HeatResult {
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
		input.T3 = &heat.Tier3ParamsV2{
			CommitSpanDays: float64(t3.CommitSpanDays),
		}
	}
	// Wire v2 lone wolf when we have commits to analyze.
	if t2 != nil && len(t2.Commits) > 0 {
		lw := buildLoneWolfInput(f, now, t2)
		if input.T3 == nil {
			input.T3 = &heat.Tier3ParamsV2{}
		}
		input.T3.LoneWolf = heat.DetectLoneWolfV2(lw)
	}
	result := scorer.ScoreRaw(input)
	// Propagate the lone wolf result to the top-level HeatResult field so
	// callers (TUI, dump, JSON) can access it without digging into T3 params.
	if input.T3 != nil && input.T3.LoneWolf != nil {
		result.LoneWolfV2 = input.T3.LoneWolf
	}
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
		authors = append(authors, login)
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
