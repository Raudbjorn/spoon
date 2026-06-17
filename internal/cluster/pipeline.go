package cluster

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/mdg"
	"github.com/svnbjrn/spoon/internal/repo"
)

// NOTE: dump.ClusterOptions, forksops.ClusterOptions, and tui.ClusterOptions
// each re-declare similar fields and translate to PipelineOptions. A future
// cleanup should collapse these into one canonical struct used by all three
// call sites.

// PipelineOptions carries the CLI-derived cluster pipeline configuration.
// This is the single source of truth shared by both internal/dump (used by
// the `spoon --json/--csv` and TUI paths) and internal/forksops (used by
// `spn forks list`).
type PipelineOptions struct {
	Enabled        bool // false → skip clustering entirely
	TopN           int  // max forks to embed
	Epsilon        float64
	MinClusterSize int
	Refresh        bool // true → skip LoadCache, force a fresh clustering pass

	// Embedder, when non-nil, replaces the built-in lexical embedder. Used
	// by the openvino backend (constructed in the CLI layer, which owns its
	// lifecycle) and by tests.
	Embedder embed.Embedder

	// EmbedderID identifies the embedder for cluster-cache keying. Must be
	// set whenever Embedder is (e.g. embed.OpenVINOConfig.EmbedderID());
	// empty means the built-in lexical embedder.
	EmbedderID string

	// LabelPolisher, when non-nil, rewrites each non-noise cluster's
	// heuristic label (in-process LLM). Errors fall back silently to the
	// heuristic.
	LabelPolisher LabelPolisher

	// Categorize enables zero-shot category assignment (ClassifyForks) for
	// the embedded candidates. Callers gate this on a semantic embedder —
	// the lexical backend makes anchor matching meaningless.
	Categorize bool

	// CentralityBackend chooses the ChangeImpact computation. "" or
	// "directory" → the cheap directory-centrality proxy. "mdg" → the full
	// Module Dependency Graph (requires a local clone; falls back silently to
	// the directory proxy when unavailable).
	CentralityBackend string

	// CentralityRepoPath, when non-empty and CentralityBackend == "mdg", is
	// the on-disk path to a clone of the upstream. When empty, the pipeline
	// performs a shallow clone to a tempdir.
	CentralityRepoPath string

	// CentralityHeadSHA, when non-empty, is the upstream default-branch SHA;
	// used by the MDG cache to invalidate stale entries.
	CentralityHeadSHA string

	// StrictMDG, when true, makes loadOrComputeCentrality return an error
	// to the caller on MDG build/cache failure instead of silently falling
	// back to the directory proxy. The error is surfaced as a ClusterSkip
	// with code "mdg_unavailable" so callers can decide whether to treat
	// it as fatal. Off by default — the silent fallback is the right
	// behavior for ordinary `--full-mdg` runs that just want *some*
	// ChangeImpact signal.
	StrictMDG bool
}

// EnrichedFork pairs a fork's T1+T2 data with its HeatResult so the pipeline
// can write back ClusterID / ClusterLabel / NoveltyScore / ChangeImpact /
// ClusterMemberCount.
//
// Heat is mutated in-place when clustering runs. Score is also mutated when
// novelty is non-zero: heat.ApplyNoveltyToScore adds up to +5 (capped at 100).
// See RunPipeline docs.
type EnrichedFork struct {
	T1   forge.T1Data
	T2   *forge.T2Data
	Heat *heat.HeatResult
}

// ReadmeFetcher fetches a README for owner/repo. Implementations typically wrap
// the GitHub client's FetchReadme. May return ("", nil) when no README exists.
type ReadmeFetcher interface {
	FetchReadme(ctx context.Context, owner, repo string) (string, error)
}

// PipelineInputs are the inputs to the clustering pass after T2 enrichment.
type PipelineInputs struct {
	Provider      string // "github" / "gitlab" / "other" — used for cache key
	UpstreamOwner string
	UpstreamRepo  string
	Upstream      forge.ParentData

	Forks []EnrichedFork // T1+T2 enriched, Heat populated

	// Optional sources for the upstream-side centrality pass. When any is nil,
	// the pipeline skips centrality (ChangeImpact = 0 for all forks).
	TreeSource    repo.TreeSource
	CommitSource  repo.CommitSource
	ReadmeFetcher ReadmeFetcher
}

