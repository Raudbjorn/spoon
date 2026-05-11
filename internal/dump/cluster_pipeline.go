package dump

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/repo"
)

// ClusterOptions carries the CLI-derived cluster pipeline configuration.
type ClusterOptions struct {
	Enabled         bool   // false → skip clustering entirely
	TopN            int    // max forks to embed
	Endpoint        string // embedder URL; "" → default resolution
	ModelOverride   string // explicit model
	LabelerEndpoint string // optional LLM labeler endpoint (not used yet)
	Epsilon         float64
	MinClusterSize  int
	AutoPull        bool
	NoPrompt        bool
	NonInteractive  bool // true for --json / --csv runs; false for TUI calls

	// embedderForTest, when non-nil, skips the SelectEmbedder bootstrap and
	// uses the provided Embedder directly. Reserved for internal tests of the
	// orchestration logic; not exposed via CLI flags.
	embedderForTest embed.Embedder
}

// EnrichedFork pairs a fork's existing T1+T2 data with its HeatResult so the
// pipeline can write back ClusterID / ClusterLabel / NoveltyScore / etc.
//
// Heat is mutated in-place when clustering runs. The Score field is NOT
// modified — see RunClusterPipeline docs.
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

// ClusterInputs are the inputs to the clustering pass after T2 enrichment.
type ClusterInputs struct {
	Provider     string         // "github" / "gitlab" — used for cache key
	UpstreamOwner string
	UpstreamRepo  string
	Upstream     forge.ParentData

	Forks []EnrichedFork // T1+T2 enriched, Heat populated

	// Optional sources for the upstream-side centrality pass. When any is nil,
	// the pipeline skips centrality (ChangeImpact = 0 for all forks).
	TreeSource    repo.TreeSource
	CommitSource  repo.CommitSource
	ReadmeFetcher ReadmeFetcher
}

// runClusterPipeline performs the post-T2 cluster orchestration. See package
// godoc on the function for the step-by-step description and limitations.
//
// IMPORTANT — score semantics: this pipeline populates the new cluster fields
// on each fork's HeatResult (ClusterID, ClusterLabel, NoveltyScore,
// ClusterMemberCount) but does NOT mutate HeatResult.Score. The existing score
// was computed (and trust/penalty-adjusted) before clustering ran, and
// retroactively applying the novelty bonus through the trust multiplier is
// brittle. A future task may revisit this trade-off.
//
// Returns (skipReason, error). skipReason is a non-error human-readable string
// describing why clustering was skipped (e.g., "embedder unreachable"); empty
// means the pipeline ran end-to-end. error is reserved for hard, propagating
// failures (context cancellation, etc.).
//
//nolint:revive // function is large by design — it's the integration layer.
func runClusterPipeline(ctx context.Context, opts ClusterOptions, inputs ClusterInputs, logger io.Writer) (string, error) {
	if !opts.Enabled {
		return "clustering disabled", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// 1. Bootstrap embedder.
	var embedder embed.Embedder
	var modelName string
	if opts.embedderForTest != nil {
		embedder = opts.embedderForTest
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
			return skip.Message, nil
		}
		embedder = e
		modelName = m
	}

	// 2. Compute (or load) directory centrality. Failures are non-fatal:
	//    ChangeImpact = 0 for all forks.
	dc, dcOK := loadOrComputeCentrality(ctx, opts, inputs, logger)

	// 3. Gate top-N forks for embedding.
	candidates := selectClusterCandidates(inputs.Forks, opts.TopN)
	if len(candidates) == 0 {
		fmt.Fprintln(logger, "[cluster] no eligible forks after filtering")
		return "no eligible forks", nil
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
		fmt.Fprintf(logger, "[cluster] embedder failed: %v; skipping clustering\n", err)
		return fmt.Sprintf("embedder failed: %v", err), nil
	}
	if len(vecs) != len(candidates) {
		fmt.Fprintf(logger, "[cluster] embedder returned %d vecs for %d forks; skipping\n", len(vecs), len(candidates))
		return "embedder vector count mismatch", nil
	}

	// 6. Build weak signals and append scaled signal vectors.
	const weakSignalScale = float32(0.05)
	points := make([]cluster.Point, 0, len(candidates))
	for i, ef := range candidates {
		paths := pathsOf(ef.T2)
		impact := changeImpactFor(dc, dcOK, paths)
		signals := cluster.BuildWeakSignals(cluster.WeakSignalInput{
			Paths:        paths,
			PrimaryLang:  ef.T1.Language,
			RepoName:     ef.T1.ID,
			TotalAdds:    sumAdditions(ef.T2),
			TotalDels:    sumDeletions(ef.T2),
			ChangeImpact: impact,
		})
		signalVec := cluster.SignalsToFeatureVec(signals)
		combined := make([]float32, 0, len(vecs[i])+len(signalVec))
		combined = append(combined, vecs[i]...)
		for _, x := range signalVec {
			combined = append(combined, x*weakSignalScale)
		}
		points = append(points, cluster.Point{
			ForkID: ef.T1.ID,
			Vec:    combined,
		})
	}

	// 7. Run clustering.
	clusters, assignments := cluster.Run(points, cluster.Options{
		Epsilon:        opts.Epsilon,
		MinClusterSize: opts.MinClusterSize,
	})

	// 8. Label each non-noise cluster.
	idxByForkID := make(map[string]int, len(candidates))
	for i, ef := range candidates {
		idxByForkID[ef.T1.ID] = i
	}
	labels := labelClusters(clusters, features, idxByForkID)

	// 9. Write back cluster metadata into each candidate's HeatResult.
	memberCounts := make(map[string]int, len(clusters))
	for _, c := range clusters {
		if c.ID == "noise" {
			continue
		}
		memberCounts[c.ID] = len(c.Members)
	}
	for _, a := range assignments {
		i, ok := idxByForkID[a.ForkID]
		if !ok {
			continue
		}
		hr := candidates[i].Heat
		if hr == nil {
			continue
		}
		hr.NoveltyScore = a.Novelty
		if a.Cluster == "noise" || a.Cluster == "" {
			hr.ClusterID = a.Cluster
			hr.ClusterLabel = ""
			hr.ClusterMemberCount = 0
			continue
		}
		hr.ClusterID = a.Cluster
		hr.ClusterLabel = labels[a.Cluster]
		hr.ClusterMemberCount = memberCounts[a.Cluster]
		// TODO(T9 follow-up): re-score Heat.Score with the novelty bonus
		// applied through the existing trust multiplier. Today we leave
		// Score unchanged — only the metadata fields are populated.
	}

	fmt.Fprintf(logger, "[cluster] embedded %d forks → %d clusters (incl. noise)\n",
		len(candidates), len(clusters))
	return "", nil
}

