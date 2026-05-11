package cluster

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
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
	Enabled         bool   // false → skip clustering entirely
	TopN            int    // max forks to embed
	Endpoint        string // embedder URL; "" → default resolution
	ModelOverride   string // explicit model
	LabelerEndpoint string // optional LLM labeler endpoint (informational; CLI uses Labeler directly)
	Epsilon         float64
	MinClusterSize  int
	AutoPull        bool
	NoPrompt        bool
	NonInteractive  bool // true for --json / --csv / spn runs; false for TUI calls
	Refresh         bool // true → skip LoadCache, force a fresh clustering pass

	// Labeler, when non-nil, is invoked after heuristic labeling to polish
	// each non-noise cluster's label. Errors fall back silently to the
	// heuristic. Construction is the CLI's responsibility; the pipeline only
	// consumes the interface.
	Labeler Labeler

	// EmbedderForTest, when non-nil, skips the SelectEmbedder bootstrap and
	// uses the provided Embedder directly. Reserved for tests of the
	// orchestration logic; not exposed via CLI flags.
	EmbedderForTest embed.Embedder
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

	// Optional upstream-side context for the LLM labeler. UpstreamDesc and
	// UpstreamReadme are typically pre-populated by the caller; the pipeline
	// will additionally fetch the README via ReadmeFetcher (if available) when
	// Labeler is set and UpstreamReadme is empty. UpstreamCoreDirs is sourced
	// from the centrality pass below when not pre-set.
	UpstreamDesc     string
	UpstreamReadme   string
	UpstreamCoreDirs []string
}

