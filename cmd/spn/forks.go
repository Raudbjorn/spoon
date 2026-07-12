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
	"github.com/svnbjrn/spoon/internal/priors"
	"github.com/svnbjrn/spoon/internal/semantic"
	"github.com/svnbjrn/spoon/internal/store"
	"github.com/svnbjrn/spoon/internal/topics"
)

// embedderHookForTest, when non-nil, installs the given embedder onto the
// forksops cluster options before Stream runs. Tests use it to drive the
// cluster pipeline deterministically. Production code leaves this nil so the
// built-in embedder runs as usual.
var embedderHookForTest embed.Embedder

type githubRPMContextKey struct{}

func githubRPMFromContext(ctx context.Context) float64 {
	rpm, _ := ctx.Value(githubRPMContextKey{}).(float64)
	return rpm
}

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
		client, status, cerr := gh.CheckAuthConfigured(githubRPMFromContext(ctx))
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

type detailOptions struct {
	files       bool
	commits     bool
	commitFiles bool
}

func doForksList(args []string, stdout, stderr io.Writer) int {
	var repo, forgeFlag, forgeHost, botList string
	details := detailOptions{}
	var githubRPM float64
	webDiffEnabled := false
	csvMode := false
	opts := forksops.Options{
		// Default: clustering enabled — the built-in embedder is always
		// available, so this never blocks on external services.
		Cluster: forksops.ClusterOptions{
			Enabled:        true,
			TopN:           50,
			Epsilon:        0, // resolved per embedder backend below
			MinClusterSize: 3,
			// SiblingSimEnabled defaults to true for standard mode;
			// topic mode overrides it to false (5x cost multiplier).
			SiblingSimEnabled: true,
		},
		MomentumSnapshots: true,
	}
	var embedderBackend, openvinoModel, openvinoDevice, openvinoPooling, fastembedModel, fastembedCache string
	query := ""
	topicRepos := 0
	topicLanesRaw := ""
	topicLaneBudget := 0
	siblingSimModeRaw := ""
	// Track which flags the user passed explicitly so the post-loop
	// defaulting can distinguish "user wants the default" from "user
	// did not address this knob". Used by OwnerCacheTTL (default 24h)
	// and topic-mode SiblingSimEnabled (default off).
	ownerCacheTTLSet := false
	siblingSimFlagSet := false
	noSiblingSimFlagSet := false
	siblingSimModeSet := false
	topicLanesSet := false
	topicLaneBudgetSet := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--rpm":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--rpm requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			value, err := strconv.ParseFloat(args[i], 64)
			if err != nil || value <= 0 || value > 900 {
				return agentio.NewError(agentio.CodeBadInput, "--rpm must be in (0, 900]", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			githubRPM = value
		case "--files":
			details.files = true
		case "--commits":
			details.commits = true
		case "--commit-files":
			details.files = true
			details.commits = true
			details.commitFiles = true
			opts.CommitFiles = true
		case "--commit-file-budget":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--commit-file-budget requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil || value <= 0 {
				return agentio.NewError(agentio.CodeBadInput, "--commit-file-budget must be a positive integer", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.CommitFileBudget = value
		case "--web-diff":
			webDiffEnabled = true
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
		case "--topic-lanes":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--topic-lanes requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			topicLanesRaw = args[i]
			topicLanesSet = true
		case "--topic-lane-budget":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--topic-lane-budget requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > 100 {
				return agentio.NewError(agentio.CodeBadInput, "--topic-lane-budget must be in [1, 100]", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			topicLaneBudget = n
			topicLaneBudgetSet = true
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
		case "--priors":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--priors requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			spec, perr := priors.Load(args[i])
			if perr != nil {
				return agentio.NewError(agentio.CodeBadInput, "--priors: "+perr.Error(), agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.Priors = spec
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
		case "--fastembed-model":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--fastembed-model requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			fastembedModel = args[i]
		case "--fastembed-cache":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--fastembed-cache requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			fastembedCache = args[i]
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
		case "--strict-mdg":
			opts.Cluster.CentralityBackend = "mdg"
			opts.Cluster.StrictMDG = true
		case "--csv":
			csvMode = true
		case "--sibling-sim":
			opts.Cluster.SiblingSimEnabled = true
			siblingSimFlagSet = true
		case "--no-sibling-sim":
			opts.Cluster.SiblingSimEnabled = false
			siblingSimFlagSet = true
			noSiblingSimFlagSet = true
		case "--sibling-sim-mode":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--sibling-sim-mode requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			siblingSimModeRaw = args[i]
			siblingSimFlagSet = true
			siblingSimModeSet = true
		case "--owner-cache-ttl":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--owner-cache-ttl requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			d, err := time.ParseDuration(args[i])
			if err != nil || d < 0 {
				return agentio.NewError(agentio.CodeBadInput, "--owner-cache-ttl must be a valid Go duration (e.g. 1h, 24h)", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.OwnerCacheTTL = d
			ownerCacheTTLSet = true
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
	if _, isTopic := strings.CutPrefix(repo, "topic:"); !isTopic && (topicLanesSet || topicLaneBudgetSet) {
		return agentio.NewError(agentio.CodeBadInput, "--topic-lanes and --topic-lane-budget only apply to topic:NAME mode", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	if siblingSimModeSet {
		if noSiblingSimFlagSet {
			return agentio.NewError(agentio.CodeBadInput, "--sibling-sim-mode conflicts with --no-sibling-sim", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
		}
		switch cluster.SiblingSimMode(siblingSimModeRaw) {
		case cluster.SiblingSimModeUpstreamReadme, cluster.SiblingSimModeForkIntent:
			opts.Cluster.SiblingSimMode = cluster.SiblingSimMode(siblingSimModeRaw)
			opts.Cluster.SiblingSimEnabled = true
		default:
			return agentio.NewError(agentio.CodeBadInput, "--sibling-sim-mode must be upstream_readme or fork_intent", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
		}
	}

	// Default the owner-profile cache TTL to 24h when the user did not
	// pass --owner-cache-ttl and did not pass --refresh. The cache
	// layer treats ttl==0 as "always re-fetch", which is the wrong
	// default for normal runs. --refresh falls back to ttl==0 so the
	// refresh path stays intact.
	if !ownerCacheTTLSet && !opts.Refresh {
		opts.OwnerCacheTTL = 24 * time.Hour
	}

	// Resolve and construct the embedder backend (flag > env > config) so a
	// misconfigured openvino setup fails fast with a structured error.
	var searchEmbedder embed.SearchEmbedder
	var semanticModelID string
	embedderBackend, ovCfg := resolveSpnEmbedderConfig(embedderBackend, openvinoModel, openvinoDevice, openvinoPooling, stderr)
	fastCfg := resolveFastEmbedConfig(fastembedModel, fastembedCache)
	if embedderHookForTest == nil {
		embedderBackendInstance, embedderID, closeEmbedder, err := embed.SelectBackendConfig(embedderBackend, embed.BackendConfig{OpenVINO: ovCfg, FastEmbed: fastCfg})
		if err != nil {
			return agentio.NewError(agentio.CodeBadInput, err.Error(),
				"Check --embedder-backend/--openvino-model (or $SPOON_EMBEDDER_BACKEND/$SPOON_OPENVINO_MODEL), or omit them to use the built-in embedder.").Emit(stderr)
		}
		defer closeEmbedder()
		if searchable, ok := embedderBackendInstance.(embed.SearchEmbedder); ok {
			searchEmbedder = searchable
			semanticModelID = searchable.ModelID()
		}
		if embedderBackend != "" && embedderBackend != embed.BackendBuiltin {
			opts.Cluster.Embedder = embedderBackendInstance
			opts.Cluster.EmbedderID = embedderID
			// Zero-shot categories need a semantic embedder.
			opts.Cluster.Categorize = embedderBackend == embed.BackendOpenVINO || embedderBackend == embed.BackendFastEmbed
		}
	}
	if opts.Cluster.Epsilon == 0 {
		if embedderBackend == embed.BackendOpenVINO || embedderBackend == embed.BackendFastEmbed {
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

	ctx := context.WithValue(context.Background(), githubRPMContextKey{}, githubRPM)

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
	if webDiffEnabled {
		_ = json.NewEncoder(stderr).Encode(map[string]any{"warning": map[string]any{
			"code": "web_diff_unstable", "message": "GitHub web diff HTML is an unsupported, unstable fallback",
		}})
	}

	db, storeErr := store.OpenDefault()
	if storeErr != nil {
		return agentio.NewError(agentio.CodeInternal, "store_unavailable: "+storeErr.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	defer db.Close()

	if topicName, isTopic := strings.CutPrefix(repo, "topic:"); isTopic {
		if csvMode {
			return agentio.NewError(agentio.CodeBadInput, "topic mode emits NDJSON only (records span multiple upstreams)",
				"Drop --csv, or run per-repo CSV exports against the repos topic mode reports on stderr.").Emit(stderr)
		}
		// P2 is opt-in for topic mode; the 5x cost multiplier burns
		// the search rate budget. Override this default only when
		// the user did not pass either --sibling-sim or --no-sibling-sim
		// (an explicit flag wins over the topic-mode default).
		if !siblingSimFlagSet {
			opts.Cluster.SiblingSimEnabled = false
		}
		provider, _, e := providerFactory(ctx, "topic/placeholder", forgeFlag, forgeHost)
		if e != nil {
			return e.Emit(stderr)
		}
		if webDiffEnabled {
			if ghp, ok := provider.(*gh.GHProvider); ok && ghp.Client() != nil {
				ghp.Client().EnableWebDiff(os.Getenv("SPOON_GH_COOKIE"))
			}
		}
		parsedLanes, perr := topics.ParseLanes(topicLanesRaw)
		if perr != nil {
			return agentio.NewError(agentio.CodeBadInput, perr.Error(),
				"Use --topic-lanes with comma-separated values: default,stars,updated,forks.").Emit(stderr)
		}
		selections, terr := topics.ResolveWithOptions(ctx, provider, topicName, topics.ResolveOptions{
			Repos:      topicRepos,
			Lanes:      parsedLanes,
			LaneBudget: topicLaneBudget,
		})
		if ghp, ok := provider.(*gh.GHProvider); ok {
			emitDuplicateIdentityWarning(stderr, ghp.Client())
		}
		if terr != nil {
			return agentio.NewError(agentio.CodeBadInput, terr.Error(),
				"Topic mode needs a GitHub topic with forkable repositories, e.g. `spn forks list topic:terminal`.").Emit(stderr)
		}
		auth, _ := provider.Auth(ctx)
		for _, sel := range selections {
			info := map[string]any{
				"repo": sel.FullName, "score": sel.Score, "components": sel.Components,
				"stars": sel.Stars, "forks": sel.ForkCount,
			}
			if len(parsedLanes) > 0 {
				info["lanes"] = sel.Lanes
			}
			_ = json.NewEncoder(stderr).Encode(map[string]any{"info": map[string]any{
				"code":    "topic_repo_selected",
				"message": fmt.Sprintf("evaluating %s (score %.1f)", sel.FullName, sel.Score),
				"details": info,
			}})
		}
		for _, sel := range selections {
			owner, name := splitRepoArg(sel.FullName)
			if owner == "" || name == "" {
				continue
			}
			if code := streamAndEmit(ctx, db, auth, provider, owner, name, sel.FullName, opts, details, semanticModelID, stdout, stderr); code != 0 {
				return code
			}
		}
		if searchEmbedder != nil {
			emitSemanticIndexWarning(ctx, db, searchEmbedder, stderr)
		}
		return 0
	}

	provider, repoArg, e := providerFactory(ctx, repo, forgeFlag, forgeHost)
	if e != nil {
		return e.Emit(stderr)
	}
	if webDiffEnabled {
		if ghp, ok := provider.(*gh.GHProvider); ok && ghp.Client() != nil {
			ghp.Client().EnableWebDiff(os.Getenv("SPOON_GH_COOKIE"))
		}
	}
	owner, name := splitRepoArg(repoArg)
	if ghp, ok := provider.(*gh.GHProvider); ok {
		emitDuplicateIdentityWarning(stderr, ghp.Client())
	}
	if owner == "" || name == "" {
		return agentio.NewError(agentio.CodeBadInput, "invalid repo: "+repo, agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	auth, _ := provider.Auth(ctx)
	if ghp, ok := provider.(*gh.GHProvider); ok {
		if client := ghp.Client(); client != nil {
			opts.Cluster.SiblingSearcher = gh.NewGHSiblingSearcher(client)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, streamErr := forksops.Stream(ctx, provider, owner, name, opts)
	if streamErr != nil {
		var rl *gh.RateLimitError
		if errors.As(streamErr, &rl) {
			resetAt := rl.ResetAt.UTC().Format("2006-01-02T15:04:05Z07:00")
			secs := rl.RetryAfterSeconds()
			e := agentio.NewError(agentio.CodeRateLimited, streamErr.Error(), agentio.RemediationRateLimited(resetAt, secs)).
				WithDetails(map[string]any{"reset_at": resetAt, "retry_after_seconds": secs, "remaining": rl.Remaining})
			if secs > 0 {
				e = e.WithRetryAfter(secs)
			}
			return e.Emit(stderr)
		}
		return agentio.NewError(agentio.CodeUpstream, streamErr.Error(), agentio.RemediationUpstream()).Emit(stderr)
	}
	if csvMode {
		return emitForksCSV(ctx, db, auth, owner, name, semanticModelID, stdout, stderr, ch)
	}
	degraded, total := 0, 0
	for r := range ch {
		if r.ClusterSkip != nil {
			if r.ClusterSkip.Code == "mdg_unavailable" {
				return agentio.NewError(agentio.CodePolicy, r.ClusterSkip.Message,
					"--strict-mdg: MDG centrality is unavailable; rerun without --strict-mdg to allow silent fallback, or omit --full-mdg to use the directory proxy.").Emit(stderr)
			}
			emitClusterWarning(stderr, r.ClusterSkip)
		}
		if r.T3Skip != nil {
			emitStageSkipWarning(stderr, r.T3Skip)
		}
		if r.OwnerProfileSkip != nil {
			emitStageSkipWarning(stderr, r.OwnerProfileSkip)
		}
		if r.SiblingSimSkip != nil {
			emitStageSkipWarning(stderr, r.SiblingSimSkip)
		}
		if r.CommitFilesSkip != nil {
			emitStageSkipWarning(stderr, r.CommitFilesSkip)
		}
		if r.Err != nil {
			_ = agentio.WriteNDJSON(stderr, perForkErrorEnvelope(r.Err))
			continue
		}
		total++
		if r.BudgetSkip != nil {
			degraded++
		}
		if err := persistForkSnapshot(ctx, db, auth, owner, name, semanticModelID, r); err != nil {
			return agentio.NewError(agentio.CodeInternal, "store_unavailable: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
		if err := agentio.WriteNDJSON(stdout, forkToJSONDetailedUpstream(r, details, "")); err != nil {
			return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
	}
	if degraded > 0 {
		_ = json.NewEncoder(stderr).Encode(map[string]any{"warning": map[string]any{
			"code":        "degraded_rate_reserve",
			"message":     fmt.Sprintf("%d/%d forks left un-enriched at the rate-limit reserve; their divergence is absent, not zero", degraded, total),
			"remediation": "Re-run after the rate window resets to backfill (cached compares resume), or set SPOON_NO_RESERVE=1 to drain the full budget.",
		}})
	}
	if searchEmbedder != nil {
		emitSemanticIndexWarning(ctx, db, searchEmbedder, stderr)
	}
	return 0
}

func persistForkSnapshot(ctx context.Context, db *store.Store, auth forge.AuthInfo, owner, name, modelID string, r forksops.Result) error {
	now := time.Now().UTC()
	host := auth.Host
	if host == "" {
		switch auth.Provider {
		case forge.ProviderGitLab:
			host = "gitlab.com"
		case forge.ProviderGitea:
			host = "codeberg.org"
		default:
			host = "github.com"
		}
	}
	firstSeen := r.Fork.CreatedAt
	if firstSeen.IsZero() {
		firstSeen = now
	}
	snapshot := store.Snapshot{
		Repo: store.RepoRecord{
			Provider: auth.Provider.String(), Host: host, Owner: owner, Name: name,
			FirstSeen: firstSeen, LastSeen: now,
		},
		Fork: store.ForkRecord{
			ForgeID: r.Fork.ID, Owner: r.Fork.Owner, Name: r.Fork.Name, URL: r.Fork.URL,
			Description: r.Fork.Description, Language: r.Fork.Language, Topics: r.Fork.Topics,
			Stars: r.Fork.Stars, PushedAt: r.Fork.PushedAt, Heat: r.Heat.Score,
			Tier: r.Heat.Tier, UpdatedAt: now,
		},
	}
	if r.T2 != nil {
		snapshot.CompareFiles = storeFiles(r.T2.Diffs)
		snapshot.Commits = make([]store.CommitRecord, 0, len(r.T2.Commits))
		for _, commit := range r.T2.Commits {
			snapshot.Commits = append(snapshot.Commits, store.CommitRecord{
				SHA: commit.SHA, Message: commit.Message, AuthorLogin: commit.AuthorLogin,
				AuthorEmail: commit.AuthorEmail, CommittedAt: commit.Timestamp, Files: storeFiles(commit.Files),
			})
		}
	}
	if modelID != "" {
		repoKey := store.RepoKey(snapshot.Repo.Provider, snapshot.Repo.Host, owner, name)
		forkKey := store.ForkKey(repoKey, r.Fork.ID)
		snapshot.Document = semantic.BuildDocument(modelID, forkKey, r.Fork, r.T2)
	}
	return db.UpsertSnapshot(ctx, snapshot)
}

func emitDuplicateIdentityWarning(stderr io.Writer, client *gh.Client) {
	if client == nil || client.DuplicateIdentities() == 0 {
		return
	}
	_ = json.NewEncoder(stderr).Encode(map[string]any{"warning": map[string]any{
		"code":        "duplicate_github_identity",
		"message":     fmt.Sprintf("%d configured token(s) were deduplicated because they resolve to an already-active GitHub login", client.DuplicateIdentities()),
		"remediation": "Use credentials for distinct GitHub users to gain independent primary budgets; REST and GraphQL quotas remain separate.",
	}})
}

func emitSemanticIndexWarning(ctx context.Context, db *store.Store, model embed.SearchEmbedder, stderr io.Writer) {
	if _, err := semantic.IndexPending(ctx, db, model); err != nil {
		_ = json.NewEncoder(stderr).Encode(map[string]any{"warning": map[string]any{
			"code": "semantic_index_failed", "message": err.Error(),
			"remediation": "The relational snapshot was saved; rerun the same command after fixing FastEmbed to retry missing embeddings.",
		}})
	}
}

func storeFiles(files []forge.FileDiff) []store.FileRecord {
	out := make([]store.FileRecord, 0, len(files))
	for _, file := range files {
		var patch *string
		if file.Patch != "" {
			value := file.Patch
			patch = &value
		}
		out = append(out, store.FileRecord{
			Path: file.Path, PreviousPath: file.PreviousPath, Status: file.Status,
			Additions: file.Additions, Deletions: file.Deletions, Patch: patch, PatchSource: file.PatchSource,
		})
	}
	return out
}

func splitRepoArg(s string) (owner, repo string) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func forkToJSON(r forksops.Result) map[string]any {
	return forkToJSONDetailed(r, detailOptions{})
}

func forkToJSONDetailed(r forksops.Result, details detailOptions) map[string]any {
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
		"visibility":  visibilityToJSON(r),
		"momentum":    momentumToJSON(r.Momentum),
	}
	if r.QueryMethod != "" {
		out["queryScore"] = r.QueryScore
		out["queryMethod"] = r.QueryMethod
	}
	// priorScore/priorReasons are emitted only when --priors ran (a match
	// scored > 0, or a deny-only match left reasons). NDJSON-only, like
	// visibility/momentum/networkRank; CSV is intentionally unchanged.
	if len(r.PriorReasons) > 0 || r.PriorScore > 0 {
		out["priorScore"] = r.PriorScore
		out["priorReasons"] = r.PriorReasons
	}
	if r.Heat.Category != "" {
		out["category"] = r.Heat.Category
		out["categoryScore"] = r.Heat.CategoryScore
	}
	if r.T2 != nil {
		t2 := map[string]any{
			"ahead":  r.T2.AheadCount,
			"behind": r.T2.BehindCount,
			"mna":    r.T2.MNA,
		}
		if details.files {
			t2["files"] = fileDiffsToJSON(r.T2.Diffs)
		}
		if r.T2.PatchSkipReason != "" {
			t2["patch_skipped_reason"] = r.T2.PatchSkipReason
		}
		if details.commits {
			commits := make([]map[string]any, 0, len(r.T2.Commits))
			for _, commit := range r.T2.Commits {
				row := map[string]any{
					"sha": commit.SHA, "message": commit.Message,
					"authorLogin": commit.AuthorLogin, "authorEmail": commit.AuthorEmail,
					"timestamp": commit.Timestamp,
				}
				if details.commitFiles {
					row["files"] = fileDiffsToJSON(commit.Files)
				}
				commits = append(commits, row)
			}
			t2["commits"] = commits
		}
		if details.commitFiles {
			t2["commit_files_complete"] = r.CommitFilesComplete
			if r.CommitFilesSkip != nil {
				t2["commit_files_skipped_reason"] = r.CommitFilesSkip.Reason
			}
		}
		out["t2"] = t2
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
	if degraded := degradedToJSON(r); len(degraded) > 0 {
		out["degraded"] = degraded
	}
	if r.NetworkRank != nil {
		out["networkRank"] = map[string]any{
			"position":   r.NetworkRank.Position,
			"total":      r.NetworkRank.Total,
			"percentile": r.NetworkRank.Percentile,
			"band":       string(r.NetworkRank.Band),
		}
	}
	// profile is always meaningful (derives from always-present fields, with
	// "standard" as the floor), so it is emitted unconditionally; profileReasons
	// only when the label carries explanatory facts.
	profile, profileReasons := forksops.DeriveProfile(r)
	out["profile"] = profile
	if len(profileReasons) > 0 {
		out["profileReasons"] = profileReasons
	}
	return out
}

func fileDiffsToJSON(files []forge.FileDiff) []map[string]any {
	out := make([]map[string]any, 0, len(files))
	for _, file := range files {
		var patch any
		if file.Patch != "" {
			patch = file.Patch
		}
		out = append(out, map[string]any{
			"path": file.Path, "previousPath": file.PreviousPath, "status": file.Status,
			"additions": file.Additions, "deletions": file.Deletions,
			"patch": patch, "patchSource": file.PatchSource,
		})
	}
	return out
}

func visibilityToJSON(r forksops.Result) map[string]any {
	visibility := r.Visibility
	if visibility.Status == "" {
		visibility = forksops.DeriveVisibility(r)
	}
	out := map[string]any{"status": string(visibility.Status)}
	if len(visibility.Reasons) > 0 {
		out["reasons"] = append([]string(nil), visibility.Reasons...)
	}
	return out
}

func degradedToJSON(r forksops.Result) []map[string]any {
	degraded := r.Degraded
	if len(degraded) == 0 {
		degraded = forksops.CollectDegradedStages(r)
	}
	if len(degraded) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(degraded))
	for _, d := range degraded {
		out = append(out, map[string]any{
			"stage":  d.Stage,
			"reason": d.Reason,
		})
	}
	return out
}

func momentumToJSON(momentum forksops.MomentumInfo) map[string]any {
	if momentum.Status == "" {
		momentum.Status = forksops.MomentumUnknown
	}
	return map[string]any{
		"status":           string(momentum.Status),
		"starsDelta30d":    momentum.StarsDelta30d,
		"subForksDelta30d": momentum.SubForksDelta30d,
		"observedDays":     momentum.ObservedDays,
	}
}

func emitForksCSV(ctx context.Context, db *store.Store, auth forge.AuthInfo, owner, name, modelID string, stdout, stderr io.Writer, ch <-chan forksops.Result) int {
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
		if r.OwnerProfileSkip != nil {
			emitStageSkipWarning(stderr, r.OwnerProfileSkip)
		}
		if r.SiblingSimSkip != nil {
			emitStageSkipWarning(stderr, r.SiblingSimSkip)
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
		if err := persistForkSnapshot(ctx, db, auth, owner, name, modelID, r); err != nil {
			return agentio.NewError(agentio.CodeInternal, "store_unavailable: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
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

func resolveFastEmbedConfig(model, cacheDir string) embed.FastEmbedConfig {
	var fileCfg config.EmbedderConfig
	if cfg, err := config.LoadDefault(); err == nil && cfg != nil {
		fileCfg = cfg.Embedder
	}
	model = config.Coalesce(model, os.Getenv("SPOON_FASTEMBED_MODEL"), fileCfg.Model)
	cacheDir = config.Coalesce(cacheDir, os.Getenv("SPOON_FASTEMBED_CACHE"), fileCfg.CacheDir)
	return embed.FastEmbedConfig{
		Model: model, CacheDir: cacheDir, MaxLength: fileCfg.MaxLength, BatchSize: fileCfg.BatchSize,
	}
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
func streamAndEmit(ctx context.Context, db *store.Store, auth forge.AuthInfo, provider forge.Forge, owner, name, upstream string, opts forksops.Options, details detailOptions, modelID string, stdout, stderr io.Writer) int {
	// P2 distant-relation discovery: wire a real GHSiblingSearcher for
	// the GitHub provider. Topic mode reaches this path once per
	// selected upstream; the per-upstream cost is 1 search + 1 embed.
	if ghp, ok := provider.(*gh.GHProvider); ok {
		if client := ghp.Client(); client != nil {
			opts.Cluster.SiblingSearcher = gh.NewGHSiblingSearcher(client)
		}
	}
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
		if r.OwnerProfileSkip != nil {
			emitStageSkipWarning(stderr, r.OwnerProfileSkip)
		}
		if r.SiblingSimSkip != nil {
			emitStageSkipWarning(stderr, r.SiblingSimSkip)
		}
		if r.CommitFilesSkip != nil {
			emitStageSkipWarning(stderr, r.CommitFilesSkip)
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
		if err := persistForkSnapshot(ctx, db, auth, owner, name, modelID, r); err != nil {
			return agentio.NewError(agentio.CodeInternal, "store_unavailable: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
		if err := agentio.WriteNDJSON(stdout, forkToJSONDetailedUpstream(r, details, upstream)); err != nil {
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
	return forkToJSONDetailedUpstream(r, detailOptions{}, upstream)
}

func forkToJSONDetailedUpstream(r forksops.Result, details detailOptions, upstream string) map[string]any {
	out := forkToJSONDetailed(r, details)
	if upstream != "" {
		out["upstream"] = upstream
	}
	return out
}

// perForkErrorEnvelope builds the NDJSON-shaped `{"error": {...}}` map for
// a per-fork failure, populating the full agentio envelope contract
// (remediation + retryable) so agents can branch consistently with the
// fatal-error path. Called only from the streaming loop; emits a single
// compact line via agentio.WriteNDJSON.
func perForkErrorEnvelope(e *forksops.Error) map[string]any {
	code := agentio.Code(e.Code)
	// Per-fork failures don't carry reset_at / retry-after metadata, so we
	// use the generic upstream wait guidance for every code. The code-to-
	// retryable mapping still comes from agentio (defaultRetryable), which
	// is the contract that matters for agent branching.
	rem := agentio.RemediationUpstream()
	body := map[string]any{
		"code":        string(code),
		"message":     e.Message,
		"remediation": rem,
		"retryable":   agentio.NewError(code, e.Message, rem).Retryable,
	}
	if e.Details != nil {
		body["details"] = e.Details
	}
	return map[string]any{"error": body}
}
