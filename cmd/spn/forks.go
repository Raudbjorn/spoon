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
	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
	"github.com/svnbjrn/spoon/internal/genai"
	"github.com/svnbjrn/spoon/internal/gitea"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/gitlab"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/topics"
)

// embedderHookForTest, when non-nil, installs the given embedder onto the
// forksops cluster options before Stream runs. Tests use it to drive the
// cluster pipeline deterministically. Production code leaves this nil so the
// built-in embedder runs as usual.
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
		// Default: clustering enabled — the built-in embedder is always
		// available, so this never blocks on external services.
		Cluster: forksops.ClusterOptions{
			Enabled:        true,
			TopN:           50,
			Epsilon:        0, // resolved per embedder backend below
			MinClusterSize: 3,
		},
	}
	var embedderBackend, openvinoModel, openvinoDevice, openvinoPooling string
	query := ""
	topicRepos := 0
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
		case "--topic-repos":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--topic-repos requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > 25 {
				return agentio.NewError(agentio.CodeBadInput, "--topic-repos must be in [1, 25]", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			topicRepos = n
		case "--heat-weights":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--heat-weights requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			w, werr := heat.LoadWeights(args[i])
			if werr != nil {
				return agentio.NewError(agentio.CodeBadInput, "--heat-weights: "+werr.Error(), agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.HeatWeights = w
		case "--query":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--query requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			query = args[i]
		case "--embedder-backend":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--embedder-backend requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			embedderBackend = strings.ToLower(args[i])
		case "--openvino-model":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--openvino-model requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			openvinoModel = args[i]
		case "--openvino-device":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--openvino-device requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			openvinoDevice = args[i]
		case "--openvino-pooling":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--openvino-pooling requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			openvinoPooling = strings.ToLower(args[i])
			if _, ok := embed.ParsePooling(openvinoPooling); !ok {
				return agentio.NewError(agentio.CodeBadInput, "--openvino-pooling must be 'cls', 'mean', or 'last'", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
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

	// Resolve and construct the embedder backend (flag > env > config) so a
	// misconfigured openvino setup fails fast with a structured error.
	embedderBackend, ovCfg := resolveSpnEmbedderConfig(embedderBackend, openvinoModel, openvinoDevice, openvinoPooling, stderr)
	if embedderHookForTest == nil {
		embedder, embedderID, closeEmbedder, err := embed.SelectBackend(embedderBackend, ovCfg)
		if err != nil {
			return agentio.NewError(agentio.CodeBadInput, err.Error(),
				"Check --embedder-backend/--openvino-model (or $SPOON_EMBEDDER_BACKEND/$SPOON_OPENVINO_MODEL), or omit them to use the built-in embedder.").Emit(stderr)
		}
		defer closeEmbedder()
		if embedderBackend != "" && embedderBackend != embed.BackendBuiltin {
			opts.Cluster.Embedder = embedder
			opts.Cluster.EmbedderID = embedderID
			// Zero-shot categories need a semantic embedder.
			opts.Cluster.Categorize = embedderBackend == embed.BackendOpenVINO
		}
	}
	if opts.Cluster.Epsilon == 0 {
		if embedderBackend == embed.BackendOpenVINO {
			opts.Cluster.Epsilon = 0.35
		} else {
			opts.Cluster.Epsilon = 0.55
		}
	}
	// Label polishing: only when a labeler model is configured and the
	// openvino-genai runtime loads. Like the reranker, a configured but
	// unloadable labeler is a hard error.
	if opts.Cluster.Enabled {
		polisher, closePolisher, lerr := newLabelPolisher()
		if lerr != nil {
			return agentio.NewError(agentio.CodeBadInput, lerr.Error(),
				"Run 'spoon setup' to download the default labeler, install openvino-genai (or set SPOON_OPENVINO_GENAI_LIB), or unset the labeler config.").Emit(stderr)
		}
		if polisher != nil {
			opts.Cluster.LabelPolisher = polisher
			defer closePolisher()
		}
	}

	opts.Query = query
	if query != "" {
		scorer, closeScorer, qerr := newQueryScorer(stderr)
		if qerr != nil {
			return agentio.NewError(agentio.CodeBadInput, qerr.Error(),
				"Run 'spoon setup' to download the default reranker, or unset the reranker config to fall back to lexical query scoring.").Emit(stderr)
		}
		opts.QueryScorer = scorer
		defer closeScorer()
	}

	ctx := context.Background()

	// Cluster-pipeline progress logs are silenced to keep NDJSON stable;
	// only structured ClusterSkip warnings are emitted on stderr via
	// emitClusterWarning. The discard is intentional — do not wire stderr
	// here, prose log lines would interleave with the agent envelopes.
	// SPOON_DEBUG=1 overrides for troubleshooting.
	opts.Logger = io.Discard
	if os.Getenv("SPOON_DEBUG") == "1" {
		opts.Logger = stderr
	}
	// Auto-budget reserve is on by default (stop enriching before the rate
	// window is drained, marking the rest degraded). SPOON_NO_RESERVE=1 opts out
	// to drain the full budget in one pass.
	opts.ReserveDisabled = os.Getenv("SPOON_NO_RESERVE") == "1"
	if embedderHookForTest != nil {
		opts.Cluster.SetEmbedderForTest(embedderHookForTest)
	}

	// Topic mode: "topic:NAME" selects the best repos representing the
	// GitHub topic and evaluates each one's fork network in sequence. Every
	// record carries an "upstream" field so consumers can tell the networks
	// apart.
	if topicName, isTopic := strings.CutPrefix(repo, "topic:"); isTopic {
		if csvMode {
			return agentio.NewError(agentio.CodeBadInput, "topic mode emits NDJSON only (records span multiple upstreams)",
				"Drop --csv, or run per-repo CSV exports against the repos topic mode reports on stderr.").Emit(stderr)
		}
		// Provider construction needs no repo in topic mode; the placeholder
		// is parsed for its host only.
		provider, _, e := providerFactory(ctx, "topic/placeholder", forgeFlag, forgeHost)
		if e != nil {
			return e.Emit(stderr)
		}
		selections, terr := topics.Resolve(ctx, provider, topicName, topicRepos)
		if terr != nil {
			return agentio.NewError(agentio.CodeBadInput, terr.Error(),
				"Topic mode needs a GitHub topic with forkable repositories, e.g. `spn forks list topic:terminal`.").Emit(stderr)
		}
		for _, sel := range selections {
			_ = json.NewEncoder(stderr).Encode(map[string]any{
				"info": map[string]any{
					"code":    "topic_repo_selected",
					"message": fmt.Sprintf("evaluating %s (score %.1f)", sel.FullName, sel.Score),
					"details": map[string]any{
						"repo":       sel.FullName,
						"score":      sel.Score,
						"components": sel.Components,
						"stars":      sel.Stars,
						"forks":      sel.ForkCount,
					},
				},
			})
		}
		for _, sel := range selections {
			owner, name := splitRepoArg(sel.FullName)
			if owner == "" || name == "" {
				continue
			}
			if code := streamAndEmit(ctx, provider, owner, name, sel.FullName, opts, stdout, stderr); code != 0 {
				return code
			}
		}
		return 0
	}

	provider, repoArg, e := providerFactory(ctx, repo, forgeFlag, forgeHost)
	if e != nil {
		return e.Emit(stderr)
	}
	owner, name := splitRepoArg(repoArg)
	if owner == "" || name == "" {
		return agentio.NewError(agentio.CodeBadInput, "invalid repo: "+repo, agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	// Shadow ctx with a cancellable child so any early return below (a write
	// error in the CSV or NDJSON path) aborts the upstream Stream goroutine
	// instead of leaking it blocked on a channel send. opts.Logger,
	// ReserveDisabled, and the test embedder hook are already configured above
	// (hoisted before the topic/single-repo split), so they are not repeated
	// here.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, streamErr := forksops.Stream(ctx, provider, owner, name, opts)
	if streamErr != nil {
		var rl *gh.RateLimitError
		if errors.As(streamErr, &rl) {
			resetAt := rl.ResetAt.UTC().Format("2006-01-02T15:04:05Z07:00")
			secs := rl.RetryAfterSeconds()
			e := agentio.NewError(agentio.CodeRateLimited, streamErr.Error(), agentio.RemediationRateLimited(resetAt, secs)).
				WithDetails(map[string]any{
					"reset_at":            resetAt,
					"retry_after_seconds": secs,
					"remaining":           rl.Remaining,
				})
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
		if err := agentio.WriteNDJSON(stdout, forkToJSONUpstream(r, "")); err != nil {
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
	if r.QueryMethod != "" {
		out["queryScore"] = r.QueryScore
		out["queryMethod"] = r.QueryMethod
	}
	if r.Heat.Category != "" {
		out["category"] = r.Heat.Category
		out["categoryScore"] = r.Heat.CategoryScore
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
	envelope := map[string]any{
		"warning": map[string]any{
			"code":        skip.Code,
			"message":     skip.Message,
			"remediation": "re-run with --no-cluster to skip clustering, or report this if it persists",
		},
	}
	enc := json.NewEncoder(stderr)
	_ = enc.Encode(envelope)
}

// resolveSpnEmbedderConfig layers the embedder backend settings: flag > env >
// config file. A bad config file warns (structured) and is otherwise ignored.
func resolveSpnEmbedderConfig(backend, model, device, pooling string, stderr io.Writer) (string, embed.OpenVINOConfig) {
	var fileCfg config.EmbedderConfig
	if cfg, cerr := config.LoadDefault(); cerr != nil {
		envelope := map[string]any{"warning": map[string]any{
			"code":    "config_invalid",
			"message": cerr.Error(),
		}}
		_ = json.NewEncoder(stderr).Encode(envelope)
	} else if cfg != nil {
		fileCfg = cfg.Embedder
	}
	backend = strings.ToLower(config.Coalesce(backend, os.Getenv("SPOON_EMBEDDER_BACKEND"), fileCfg.Backend))
	model = config.Coalesce(model, os.Getenv("SPOON_OPENVINO_MODEL"), fileCfg.ModelPath)
	device = config.Coalesce(device, os.Getenv("SPOON_OPENVINO_DEVICE"), fileCfg.Device)
	pooling = strings.ToLower(config.Coalesce(pooling, fileCfg.Pooling))
	var p embed.Pooling
	if pooling != "" {
		p, _ = embed.ParsePooling(pooling)
	}
	return backend, embed.OpenVINOConfig{ModelPath: model, Device: device, Pooling: p}
}

// newQueryScorer builds the query relevance scorer: the OpenVINO
// cross-encoder when a reranker model is configured (config file or
// $SPOON_OPENVINO_RERANKER) and this binary supports it; otherwise nil so
// the stream falls back to the built-in lexical scorer. A configured but
// unloadable reranker is a hard error — never a silent quality downgrade.
func newQueryScorer(stderr io.Writer) (embed.QueryScorer, func(), error) {
	modelPath := os.Getenv("SPOON_OPENVINO_RERANKER")
	device := os.Getenv("SPOON_OPENVINO_DEVICE")
	if cfg, cerr := config.LoadDefault(); cerr == nil && cfg != nil {
		modelPath = config.Coalesce(modelPath, cfg.Reranker.ModelPath)
		device = config.Coalesce(device, cfg.Reranker.Device)
	}
	if modelPath == "" {
		return nil, func() {}, nil // lexical fallback
	}
	r, err := embed.NewReranker(embed.RerankConfig{ModelPath: modelPath, Device: device})
	if err != nil {
		return nil, nil, err
	}
	return r, r.Close, nil
}

// newLabelPolisher builds the cluster label polisher from config/env
// (labeler.modelPath or $SPOON_OPENVINO_LABELER). Returns (nil, nil, nil)
// when no labeler is configured.
func newLabelPolisher() (cluster.LabelPolisher, func(), error) {
	modelPath := os.Getenv("SPOON_OPENVINO_LABELER")
	device := ""
	if cfg, cerr := config.LoadDefault(); cerr == nil && cfg != nil {
		modelPath = config.Coalesce(modelPath, cfg.Labeler.ModelPath)
		device = cfg.Labeler.Device
	}
	if modelPath == "" {
		return nil, nil, nil
	}
	p, err := genai.NewLabelPolisher(genai.Config{ModelPath: modelPath, Device: device})
	if err != nil {
		return nil, nil, err
	}
	return p, p.Close, nil
}

// streamAndEmit runs the fork pipeline for one upstream and emits NDJSON
// records tagged with the upstream's full name. Used by topic mode, where
// several upstreams share one output stream.
func streamAndEmit(ctx context.Context, provider forge.Forge, owner, name, upstream string, opts forksops.Options, stdout, stderr io.Writer) int {
	// Cancel on any early return so the background goroutine spawned by
	// forksops.Stream doesn't keep consuming API rate limit after we stop
	// draining ch (e.g. a stdout write failure below).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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
		// A failing repo in a topic set degrades to a structured warning so
		// the remaining repos still get evaluated.
		_ = json.NewEncoder(stderr).Encode(map[string]any{
			"warning": map[string]any{
				"code":    "topic_repo_failed",
				"message": upstream + ": " + streamErr.Error(),
			},
		})
		return 0
	}
	degraded, total := 0, 0
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
		total++
		if r.BudgetSkip != nil {
			degraded++
		}
		if err := agentio.WriteNDJSON(stdout, forkToJSONUpstream(r, upstream)); err != nil {
			return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
	}
	if degraded > 0 {
		_ = json.NewEncoder(stderr).Encode(map[string]any{
			"warning": map[string]any{
				"code":        "degraded_rate_reserve",
				"message":     fmt.Sprintf("%s: %d/%d forks left un-enriched at the rate-limit reserve", upstream, degraded, total),
				"remediation": "Re-run after the rate window resets to backfill (cached compares resume), or set SPOON_NO_RESERVE=1.",
			},
		})
	}
	return 0
}

// forkToJSONUpstream is forkToJSON plus an optional upstream tag (topic mode).
func forkToJSONUpstream(r forksops.Result, upstream string) map[string]any {
	out := forkToJSON(r)
	if upstream != "" {
		out["upstream"] = upstream
	}
	return out
}