// SkipReason is returned by RunPipeline when clustering was skipped for a
// non-fatal reason (e.g., embedder unreachable, missing model). Callers that
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

	// 0. Cache fast-path. Try to satisfy this pipeline from a previous run's
	//    saved clusters before doing any expensive work.
	if !opts.Refresh {
		if cached, ok := LoadCache(provider, inputs.UpstreamOwner, inputs.UpstreamRepo,
			opts.Endpoint, opts.ModelOverride); ok {
			applyAssignmentsToForks(cached.Clusters, cached.Assignments, inputs.Forks)
			fmt.Fprintf(logger, "[cluster] cache hit: %d clusters, %d assignments (skipping embed)\n",
				len(cached.Clusters), len(cached.Assignments))
			return nil, nil
		}
	}

	// 1. Bootstrap embedder.
	var embedder embed.Embedder
	var modelName string
	if opts.EmbedderForTest != nil {
		embedder = opts.EmbedderForTest
		modelName = opts.ModelOverride
	} else {
		var prompter embed.Prompter
		if !opts.NonInteractive {
			prompter = embed.NewStdinPrompter()
		}
		e, m, skip := embed.SelectEmbedder(ctx, embed.SelectOptions{
			Endpoint:       opts.Endpoint,
			ExplicitModel:  opts.ModelOverride,
			AutoPull:       opts.AutoPull,
			NoPrompt:       opts.NoPrompt,
			NonInteractive: opts.NonInteractive,
		}, prompter)
		if skip != nil {
			fmt.Fprintf(logger, "[cluster] skipping: %s\n", skip.Message)
			return &SkipReason{
				Code:     skip.Code,
				Message:  skip.Message,
				Endpoint: skip.Endpoint,
				Model:    skip.Model,
			}, nil
		}
		embedder = e
		modelName = m
	}

	// 2. Compute (or load) directory centrality.
	dc, dcOK := loadOrComputeCentrality(ctx, inputs, logger)

	// 2a. Fetch upstream README for the labeler when one is configured and
	// the caller did not pre-populate it. Failures are non-fatal — the labeler
	// just gets an empty README in that case.
	if opts.Labeler != nil && inputs.UpstreamReadme == "" && inputs.ReadmeFetcher != nil &&
		inputs.UpstreamOwner != "" && inputs.UpstreamRepo != "" {
		if r, err := inputs.ReadmeFetcher.FetchReadme(ctx, inputs.UpstreamOwner, inputs.UpstreamRepo); err == nil {
			if len(r) > readmeMaxBytes {
				r = r[:readmeMaxBytes]
			}
			inputs.UpstreamReadme = r
		} else {
			fmt.Fprintf(logger, "[cluster] upstream README fetch failed: %v (continuing)\n", err)
		}
	}
	// Fall back to ParentData.Description and centrality CoreDirs when the
	// caller hasn't set them.
	if inputs.UpstreamDesc == "" {
		inputs.UpstreamDesc = inputs.Upstream.Description
	}
	if len(inputs.UpstreamCoreDirs) == 0 && dcOK {
		inputs.UpstreamCoreDirs = dc.CoreDirs
	}

	// 3. Gate top-N forks for embedding.
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
	vecs, err := embed.MultiModalEmbed(ctx, embedder, modelName, features)
	if err != nil {
		msg := fmt.Sprintf("embedder failed: %v", err)
		fmt.Fprintf(logger, "[cluster] embedder failed: %v; skipping clustering\n", err)
		return &SkipReason{Code: "embedder_failed", Message: msg, Endpoint: opts.Endpoint, Model: modelName}, nil
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
		impact := changeImpactFor(dc, dcOK, paths)
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

	// 7. Run clustering.
	clusters, assignments := Run(points, Options{
		Epsilon:        opts.Epsilon,
		MinClusterSize: opts.MinClusterSize,
	})

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

	// 8a. Optional LLM polish over each non-noise cluster label. Errors fall
	//     back silently to the heuristic.
	if opts.Labeler != nil {
		upstreamRepo := fmt.Sprintf("%s/%s", inputs.UpstreamOwner, inputs.UpstreamRepo)
		for i := range clusters {
			if clusters[i].ID == "noise" {
				continue
			}
			members := membersForCluster(clusters[i], features, idxByForkID, 5)
			polished, err := opts.Labeler.Polish(ctx, LabelerContext{
				Heuristic:        clusters[i].Label,
				Members:          members,
				UpstreamRepo:     upstreamRepo,
				UpstreamDesc:     inputs.UpstreamDesc,
				UpstreamReadme:   inputs.UpstreamReadme,
				UpstreamCoreDirs: inputs.UpstreamCoreDirs,
			})
			if err != nil {
				fmt.Fprintf(logger, "[cluster] labeler polish failed for %s: %v (keeping heuristic)\n",
					clusters[i].ID, err)
				continue
			}
			if polished == "" {
				continue
			}
			clusters[i].Label = polished
		}
	}

	// 9. Write back cluster metadata into each candidate's HeatResult.
	applyAssignmentsToForks(clusters, assignments, candidates)

	// 10. Persist a cache entry. Failures are logged but non-fatal.
	if err := SaveCache(ClusterCache{
		SchemaVersion:    SchemaVersion,
		ComputedAt:       time.Now().UTC(),
		EmbedderModel:    modelName,
		EmbedderEndpoint: opts.Endpoint,
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
//
// Noise points (a.Cluster == "noise" or "") receive NoveltyScore = 1.0 from
// the assignment and the +5 heat bonus — they are "outliers, most novel" by
// definition. Only real clusters get ClusterID/ClusterLabel/ClusterMemberCount
// populated; noise points have those left at their assigned/zero values.
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
		hr.NoveltyScore = a.Novelty
		// Apply the novelty bonus to every assigned fork — including noise
		// points, which carry Novelty=1.0 by definition. The +5 max matches
		// the v2 NoveltyComponent's Max so the score stays comparable.
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

// loadOrComputeCentrality returns the upstream DirectoryCentrality. Tries the
// on-disk cache first; on miss, calls repo.Compute and saves the result.
// Returns (zero, false) when no tree source is available or the call fails.
func loadOrComputeCentrality(ctx context.Context, inputs PipelineInputs, logger io.Writer) (repo.DirectoryCentrality, bool) {
	if inputs.TreeSource == nil {
		return repo.DirectoryCentrality{}, false
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
		return repo.DirectoryCentrality{}, false
	}
	if saveErr := repo.SaveCache(dc); saveErr != nil {
		fmt.Fprintf(logger, "[cluster] centrality cache save failed: %v (non-fatal)\n", saveErr)
	}
	return dc, true
}

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

// membersForCluster returns up to `cap` member ForkFeatures for the given
// cluster, looked up by ForkID via idxByForkID. Order follows the cluster's
// Members list (which itself is deterministic — see cluster.Run).
func membersForCluster(c Cluster, features []embed.ForkFeatures, idxByForkID map[string]int, cap int) []embed.ForkFeatures {
	if cap <= 0 || len(c.Members) == 0 {
		return nil
	}
	out := make([]embed.ForkFeatures, 0, cap)
	for _, id := range c.Members {
		if len(out) >= cap {
			break
		}
		idx, ok := idxByForkID[id]
		if !ok || idx < 0 || idx >= len(features) {
			continue
		}
		out = append(out, features[idx])
	}
	return out
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

// changeImpactFor returns DirectoryCentrality.ScoreFork over the file paths
// touched by the fork. Returns 0 if DC is unavailable or no paths.
func changeImpactFor(dc repo.DirectoryCentrality, ok bool, paths []string) float32 {
	if !ok || len(paths) == 0 {
		return 0
	}
	return float32(dc.ScoreFork(paths))
}

// dirOf returns the immediate parent directory of p (e.g., "a/b/c.go" → "a/b").
func dirOf(p string) string {
	p = strings.TrimSpace(p)
	d := filepath.ToSlash(filepath.Dir(p))
	if d == "." || d == "/" {
		return ""
	}
	return d
}
