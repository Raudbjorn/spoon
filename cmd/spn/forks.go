// cmd/spn/forks.go
package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/gitlab"
)

// embedderHookForTest, when non-nil, installs the given embedder onto the
// forksops cluster options before Stream runs. Tests use it to drive the
// cluster pipeline deterministically without a live Ollama. Production code
// leaves this nil so SelectEmbedder runs as usual.
var embedderHookForTest embed.Embedder

// providerFactory creates the forge provider for the given repo. Overridable in tests.
var providerFactory = func(ctx context.Context, repo, forgeFlag, forgeHost string) (forge.Forge, string, *agentio.Error) {
	var forced forge.Provider
	switch forgeFlag {
	case "gitlab":
		forced = forge.ProviderGitLab
	case "github":
		forced = forge.ProviderGitHub
	}
	parsed, err := forge.Parse(forge.Config{RepoURL: repo, ForceProvider: forced, ForgeHost: forgeHost})
	if err != nil {
		return nil, "", agentio.NewError(agentio.CodeBadInput, err.Error(), agentio.RemediationBadInput("forks", "list"))
	}
	switch parsed.Provider {
	case forge.ProviderGitHub:
		client, status, cerr := gh.CheckAuth()
		if cerr != nil {
			return nil, "", agentio.NewError(agentio.CodeAuthRequired, cerr.Error(), agentio.RemediationAuthRequired())
		}
		return gh.NewGHProvider(client, status), parsed.Owner + "/" + parsed.Repo, nil
	case forge.ProviderGitLab:
		auth, gerr := gitlab.DetectAuth(ctx, parsed.Host)
		if gerr != nil {
			return nil, "", agentio.NewError(agentio.CodeAuthRequired, gerr.Error(), agentio.RemediationAuthRequired())
		}
		tok := gitlab.TokenFromAuth(ctx, parsed.Host)
		return gitlab.NewProvider(gitlab.NewClient(parsed.Host, tok), auth), parsed.Owner + "/" + parsed.Repo, nil
	default:
		return nil, "", agentio.NewError(agentio.CodeBadInput, "unsupported provider", agentio.RemediationBadInput("forks", "list"))
	}
}

func runForks(args []string) int { return runForksWith(args, os.Stdout, os.Stderr) }

func runForksWith(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return agentio.NewError(agentio.CodeBadInput, "missing verb (list)", agentio.RemediationBadInput("forks", "")).Emit(stderr)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		return doForksList(rest, stdout, stderr)
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown verb: "+verb, agentio.RemediationBadInput("forks", "")).Emit(stderr)
	}
}

