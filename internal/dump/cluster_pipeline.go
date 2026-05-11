package dump

import (
	"context"
	"fmt"
	"io"

	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
)

// ClusterOptions is the CLI-facing options struct for the cluster pipeline.
// It mirrors cluster.PipelineOptions while keeping the embedder-for-test hook
// unexported so the spoon CLI surface remains read-only.
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

	// Labeler is the (optional) cluster Labeler to use. When set, it overrides
	// construction-from-LabelerEndpoint. Tests inject a stub directly via this
	// field.
	Labeler cluster.Labeler

	// embedderForTest is the test seam from the cluster pipeline tests.
	// Not exposed to CLI callers.
	embedderForTest embed.Embedder
}

// toPipeline converts ClusterOptions into the underlying cluster.PipelineOptions.
// If Labeler is unset and LabelerEndpoint is non-empty, a fresh
// OllamaChatLabeler is constructed using the (optionally-overridden) model.
func (o ClusterOptions) toPipeline() cluster.PipelineOptions {
	labeler := o.Labeler
	if labeler == nil && o.LabelerEndpoint != "" {
		model := o.LabelerModel
		if model == "" {
			model = defaultLabelerModel
		}
		labeler = &cluster.OllamaChatLabeler{
			Endpoint: o.LabelerEndpoint,
			Model:    model,
		}
	}
	return cluster.PipelineOptions{
		Enabled:         o.Enabled,
		TopN:            o.TopN,
		Endpoint:        o.Endpoint,
		ModelOverride:   o.ModelOverride,
		LabelerEndpoint: o.LabelerEndpoint,
		Epsilon:         o.Epsilon,
		MinClusterSize:  o.MinClusterSize,
		AutoPull:        o.AutoPull,
		NoPrompt:        o.NoPrompt,
		NonInteractive:  o.NonInteractive,
		Refresh:         o.Refresh,
		Labeler:         labeler,
		EmbedderForTest: o.embedderForTest,
	}
}

// defaultLabelerModel is used when --labeler is set but --labeler-model is not.
const defaultLabelerModel = "llama3.2:3b"

// EnrichedFork is a CLI-facing alias of cluster.EnrichedFork.
type EnrichedFork = cluster.EnrichedFork

// ReadmeFetcher is a CLI-facing alias of cluster.ReadmeFetcher.
type ReadmeFetcher = cluster.ReadmeFetcher

// ClusterInputs is a CLI-facing alias of cluster.PipelineInputs.
type ClusterInputs = cluster.PipelineInputs

// runClusterPipeline is a thin shim over cluster.RunPipeline retained so the
// existing dump test suite continues to drive the same code path. Returns a
// human-readable skip reason (empty string when the pipeline ran to
// completion) and a fatal error.
func runClusterPipeline(ctx context.Context, opts ClusterOptions, inputs ClusterInputs, logger io.Writer) (string, error) {
	skip, err := cluster.RunPipeline(ctx, opts.toPipeline(), inputs, logger)
	if err != nil {
		return "", err
	}
	if skip == nil {
		return "", nil
	}
	return skip.Message, nil
}

// selectClusterCandidates is a thin shim retained for the dump tests.
func selectClusterCandidates(forks []EnrichedFork, topN int) []EnrichedFork {
	return cluster.SelectClusterCandidates(forks, topN)
}

// runDumpClustering wires the cluster pipeline into the dump path. It builds
// EnrichedFork pointers over the live scoredForks slice so the pipeline's
// in-place HeatResult mutations land back in the dump output.
//
// When the provider is anything other than *gh.GHProvider, the README, tree,
// and commit sources are nil. The pipeline tolerates nil sources (README
// skipped, ChangeImpact = 0).
func runDumpClustering(
	ctx context.Context,
	provider forge.Forge,
	parent *forge.ParentData,
	owner, repoName string,
	scoredForks []scoredFork,
	opts Options,
	logger io.Writer,
) {
	enriched := make([]EnrichedFork, len(scoredForks))
	for i := range scoredForks {
		enriched[i] = EnrichedFork{
			T1:   scoredForks[i].Fork,
			T2:   scoredForks[i].T2,
			Heat: &scoredForks[i].Heat,
		}
	}

	inputs := ClusterInputs{
		Provider:      "github",
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
	} else {
		// Non-GitHub provider (e.g., GitLab). Cluster pipeline still runs —
		// README and centrality just won't be available.
		inputs.Provider = "other"
	}

	if _, err := cluster.RunPipeline(ctx, opts.Cluster.toPipeline(), inputs, logger); err != nil {
		fmt.Fprintf(logger, "[cluster] pipeline error: %v (continuing)\n", err)
	}
}
