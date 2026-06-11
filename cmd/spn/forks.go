// cmd/spn/forks.go
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
	"github.com/svnbjrn/spoon/internal/gitea"
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
	case "gitea", "forgejo", "codeberg":
		forced = forge.ProviderGitea
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
	case forge.ProviderGitea:
		info, client := gitea.DetectAuth(ctx, parsed.Host)
		return gitea.NewProvider(client, info, parsed.Host), parsed.Owner + "/" + parsed.Repo, nil
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
	csvMode := false
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
		case "--budget":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--budget requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return agentio.NewError(agentio.CodeBadInput, "--budget must be a positive integer", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.Budget = n
		case "--shortlist":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--shortlist requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return agentio.NewError(agentio.CodeBadInput, "--shortlist must be a positive integer", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.ShortlistN = n
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
		case "--embedder-backend":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--embedder-backend requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			val := strings.ToLower(args[i])
			if val != "ollama" && val != "sidecar" && val != "openai" {
				return agentio.NewError(agentio.CodeBadInput, "--embedder-backend must be 'ollama', 'sidecar', or 'openai'", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.Cluster.Backend = val
		case "--sidecar-endpoint":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--sidecar-endpoint requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			opts.Cluster.SidecarEndpoint = args[i]
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
		case "--full-mdg":
			opts.Cluster.CentralityBackend = "mdg"
		case "--no-mdg":
			opts.Cluster.CentralityBackend = ""
		case "--csv":
			csvMode = true
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

	// An explicit --sidecar-endpoint implies the sidecar backend, overriding a
	// saved openai/ollama config so the flag isn't silently ignored.
	if opts.Cluster.SidecarEndpoint != "" && opts.Cluster.Backend == "" {
		opts.Cluster.Backend = "sidecar"
	}

	// Layer saved embedder defaults under flags/env (flags > env > config >
	// built-in). Provider/host are not layered — the repo arg determines the
	// forge. A bad config emits a warning but never blocks the run.
	if cfg, cerr := config.LoadDefault(); cerr != nil {
		emitConfigWarning(stderr, cerr)
	} else if cfg != nil {
		// flag > env, then layer config backend-aware (see config.LayerEmbedder):
		// a saved backend's endpoint/model isn't inherited under a different backend.
		be := strings.ToLower(config.Coalesce(opts.Cluster.Backend, os.Getenv("SPOON_EMBEDDER_BACKEND")))
		eu := config.Coalesce(opts.Cluster.Endpoint, os.Getenv("SPOON_EMBEDDER_URL"))
		se := config.Coalesce(opts.Cluster.SidecarEndpoint, os.Getenv("SPOON_SIDECAR_ENDPOINT"))
		opts.Cluster.Backend, opts.Cluster.Endpoint, opts.Cluster.ModelOverride, opts.Cluster.SidecarEndpoint, opts.Cluster.LabelerModel =
			cfg.LayerEmbedder(be, eu, opts.Cluster.ModelOverride, se, opts.Cluster.LabelerModel)
	}

	ctx := context.Background()

	// Validate the embedder endpoint up front so a bad URL fails fast (before
	// enumerating forks) instead of silently disabling clustering. --no-cluster
	// skips this. Skipped when a test embedder is injected (it bypasses
	// SelectEmbedder, so there's no real endpoint to probe).
	if embedderHookForTest == nil {
		if perr := embed.Preflight(ctx, embed.PreflightOptions{
			Enabled:         opts.Cluster.Enabled,
			Backend:         opts.Cluster.Backend,
			Endpoint:        opts.Cluster.Endpoint,
			Model:           opts.Cluster.ModelOverride,
			SidecarEndpoint: opts.Cluster.SidecarEndpoint,
			LabelerEndpoint: opts.Cluster.LabelerEndpoint,
		}); perr != nil {
			return agentio.NewError(agentio.CodeBadInput, perr.Error(),
				"Start the embedder or fix the endpoint (--embedder/--sidecar-endpoint), run 'spoon setup' to configure one, or pass --no-cluster to skip clustering.").Emit(stderr)
		}
	}

	provider, repoArg, e := providerFactory(ctx, repo, forgeFlag, forgeHost)
	if e != nil {
		return e.Emit(stderr)
	}
	owner, name := splitRepoArg(repoArg)
	if owner == "" || name == "" {
		return agentio.NewError(agentio.CodeBadInput, "invalid repo: "+repo, agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	// Cluster-pipeline progress logs are silenced to keep NDJSON stable;
	// only structured ClusterSkip warnings are emitted on stderr via
	// emitClusterWarning. The discard is intentional — do not wire stderr
	// here, prose log lines would interleave with the agent envelopes.
	opts.Logger = io.Discard
	// Auto-budget reserve is on by default (stop enriching before the rate
	// window is drained, marking the rest degraded). SPOON_NO_RESERVE=1 opts out
	// to drain the full budget in one pass.
	opts.ReserveDisabled = os.Getenv("SPOON_NO_RESERVE") == "1"
	if embedderHookForTest != nil {
		opts.Cluster.SetEmbedderForTest(embedderHookForTest)
	}
	ch, streamErr := forksops.Stream(ctx, provider, owner, name, opts)
	if streamErr != nil {
		var rl *gh.RateLimitError
		if errors.As(streamErr, &rl) {
			resetAt := rl.ResetAt.UTC().Format("2006-01-02T15:04:05Z07:00")
			secs := rl.RetryAfterSeconds()
			e := agentio.NewError(agentio.CodeRateLimited, streamErr.Error(), agentio.RemediationRateLimited(resetAt, secs))
			if secs > 0 {
				e = e.WithRetryAfter(secs)
			}
			return e.Emit(stderr)
		}
		return agentio.NewError(agentio.CodeUpstream, streamErr.Error(), agentio.RemediationUpstream()).Emit(stderr)
	}
	if csvMode {
		return emitForksCSV(stdout, stderr, ch)
	}
	// NDJSON streaming path.
	degraded, total := 0, 0
	for r := range ch {
		if r.ClusterSkip != nil {
			emitClusterWarning(stderr, r.ClusterSkip)
		}
		if r.T3Skip != nil {
			emitStageSkipWarning(stderr, r.T3Skip)
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
		total++
		if r.BudgetSkip != nil {
			degraded++
		}
		if err := agentio.WriteNDJSON(stdout, forkToJSON(r)); err != nil {
			return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
	}
	if degraded > 0 {
		_ = json.NewEncoder(stderr).Encode(map[string]any{
			"warning": map[string]any{
				"code":        "degraded_rate_reserve",
				"message":     fmt.Sprintf("%d/%d forks left un-enriched at the rate-limit reserve; their divergence is absent, not zero", degraded, total),
				"remediation": "Re-run after the rate window resets to backfill (cached compares resume), or set SPOON_NO_RESERVE=1 to drain the full budget.",
			},
		})
	}
	return 0
}

// emitConfigWarning writes a structured, non-fatal warning when the saved
// config could not be read. The run continues with flags/env/defaults.
func emitConfigWarning(stderr io.Writer, err error) {
	_ = json.NewEncoder(stderr).Encode(map[string]any{
		"warning": map[string]any{
			"code":        "config_ignored",
			"message":     "ignoring spoon config: " + err.Error(),
			"remediation": "Fix or remove the config file, or set SPOON_NO_CONFIG=1.",
		},
	})
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
	} else if r.BudgetSkip != nil {
		// Compare was skipped at the rate-limit reserve. Flag it so the absence
		// of "t2" reads as "not computed" (re-run to backfill), not "no divergence".
		out["budget_skipped"] = true
	}
	if r.T3 != nil {
		out["t3"] = map[string]any{
			"contributors":     len(r.T3.Contributors),
			"commit_span_days": r.T3.CommitSpanDays,
		}
	} else if r.T3Skip != nil {
		// T3 was requested but skipped (e.g. GitHub stats 202). Flag it so the
		// absence of "t3" reads as "unavailable", not "computed and empty".
		out["t3_skipped"] = true
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
	// Robbins expected-rank shortlist fields (set only with --shortlist). Rank
	// is always >= 1 when computed, so >0 distinguishes "computed" from "unset".
	if r.ExpectedRank > 0 {
		out["expectedRank"] = r.ExpectedRank
		out["rankConfidence"] = r.RankConfidence
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

func emitForksCSV(stdout, stderr io.Writer, ch <-chan forksops.Result) int {
	w := csv.NewWriter(stdout)
	header := []string{
		"id", "owner", "name", "url", "stars", "pushed_at", "is_archived",
		"sub_forks", "releases", "heat", "tier",
		"t2_ahead", "t2_behind", "t2_mna",
		"t3_contributors", "t3_commit_span_days",
		"cluster_name", "cluster_score",
	}
	if err := w.Write(header); err != nil {
		return agentio.NewError(agentio.CodeInternal, "write csv header: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	for r := range ch {
		if r.ClusterSkip != nil {
			emitClusterWarning(stderr, r.ClusterSkip)
		}
		if r.T3Skip != nil {
			emitStageSkipWarning(stderr, r.T3Skip)
		}
		if r.Err != nil {
			_ = agentio.WriteNDJSON(stderr, map[string]any{
				"error": map[string]any{
					"code":    r.Err.Code,
					"message": r.Err.Message,
					"details": r.Err.Details,
				},
			})
			continue
		}
		if err := w.Write(forkToCSVRow(r)); err != nil {
			return agentio.NewError(agentio.CodeInternal, "write csv row: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return agentio.NewError(agentio.CodeInternal, "flush csv: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func forkToCSVRow(r forksops.Result) []string {
	t2Ahead, t2Behind, t2MNA := "", "", ""
	if r.T2 != nil {
		t2Ahead = strconv.Itoa(r.T2.AheadCount)
		t2Behind = strconv.Itoa(r.T2.BehindCount)
		t2MNA = strconv.Itoa(r.T2.MNA)
	}
	t3Contribs, t3Span := "", ""
	if r.T3 != nil {
		t3Contribs = strconv.Itoa(len(r.T3.Contributors))
		t3Span = strconv.Itoa(r.T3.CommitSpanDays)
	}
	clusterName, clusterScore := "", ""
	if r.Heat.ClusterID != "" {
		clusterName = r.Heat.ClusterLabel
		clusterScore = strconv.FormatFloat(r.Heat.NoveltyScore, 'f', 3, 64)
	}
	return []string{
		r.Fork.ID,
		r.Fork.Owner,
		r.Fork.Name,
		r.Fork.URL,
		strconv.Itoa(r.Fork.Stars),
		r.Fork.PushedAt.UTC().Format(time.RFC3339),
		strconv.FormatBool(r.Fork.IsArchived),
		strconv.Itoa(r.Fork.SubForkCount),
		strconv.Itoa(r.Fork.ReleaseCount),
		strconv.FormatFloat(r.Heat.Score, 'f', 2, 64),
		strconv.Itoa(r.Heat.Tier),
		t2Ahead,
		t2Behind,
		t2MNA,
		t3Contribs,
		t3Span,
		clusterName,
		clusterScore,
	}
}

// preferredOllamaEmbeddingModels returns the ranked list of Ollama-native
// preferred embedding model names — sourced dynamically from the embed
// package's PreferredEmbeddingModelsCopy() so that the remediation hint stays
// in lockstep when the embed package's list changes.
func preferredOllamaEmbeddingModels() []string {
	all := embed.PreferredEmbeddingModelsCopy()
	out := make([]string, 0, len(all))
	for _, m := range all {
		if !m.OnOllama {
			continue
		}
		out = append(out, m.Name)
	}
	return out
}

// preferredEmbeddingModels is retained for backwards compatibility with the
// existing structured-warning envelope's details.preferred field. Mirrors
// embed.PreferredEmbeddingModels' Ollama-native subset.
var preferredEmbeddingModels = preferredOllamaEmbeddingModels()

// remediationForMissingModel returns the user-facing remediation string for
// a missing-embedding-model warning. The first preferred model is named in
// the `ollama pull` example; the rest are listed as also-supported so users
// know they have choices.
func remediationForMissingModel() string {
	models := preferredOllamaEmbeddingModels()
	if len(models) == 0 {
		return "Install an embedding model on the configured Ollama endpoint"
	}
	if len(models) == 1 {
		return "Install an embedding model: e.g. 'ollama pull " + models[0] + "'"
	}
	return "Install an embedding model: e.g. 'ollama pull " + models[0] +
		"' (also supported: " + strings.Join(models[1:], ", ") + ")"
}

// emitStageSkipWarning writes a structured, non-fatal warning to stderr when a
// per-fork enrichment stage was skipped (e.g. contributors stats unavailable).
// The fork itself is still emitted on stdout — this only flags the missing
// enrichment so agents can tell "skipped" apart from "computed and empty".
func emitStageSkipWarning(stderr io.Writer, skip *forksops.StageSkip) {
	remediation := ""
	if skip.Stage == "contributors" {
		remediation = "GitHub computes contributor stats asynchronously; retry later to populate t3"
	}
	envelope := map[string]any{
		"warning": map[string]any{
			"code":        "stage_skipped",
			"message":     skip.Stage + " enrichment skipped: " + skip.Reason,
			"remediation": remediation,
			"details": map[string]any{
				"stage": skip.Stage,
				"fork":  skip.ForkID,
			},
		},
	}
	enc := json.NewEncoder(stderr)
	_ = enc.Encode(envelope)
}

// emitClusterWarning writes a structured warning to stderr (one JSON object
// per line) describing why the cluster pipeline was skipped. The shape is
// intentionally distinct from agentio.Error: this is non-fatal information,
// not an error envelope.
func emitClusterWarning(stderr io.Writer, skip *forksops.ClusterSkip) {
	code := skip.Code
	message := skip.Message
	remediation := remediationForMissingModel()
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