// SkipReason is returned by RunPipeline when clustering was skipped for a
// non-fatal reason (e.g., no eligible forks). Callers that
// want to surface a structured warning to the user can inspect Code/Endpoint/
// Model. When the pipeline ran to completion SkipReason is nil.
type SkipReason struct {
	Code     string
	Message  string
	Endpoint string
	Model    string
}

// RunPipeline performs the post-T2 cluster orchestration shared by every
// non-interactive cluster path (spoon --json/--csv, spn forks list, future TUI).
//
// Score semantics: this pipeline populates the cluster fields on each fork's
// HeatResult (ClusterID, ClusterLabel, NoveltyScore, ChangeImpact,
// ClusterMemberCount) AND folds the novelty bonus into HeatResult.Score via
// heat.ApplyNoveltyToScore (up to +5 points, capped at 100). The original
// scoring pass's trust multiplier and penalties remain applied; the novelty
// contribution lands on top. For v2 callers that compute novelty directly via
// NoveltyComponent in their own ScoreRaw, do not route through this pipeline
// — the bonus would double-count.
//
// Returns (skipReason, error). skipReason is non-nil when the pipeline was
// intentionally skipped for a non-fatal reason. error is reserved for hard,
// propagating failures (context cancellation, etc.).
//
//nolint:revive // function is large by design — it's the integration layer.
func RunPipeline(ctx context.Context, opts PipelineOptions, inputs PipelineInputs, logger io.Writer) (*SkipReason, error) {
	if !opts.Enabled {
		return &SkipReason{Code: "disabled", Message: "clustering disabled"}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	provider := inputs.Provider
	if provider == "" {
		provider = "github"
	}

	modelName := opts.EmbedderID
	if modelName == "" {
		modelName = embed.BuiltinModelName
	}

	// 0. Cache fast-path. Try to satisfy this pipeline from a previous run's
	//    saved clusters before doing any expensive work.
	if !opts.Refresh {
		if cached, ok := LoadCache(provider, inputs.UpstreamOwner, inputs.UpstreamRepo,
			"", modelName); ok {
			applyAssignmentsToForks(cached.Clusters, cached.Assignments, inputs.Forks)
			fmt.Fprintf(logger, "[cluster] cache hit: %d clusters, %d assignments (skipping embed)\n",
				len(cached.Clusters), len(cached.Assignments))
			return nil, nil
		}
	}

	// 1. No embedder bootstrap is needed: both backends run in-process and
	//    were constructed before the pipeline started.
	var embedder embed.Embedder = embed.LocalEmbedder{}
	if opts.Embedder != nil {
		embedder = opts.Embedder
	}

	// 2. Compute (or load) centrality (directory proxy or MDG, per opts).
	c, cOK, err := loadOrComputeCentrality(ctx, opts, inputs, logger)
	if err != nil {
		// Strict mode: surface MDG failure as a non-fatal skip so callers
		// can render the policy_violation envelope.
		fmt.Fprintf(logger, "[cluster] strict mode: %v\n", err)
		return &SkipReason{Code: "mdg_unavailable", Message: err.Error()}, nil
	}

	candidates := SelectClusterCandidates(inputs.Forks, opts.TopN)
	if len(candidates) == 0 {
		fmt.Fprintln(logger, "[cluster] no eligible forks after filtering")
		return &SkipReason{Code: "no_eligible_forks", Message: "no eligible forks"}, nil
	}

	// 4. Build per-fork ForkFeatures (with optional README fetch).
	features := make([]embed.ForkFeatures, len(candidates))
	for i, ef := range candidates {
		var readme string
		if inputs.ReadmeFetcher != nil && ef.T1.Owner != "" && ef.T1.Name != "" {
			if r, err := inputs.ReadmeFetcher.FetchReadme(ctx, ef.T1.Owner, ef.T1.Name); err == nil {
				readme = r
			}
		}
		var t2 forge.T2Data
		if ef.T2 != nil {
			t2 = *ef.T2
		}
		features[i] = embed.BuildFeatures(t2, readme, 0)
	}

	// 5. Multi-modal embed.
	vecs, err := embed.MultiModalEmbed(ctx, embedder, features)
	if err != nil {
		msg := fmt.Sprintf("embedder failed: %v", err)
		fmt.Fprintf(logger, "[cluster] embedder failed: %v; skipping clustering\n", err)
		return &SkipReason{Code: "embedder_failed", Message: msg, Model: modelName}, nil
	}
	if len(vecs) != len(candidates) {
		msg := fmt.Sprintf("embedder returned %d vecs for %d forks", len(vecs), len(candidates))
		fmt.Fprintf(logger, "[cluster] %s; skipping\n", msg)
		return &SkipReason{Code: "embedder_vector_mismatch", Message: msg}, nil
	}

	// 6. Build weak signals and append scaled signal vectors. Also stash the
	//    ChangeImpact per-fork onto its HeatResult before clustering runs, so
	//    callers can surface it independently of cluster membership.
	const weakSignalScale = float32(0.05)
	points := make([]Point, 0, len(candidates))
	for i, ef := range candidates {
		paths := pathsOf(ef.T2)
		impact := changeImpactFor(c, cOK, paths)
		if ef.Heat != nil {
			ef.Heat.ChangeImpact = float64(impact)
		}
		signals := BuildWeakSignals(WeakSignalInput{
			Paths:        paths,
			PrimaryLang:  ef.T1.Language,
			RepoName:     ef.T1.ID,
			TotalAdds:    sumAdditions(ef.T2),
			TotalDels:    sumDeletions(ef.T2),
			ChangeImpact: impact,
		})
		signalVec := SignalsToFeatureVec(signals)
		combined := make([]float32, 0, len(vecs[i])+len(signalVec))
		combined = append(combined, vecs[i]...)
		for _, x := range signalVec {
			combined = append(combined, x*weakSignalScale)
		}
		points = append(points, Point{
			ForkID: ef.T1.ID,
			Vec:    combined,
		})
	}

	// 5a. Zero-shot categories (advisory). Failures degrade silently to
	//     uncategorized output.
	var categories []string
	var categoryScores []float64
	if opts.Categorize {
		var cerr error
		categories, categoryScores, cerr = ClassifyForks(ctx, embedder, features)
		if cerr != nil {
			fmt.Fprintf(logger, "[cluster] classification failed: %v (continuing uncategorized)\n", cerr)
			categories, categoryScores = nil, nil
		}
	}

	// 7. Run clustering.
	clusters, assignments := Run(points, Options{
		Epsilon:        opts.Epsilon,
		MinClusterSize: opts.MinClusterSize,
	})

	// 7a. Attach categories to assignments (by fork ID) so they ride the
	//     cluster cache and the shared write-back path.
	if categories != nil {
		catByID := make(map[string]int, len(candidates))
		for i, ef := range candidates {
			catByID[ef.T1.ID] = i
		}
		for i := range assignments {
			if idx, ok := catByID[assignments[i].ForkID]; ok && idx < len(categories) {
				assignments[i].Category = categories[idx]
				assignments[i].CategoryScore = categoryScores[idx]
			}
		}
	}

	// 8. Label each non-noise cluster.
	idxByForkID := make(map[string]int, len(candidates))
	for i, ef := range candidates {
		idxByForkID[ef.T1.ID] = i
	}
	labels := labelClusters(clusters, features, idxByForkID)
	for i := range clusters {
		if clusters[i].ID == "noise" {
			continue
		}
		clusters[i].Label = labels[clusters[i].ID]
	}

	// 8a. Optional label polish over each non-noise cluster. Errors keep
	//     the heuristic label.
	if opts.LabelPolisher != nil {
		upstreamRepo := inputs.UpstreamOwner + "/" + inputs.UpstreamRepo
		for i := range clusters {
			if clusters[i].ID == "noise" {
				continue
			}
			hint := PolishHint{
				Heuristic:    clusters[i].Label,
				UpstreamRepo: upstreamRepo,
			}
			hint.SampleCommits, hint.SamplePaths = sampleClusterTexts(clusters[i], features, idxByForkID, 6)
			polished, perr := opts.LabelPolisher.PolishLabel(ctx, hint)
			if perr != nil {
				fmt.Fprintf(logger, "[cluster] label polish failed for %s: %v (keeping heuristic)\n", clusters[i].ID, perr)
				continue
			}
			fmt.Fprintf(logger, "[cluster] polish %s: %q (heuristic %q)\n", clusters[i].ID, polished, hint.Heuristic)
			if polished != "" {
				clusters[i].Label = polished
			}
		}
	}

	// 9. Write back cluster metadata into each candidate's HeatResult.
	applyAssignmentsToForks(clusters, assignments, candidates)

	// 10. Persist a cache entry. Failures are logged but non-fatal.
	if err := SaveCache(ClusterCache{
		SchemaVersion:    SchemaVersion,
		ComputedAt:       time.Now().UTC(),
		EmbedderModel:    modelName,
		EmbedderEndpoint: "",
		Provider:         provider,
		Owner:            inputs.UpstreamOwner,
		Repo:             inputs.UpstreamRepo,
		Epsilon:          opts.Epsilon,
		MinClusterSize:   opts.MinClusterSize,
		TopM:             opts.TopN,
		Clusters:         clusters,
		Assignments:      assignments,
	}); err != nil {
		fmt.Fprintf(logger, "[cluster] cache save failed: %v (non-fatal)\n", err)
	}
	fmt.Fprintf(logger, "[cluster] embedded %d forks → %d clusters (incl. noise)\n",
		len(candidates), len(clusters))
	return nil, nil
}

// applyAssignmentsToForks writes ClusterID / ClusterLabel / NoveltyScore /
// ClusterMemberCount onto each fork's HeatResult, sourced from a cluster +
// assignment list. Used both on a fresh clustering pass and on a cache hit
// so the write-back logic stays in one place.
//
// After populating NoveltyScore, it folds the novelty contribution into
// HeatResult.Score via heat.ApplyNoveltyToScore (up to +5, capped at 100).
// Score is therefore mutated. Idempotency is the caller's responsibility:
// this helper must be invoked exactly once per HeatResult per run.
// Noise points (a.Cluster == "noise" or "") receive NoveltyScore = 1.0
// from the assignment by default and the +5 heat bonus — they are
// "outliers, most novel" by definition. Empty-fork demotion (R3,
// below) demotes a subset of these to 0.5. Only real clusters get
// ClusterID / ClusterLabel / ClusterMemberCount populated; noise
// points have those left at their assigned/zero values.
//
// Empty-fork demotion (R3): noise points with no T2 data signal (MNA == 0
// AND AheadCount == 0) are demoted to NoveltyScore = 0.5 so that "isolated
// but empty" is not indistinguishable from "isolated and novel". The 0.5
// value is the only magic number in this helper — it is the floor of the
// "interesting but unproven" zone and is pinned by the regression test
// TestApplyNoveltyToScore_EmptyNoise in internal/heat/novelty_test.go
// (math) and TestApplyAssignmentsToForks_EmptyNoiseDemotion
// (this function's branch) in internal/cluster/pipeline_test.go.
func applyAssignmentsToForks(clusters []Cluster, assignments []Assignment, forks []EnrichedFork) {
	idxByForkID := make(map[string]int, len(forks))
	for i, ef := range forks {
		idxByForkID[ef.T1.ID] = i
	}
	labels := make(map[string]string, len(clusters))
	memberCounts := make(map[string]int, len(clusters))
	for _, c := range clusters {
		if c.ID == "noise" {
			continue
		}
		labels[c.ID] = c.Label
		memberCounts[c.ID] = len(c.Members)
	}
	for _, a := range assignments {
		i, ok := idxByForkID[a.ForkID]
		if !ok {
			continue
		}
		hr := forks[i].Heat
		if hr == nil {
			continue
		}

		// Empty-fork demotion (R3): noise forks with no T2 data signal
		// get a clamped novelty of 0.5 instead of the unconditional 1.0.
		// This preserves the +5 bonus for genuinely novel forks while
		// suppressing the false positive that an empty fork would
		// otherwise receive.
		novelty := a.Novelty
		if (a.Cluster == "noise" || a.Cluster == "") && isEmptyNoiseFork(forks[i].T2) {
			novelty = 0.5
		}
		hr.NoveltyScore = novelty
		hr.Category = a.Category
		hr.CategoryScore = a.CategoryScore
		// Apply the novelty bonus to every assigned fork — including
		// noise points, which carry Novelty=1.0 (or 0.5 for empty
		// forks) by definition. The +5 max matches the v2
		// NoveltyComponent's Max so the score stays comparable.
		heat.ApplyNoveltyToScore(hr)
		if a.Cluster == "noise" || a.Cluster == "" {
			hr.ClusterID = a.Cluster
			hr.ClusterLabel = ""
			hr.ClusterMemberCount = 0
			continue
		}
		hr.ClusterID = a.Cluster
		hr.ClusterLabel = labels[a.Cluster]
		hr.ClusterMemberCount = memberCounts[a.Cluster]
	}
}

// isEmptyNoiseFork reports whether a fork has no T2 divergence signal:
// T2 is nil, or both MNA and AheadCount are zero. The condition is
// intentionally permissive: false negatives (true novel forks demoted to
// 0.5) are less costly than false positives (empty forks rewarded as
// novel with the full +5).
func isEmptyNoiseFork(t2 *forge.T2Data) bool {
	if t2 == nil {
		return true
	}
	return t2.MNA == 0 && t2.AheadCount == 0
}

// loadOrComputeCentrality returns the centrality backend. Dispatch is by
// PipelineOptions.CentralityBackend with silent fallback to the directory
// proxy when MDG cannot run — unless opts.StrictMDG is set, in which case
// an MDG failure is returned as a non-nil error and the caller decides
// how to surface it (typically a SkipReason with code "mdg_unavailable").
func loadOrComputeCentrality(
	ctx context.Context,
	opts PipelineOptions,
	inputs PipelineInputs,
	logger io.Writer,
) (repo.Centrality, bool, error) {
	if opts.CentralityBackend == "mdg" {
		c, ok, err := loadOrComputeMDG(ctx, opts, inputs, logger)
		if ok {
			return c, true, nil
		}
		// Strict mode short-circuits BEFORE the fallback log: when
		// StrictMDG is set, the directory proxy is never consulted,
		// so the "falling back" message would be misleading. The
		// error is propagated and the caller renders it as a
		// ClusterSkip with code "mdg_unavailable".
		if opts.StrictMDG && err != nil {
			return nil, false, err
		}
		fmt.Fprintln(logger, "[cluster] MDG centrality unavailable; falling back to directory proxy")
	}
	dc, ok := loadOrComputeDirCentrality(ctx, inputs, logger)
	return dc, ok, nil
}

// loadOrComputeDirCentrality is the existing directory-proxy path, extracted
// as a backend for the dispatcher above.
func loadOrComputeDirCentrality(
	ctx context.Context,
	inputs PipelineInputs,
	logger io.Writer,
) (repo.Centrality, bool) {
	if inputs.TreeSource == nil {
		return nil, false
	}
	provider := inputs.Provider
	if provider == "" {
		provider = "github"
	}
	if dc, ok := repo.LoadCache(provider, inputs.UpstreamOwner, inputs.UpstreamRepo); ok {
		return dc, true
	}
	dc, err := repo.Compute(ctx, inputs.TreeSource, inputs.CommitSource, provider,
		inputs.UpstreamOwner, inputs.UpstreamRepo, 200)
	if err != nil {
		fmt.Fprintf(logger, "[cluster] centrality unavailable (%v); continuing without ChangeImpact\n", err)
		return nil, false
	}
	if saveErr := repo.SaveCache(dc); saveErr != nil {
		fmt.Fprintf(logger, "[cluster] centrality cache save failed: %v (non-fatal)\n", saveErr)
	}
	return dc, true
}

// loadOrComputeMDG attempts to build and cache an MDG-backed centrality.
// Returns:
//   - (c, true, nil)   on success (cache hit or fresh build).
//   - (nil, false, err) on a recoverable build/cache failure: the
//     err is non-nil for any tempdir/clone/build failure and is
//     propagated upward by the dispatcher when opts.StrictMDG is set.
//     When opts.StrictMDG is false, the dispatcher swallows the
//     error and falls back to the directory proxy.
//   - (nil, false, nil) when MDG was requested but opts.CentralityHeadSHA
//     was empty (the cache cannot pin against an empty SHA).
// The error contract is asymmetric: callers must check ok first, then
// err. A nil err with ok=false is a soft "skip MDG" signal, not a
// clean success.
//
// When opts.CentralityHeadSHA is non-empty, a cache hit short-circuits
// the live build entirely. When empty, the cache is neither consulted
// nor populated — the computed result is returned without persistence.
func loadOrComputeMDG(
	ctx context.Context,
	opts PipelineOptions,
	inputs PipelineInputs,
	logger io.Writer,
) (repo.Centrality, bool, error) {
	provider := inputs.Provider
	if provider == "" {
		provider = "github"
	}
	// Cache fast-path. Requires a non-empty HeadSHA — the cache pinned
	// that SHA at build time, and an empty SHA can't match.
	if opts.CentralityHeadSHA != "" {
		if cached, ok := mdg.LoadMDGCache(provider, inputs.UpstreamOwner, inputs.UpstreamRepo, opts.CentralityHeadSHA); ok {
			fmt.Fprintf(logger, "[cluster] mdg cache hit for %s/%s @ %s\n",
				inputs.UpstreamOwner, inputs.UpstreamRepo, opts.CentralityHeadSHA)
			return &mdgCachedAdapter{cache: cached}, true, nil
		}
	}
	repoPath := opts.CentralityRepoPath
	cleanup := func() {}
	if repoPath == "" {
		tmp, err := os.MkdirTemp("", "spoon-mdg-")
		if err != nil {
			fmt.Fprintf(logger, "[cluster] mdg tempdir: %v\n", err)
			return nil, false, fmt.Errorf("create tempdir for mdg: %w", err)
		}
		cleanup = func() { _ = os.RemoveAll(tmp) }
		if err := mdg.ShallowClone(ctx, provider, inputs.UpstreamOwner, inputs.UpstreamRepo, tmp); err != nil {
			fmt.Fprintf(logger, "[cluster] mdg shallow clone failed: %v\n", err)
			cleanup()
			return nil, false, fmt.Errorf("shallow clone upstream: %w", err)
		}
		repoPath = tmp
	}
	defer cleanup()

	c, err := mdg.BuildCentrality(ctx, repoPath, provider,
		inputs.UpstreamOwner, inputs.UpstreamRepo, opts.CentralityHeadSHA, mdg.BuildOptions{})
	if err != nil {
		fmt.Fprintf(logger, "[cluster] mdg build failed: %v\n", err)
		return nil, false, fmt.Errorf("build mdg: %w", err)
	}
	// Persist cache for next run. Skip persistence when HeadSHA is
	// empty — without it, the next load() can never match.
	if opts.CentralityHeadSHA != "" {
		if err := mdg.SaveMDGCache(mdg.MDGCache{
			SchemaVersion: mdg.CacheSchemaVersion,
			Provider:      provider,
			Owner:         inputs.UpstreamOwner,
			Repo:          inputs.UpstreamRepo,
			HeadSHA:       opts.CentralityHeadSHA,
			ComputedAt:    c.When(),
			Scores:        c.Scores,
		}); err != nil {
			fmt.Fprintf(logger, "[cluster] mdg cache save failed: %v (non-fatal)\n", err)
		}
	}
	return c, true, nil
}

// mdgCachedAdapter wraps a cached MDGCache as a repo.Centrality. It serves
// only the persisted score table keyed by module paths (e.g.,
// "example.com/m/internal/auth"). ScoreFork therefore requires callers to
// supply module paths, not file paths; real touched-file inputs (e.g.,
// "internal/auth/oauth.go") will score 0 — known degraded fidelity accepted
// by design for the cache-hit path. There is no recompute fallback inside
// the adapter; cache misses are detected upstream in loadOrComputeMDG.
type mdgCachedAdapter struct {
	cache mdg.MDGCache
}

var _ repo.Centrality = (*mdgCachedAdapter)(nil)

func (a *mdgCachedAdapter) ScoreFork(touchedFiles []string) float64 {
	if len(touchedFiles) == 0 || len(a.cache.Scores) == 0 {
		return 0
	}
	var top float64
	for _, v := range a.cache.Scores {
		if v > top {
			top = v
		}
	}
	if top <= 0 {
		return 0
	}
	var sum float64
	var n int
	for _, f := range touchedFiles {
		if v, ok := a.cache.Scores[f]; ok {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	mean := (sum / float64(n)) / top
	if mean > 1 {
		mean = 1
	}
	return mean
}

func (a *mdgCachedAdapter) Core() []string {
	type kv struct {
		k string
		v float64
	}
	pairs := make([]kv, 0, len(a.cache.Scores))
	for k, v := range a.cache.Scores {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	out := make([]string, 0, 10)
	for i := 0; i < len(pairs) && i < 10; i++ {
		out = append(out, pairs[i].k)
	}
	return out
}

func (a *mdgCachedAdapter) When() time.Time { return a.cache.ComputedAt }

// SelectClusterCandidates orders forks by current heat score desc, filters
// out archived and no-ahead forks, then caps at topN. Stable sort preserves
// input order on ties.
func SelectClusterCandidates(forks []EnrichedFork, topN int) []EnrichedFork {
	eligible := make([]EnrichedFork, 0, len(forks))
	for _, ef := range forks {
		if ef.Heat == nil {
			continue
		}
		if ef.T1.IsArchived {
			continue
		}
		if ef.T2 != nil && ef.T2.AheadCount == 0 {
			continue
		}
		eligible = append(eligible, ef)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		return eligible[i].Heat.Score > eligible[j].Heat.Score
	})
	if topN > 0 && topN < len(eligible) {
		eligible = eligible[:topN]
	}
	return eligible
}

// labelClusters runs HeuristicLabel for each non-noise cluster.
func labelClusters(clusters []Cluster, features []embed.ForkFeatures, idxByForkID map[string]int) map[string]string {
	out := make(map[string]string, len(clusters))
	for _, c := range clusters {
		if c.ID == "noise" {
			continue
		}
		memberSet := make(map[string]struct{}, len(c.Members))
		for _, m := range c.Members {
			memberSet[m] = struct{}{}
		}
		var members, corpus []embed.ForkFeatures
		for forkID, idx := range idxByForkID {
			if _, ok := memberSet[forkID]; ok {
				members = append(members, features[idx])
			} else {
				corpus = append(corpus, features[idx])
			}
		}
		out[c.ID] = HeuristicLabel(members, corpus)
	}
	return out
}

// pathsOf returns the flat list of file paths from a T2's Diffs, or nil.
func pathsOf(t2 *forge.T2Data) []string {
	if t2 == nil {
		return nil
	}
	out := make([]string, 0, len(t2.Diffs))
	for _, d := range t2.Diffs {
		if d.Path != "" {
			out = append(out, d.Path)
		}
	}
	return out
}

func sumAdditions(t2 *forge.T2Data) int {
	if t2 == nil {
		return 0
	}
	n := 0
	for _, d := range t2.Diffs {
		n += d.Additions
	}
	return n
}

func sumDeletions(t2 *forge.T2Data) int {
	if t2 == nil {
		return 0
	}
	n := 0
	for _, d := range t2.Diffs {
		n += d.Deletions
	}
	return n
}

// changeImpactFor returns Centrality.ScoreFork over the file paths touched by
// the fork. Returns 0 if centrality is unavailable or no paths.
func changeImpactFor(c repo.Centrality, ok bool, paths []string) float32 {
	if !ok || c == nil || len(paths) == 0 {
		return 0
	}
	return float32(c.ScoreFork(paths))
}

// sampleClusterTexts collects up to n member commit subjects and up to n
// member file paths for a cluster, for the label polisher's context.
func sampleClusterTexts(c Cluster, features []embed.ForkFeatures, idxByForkID map[string]int, n int) (commits, paths []string) {
	for _, id := range c.Members {
		idx, ok := idxByForkID[id]
		if !ok || idx < 0 || idx >= len(features) {
			continue
		}
		f := features[idx]
		for _, line := range strings.Split(f.Commits, "\n") {
			if line = strings.TrimSpace(line); line != "" && len(commits) < n {
				commits = append(commits, line)
			}
		}
		for _, line := range strings.Split(f.Paths, "\n") {
			if line = strings.TrimSpace(line); line != "" && len(paths) < n {
				paths = append(paths, line)
			}
		}
		if len(commits) >= n && len(paths) >= n {
			break
		}
	}
	return commits, paths
}