// loadOrComputeCentrality returns the upstream DirectoryCentrality. Tries the
// on-disk cache first; on miss, calls repo.Compute and saves the result.
// Returns (zero, false) when no tree source is available or the call fails —
// callers treat false as "ChangeImpact = 0 for all".
func loadOrComputeCentrality(ctx context.Context, opts ClusterOptions, inputs ClusterInputs, logger io.Writer) (repo.DirectoryCentrality, bool) {
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
	// _ = opts to silence unused-arg warning in lints that flag it.
	_ = opts
	return dc, true
}

// selectClusterCandidates orders forks by current heat score desc, filters out
// archived and no-ahead forks, then caps at topN. Stable sort preserves input
// order on ties. Callers are expected to have already dropped pre-T2 "ghost"
// forks (heat.IsGhostFork) — but archived forks slip through occasionally so
// we re-check here.
func selectClusterCandidates(forks []EnrichedFork, topN int) []EnrichedFork {
	// Filter first to avoid wasting topN slots on ineligible forks.
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
		// TODO: skip forks flagged as upstream-mirrors by an external
		// detector. No detector exists yet — placeholder for future.
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

// labelClusters runs HeuristicLabel for each non-noise cluster, treating the
// other candidates' features as the corpus.
func labelClusters(clusters []cluster.Cluster, features []embed.ForkFeatures, idxByForkID map[string]int) map[string]string {
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
		out[c.ID] = cluster.HeuristicLabel(members, corpus)
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

// changeImpactFor returns DirectoryCentrality.ScoreFork over the unique
// directories touched by the given file paths. Returns 0 if DC is unavailable
// or no paths.
func changeImpactFor(dc repo.DirectoryCentrality, ok bool, paths []string) float32 {
	if !ok || len(paths) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(paths))
	dirs := make([]string, 0, len(paths))
	for _, p := range paths {
		d := dirOf(p)
		if d == "" {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		dirs = append(dirs, d)
	}
	return float32(dc.ScoreFork(dirs))
}

// dirOf returns the immediate parent directory of p (e.g., "a/b/c.go" → "a/b").
// Returns "" if p has no slash.
func dirOf(p string) string {
	p = strings.TrimSpace(p)
	d := filepath.ToSlash(filepath.Dir(p))
	if d == "." || d == "/" {
		return ""
	}
	return d
}