func doForksList(args []string, stdout, stderr io.Writer) int {
	var repo, forgeFlag, forgeHost, botList string
	opts := forksops.Options{
		// Default: clustering enabled. Spn is always non-interactive, so the
		// pipeline will silently skip when no embedder is available and surface
		// a structured warning on stderr.
		Cluster: forksops.ClusterOptions{
			Enabled:        true,
			TopN:           50,
			Epsilon:        0.35,
			MinClusterSize: 3,
			NonInteractive: true,
			AutoPull:       os.Getenv("SPOON_AUTO_PULL") == "1",
			NoPrompt:       true,
		},
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tier":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--tier requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > 3 {
				return agentio.NewError(agentio.CodeBadInput, "--tier must be 1, 2, or 3", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.Tier = n
		case "--top":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--top requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return agentio.NewError(agentio.CodeBadInput, "--top must be a positive integer", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.TopN = n
		case "--bot-allowlist":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--bot-allowlist requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			botList = args[i]
		case "--refresh", "--no-cache":
			opts.Refresh = true
			opts.Cluster.Refresh = true
		case "--forge":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--forge requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			forgeFlag = strings.ToLower(args[i])
		case "--forge-host":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--forge-host requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			forgeHost = args[i]
		case "--no-cluster":
			opts.Cluster.Enabled = false
		case "--cluster-top":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--cluster-top requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return agentio.NewError(agentio.CodeBadInput, "--cluster-top requires a positive integer", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.Cluster.TopN = n
		case "--embedder":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--embedder requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			opts.Cluster.Endpoint = args[i]
		case "--embedder-model":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--embedder-model requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			opts.Cluster.ModelOverride = args[i]
		case "--labeler":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--labeler requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			opts.Cluster.LabelerEndpoint = args[i]
		case "--labeler-model":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--labeler-model requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			opts.Cluster.LabelerModel = args[i]
		case "--cluster-epsilon":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--cluster-epsilon requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			f, err := strconv.ParseFloat(args[i], 64)
			if err != nil || f < 0 {
				return agentio.NewError(agentio.CodeBadInput, "--cluster-epsilon requires a non-negative number", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.Cluster.Epsilon = f
		case "--cluster-min-size":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--cluster-min-size requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return agentio.NewError(agentio.CodeBadInput, "--cluster-min-size requires a positive integer", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.Cluster.MinClusterSize = n
		case "--auto-pull":
			opts.Cluster.AutoPull = true
		default:
			if strings.HasPrefix(args[i], "--") {
				return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			if repo != "" {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional: "+args[i], agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			repo = args[i]
		}
	}
	if repo == "" {
		return agentio.NewError(agentio.CodeBadInput, "missing repository argument", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	if botList != "" {
		opts.BotAllowlist = map[string]bool{}
		for _, b := range strings.Split(botList, ",") {
			b = strings.TrimSpace(b)
			if b != "" {
				opts.BotAllowlist[strings.ToLower(b)] = true
			}
		}
	}

	ctx := context.Background()
	provider, repoArg, e := providerFactory(ctx, repo, forgeFlag, forgeHost)
	if e != nil {
		return e.Emit(stderr)
	}
	owner, name := splitRepoArg(repoArg)
	if owner == "" || name == "" {
		return agentio.NewError(agentio.CodeBadInput, "invalid repo: "+repo, agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	// Send cluster-pipeline progress logs to stderr but only after capturing
	// the first ClusterSkip in case the user wants the structured warning.
	opts.Logger = io.Discard
	if embedderHookForTest != nil {
		opts.Cluster.SetEmbedderForTest(embedderHookForTest)
	}
	ch, err := forksops.Stream(ctx, provider, owner, name, opts)
	if err != nil {
		return agentio.NewError(agentio.CodeUpstream, err.Error(), agentio.RemediationUpstream()).Emit(stderr)
	}
	for r := range ch {
		if r.ClusterSkip != nil {
			emitClusterWarning(stderr, r.ClusterSkip)
		}
		if r.Err != nil {
			// Compact one-line stderr error per failing fork.
			_ = agentio.WriteNDJSON(stderr, map[string]any{
				"error": map[string]any{
					"code":    r.Err.Code,
					"message": r.Err.Message,
					"details": r.Err.Details,
				},
			})
			continue
		}
		if err := agentio.WriteNDJSON(stdout, forkToJSON(r)); err != nil {
			return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
	}
	return 0
}

func splitRepoArg(s string) (owner, repo string) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func forkToJSON(r forksops.Result) map[string]any {
	out := map[string]any{
		"id":          r.Fork.ID,
		"owner":       r.Fork.Owner,
		"name":        r.Fork.Name,
		"url":         r.Fork.URL,
		"stars":       r.Fork.Stars,
		"pushed_at":   r.Fork.PushedAt,
		"is_archived": r.Fork.IsArchived,
		"sub_forks":   r.Fork.SubForkCount,
		"releases":    r.Fork.ReleaseCount,
		"heat":        r.Heat.Score,
		"tier":        r.Heat.Tier,
	}
	if r.T2 != nil {
		out["t2"] = map[string]any{
			"ahead":  r.T2.AheadCount,
			"behind": r.T2.BehindCount,
			"mna":    r.T2.MNA,
		}
	}
	if r.T3 != nil {
		out["t3"] = map[string]any{
			"contributors":     len(r.T3.Contributors),
			"commit_span_days": r.T3.CommitSpanDays,
		}
	}
	// Cluster fields are emitted as a unit, gated on ClusterID. Per the plan:
	// omission means "not computed"; a clustered fork with genuine zero
	// novelty must still serialize "noveltyScore": 0 so agents can
	// distinguish "not computed" from "computed and zero."
	if r.Heat.ClusterID != "" {
		out["clusterId"] = r.Heat.ClusterID
		out["noveltyScore"] = r.Heat.NoveltyScore
		if r.Heat.ClusterLabel != "" {
			out["clusterLabel"] = r.Heat.ClusterLabel
		}
		if r.Heat.ClusterMemberCount != 0 {
			out["clusterMemberCount"] = r.Heat.ClusterMemberCount
		}
	}
	if r.Heat.ChangeImpact != 0 {
		out["changeImpact"] = r.Heat.ChangeImpact
	}
	// Components is populated by the v2 scoring path (forksops uses
	// Scorer.ScoreRaw). Emit when present so downstream agents can inspect
	// the per-component point budget breakdown (including the novelty
	// component when clustering ran).
	if len(r.Heat.Components) > 0 {
		comps := make([]map[string]any, 0, len(r.Heat.Components))
		for _, c := range r.Heat.Components {
			comps = append(comps, map[string]any{
				"name":   c.Name,
				"points": c.Points,
				"max":    c.Max,
				"raw":    c.Raw,
			})
		}
		out["components"] = comps
	}
	return out
}

// preferredEmbeddingModels lists the embedding models the cluster pipeline
// will try in order. Mirrors embed.PreferredEmbeddingModels but only the
// names — kept in this file so the warning shape is stable even if the
// embed package's list grows.
var preferredEmbeddingModels = []string{
	"nomic-embed-text",
	"mxbai-embed-large",
	"bge-m3",
	"snowflake-arctic-embed",
}

// emitClusterWarning writes a structured warning to stderr (one JSON object
// per line) describing why the cluster pipeline was skipped. The shape is
// intentionally distinct from agentio.Error: this is non-fatal information,
// not an error envelope.
func emitClusterWarning(stderr io.Writer, skip *forksops.ClusterSkip) {
	code := skip.Code
	message := skip.Message
	remediation := "ollama pull nomic-embed-text"
	switch code {
	case "ollama_unreachable":
		// Embedder host unreachable — same remediation: start Ollama.
		remediation = "ensure Ollama is running and reachable at the configured endpoint"
	case "no_model_installed", "explicit_model_unavailable":
		// Normalize to the agreed-upon code for the agent contract.
		code = "embedder_model_missing"
		if skip.Endpoint != "" {
			message = "no embedding model installed on Ollama at " + skip.Endpoint
		}
	}
	envelope := map[string]any{
		"warning": map[string]any{
			"code":        code,
			"message":     message,
			"remediation": remediation,
			"details": map[string]any{
				"endpoint":  skip.Endpoint,
				"preferred": preferredEmbeddingModels,
			},
		},
	}
	enc := json.NewEncoder(stderr)
	_ = enc.Encode(envelope)
}
