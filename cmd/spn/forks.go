// cmd/spn/forks.go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
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

type effectiveConfigContextKey struct{}

func effectiveFromContext(ctx context.Context) (config.EffectiveConfig, map[string]string, bool) {
	effective, ok := ctx.Value(effectiveConfigContextKey{}).(config.EffectiveConfig)
	env, envOK := ctx.Value(environmentContextKey{}).(map[string]string)
	return effective, env, ok && envOK
}

type environmentContextKey struct{}

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
		effective, _, ok := effectiveFromContext(ctx)
		if !ok {
			effective = config.ResolveEffectiveConfig(nil, nil, config.EnvironmentSnapshot())
		}
		client, status, cerr := gh.CheckAuthWithEffective(effective)
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

// runForksWith is the package test seam. Production dispatch supplies the
// single Bootstrap-owned result to runForksWithEffective.
func runForksWith(args []string, stdout, stderr io.Writer) int {
	boot := config.Bootstrap(io.Discard)
	env := config.EnvironmentSnapshot()
	return runForksWithEffectiveDeps(args, stdout, stderr, config.ResolveEffectiveConfig(boot.Config, nil, env), env, defaultCommandDeps())
}

func runForksWithEffective(args []string, stdout, stderr io.Writer, effective config.EffectiveConfig, env map[string]string) int {
	return runForksWithEffectiveDeps(args, stdout, stderr, effective, env, defaultCommandDeps())
}

func runForksWithEffectiveDeps(args []string, stdout, stderr io.Writer, effective config.EffectiveConfig, env map[string]string, deps commandDeps) int {
	deps = deps.withDefaults()
	if len(args) == 0 {
		return agentio.NewError(agentio.CodeBadInput, "missing verb (list)", agentio.RemediationBadInput("forks", "")).Emit(stderr)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		return doForksListWithDeps(rest, stdout, stderr, effective, env, deps)
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown verb: "+verb, agentio.RemediationBadInput("forks", "")).Emit(stderr)
	}
}

type detailOptions struct {
	files       bool
	commits     bool
	commitFiles bool
}

func doForksList(args []string, stdout, stderr io.Writer, effective config.EffectiveConfig, env map[string]string) int {
	return doForksListWithDeps(args, stdout, stderr, effective, env, defaultCommandDeps())
}

func doForksListWithDeps(args []string, stdout, stderr io.Writer, effective config.EffectiveConfig, env map[string]string, deps commandDeps) int {
	deps = deps.withDefaults()
	var repo, botList string
	forgeFlag := strings.ToLower(effective.Forge.Provider.Value)
	forgeHost := effective.Forge.Host.Value
	details := detailOptions{}
	var githubRPM float64
	webDiffEnabled := false
	// Opt-in: ScanBranchesLocal shells out to git (ls-remote/fetch/merge-base),
	// a new failure mode (missing binary, unreachable merge-base) that the
	// REST/GraphQL-only branch scan doesn't have. REST stays the default.
	localBranchScanEnabled := os.Getenv("SPOON_LOCAL_BRANCH_SCAN") == "1"
	csvMode := false
	var acquisitionReport forge.AcquisitionReport
	opts := forksops.Options{
		Report: &acquisitionReport,
		Now:    deps.now,
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
	// fastembed runs by default (persistence + semantic index); --no-embed
	// opts out. There is no backend selection — fastembed is the only local
	// embedder. Voyage, when a key is configured, indexes alongside it;
	// --no-voyage (or SPOON_NO_VOYAGE=1) skips the network provider while
	// leaving fastembed on.
	noEmbed := os.Getenv("SPOON_NO_EMBED") == "1"
	noVoyage := false
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
	clusterEpsilonSet := false
	siblingSimFlagSet := false
	noSiblingSimFlagSet := false
	siblingSimModeSet := false
	shortlistRuleSet := false
	priorScaleSet := false
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
		case "--local-branch-scan":
			localBranchScanEnabled = true
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
		case "--shortlist-rule":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--shortlist-rule requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			switch args[i] {
			case forksops.ShortlistRuleExpected, forksops.ShortlistRuleMembership:
				opts.ShortlistRule = args[i]
			default:
				return agentio.NewError(agentio.CodeBadInput, "--shortlist-rule must be expected or membership", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			shortlistRuleSet = true
		case "--rank-diagnostics":
			opts.RankDiagnostics = true
		case "--eb":
			opts.EB = true
		case "--prior-scale":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--prior-scale requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			f, err := strconv.ParseFloat(args[i], 64)
			if err != nil || !(f > 0) || math.IsInf(f, 0) {
				return agentio.NewError(agentio.CodeBadInput, "--prior-scale must be a positive number", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.PriorScale = f
			priorScaleSet = true
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
			clusterEpsilonSet = true
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
		case "--no-embed":
			noEmbed = true
		case "--no-voyage":
			noVoyage = true
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
			// A later --sibling-sim re-enables the feature, so clear the
			// --no-sibling-sim latch — otherwise the post-loop conflict check
			// fires against a state the flags no longer describe.
			noSiblingSimFlagSet = false
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
		case "--network-scope":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--network-scope requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			switch args[i] {
			case "direct":
				opts.NetworkScope = "direct"
			case "all":
				opts.NetworkScope = "all"
			default:
				return agentio.NewError(agentio.CodeBadInput, "--network-scope must be \"direct\" or \"all\"", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
		case "--network-max-nodes":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--network-max-nodes requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 {
				return agentio.NewError(agentio.CodeBadInput, "--network-max-nodes must be a positive integer", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.NetworkMaxNodes = n
		case "--network-max-depth":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--network-max-depth requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			d, err := strconv.Atoi(args[i])
			if err != nil || d < 0 {
				return agentio.NewError(agentio.CodeBadInput, "--network-max-depth must be a non-negative integer", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.NetworkMaxDepth = d
		case "--network-max-pages":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--network-max-pages requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			p, err := strconv.Atoi(args[i])
			if err != nil || p <= 0 {
				return agentio.NewError(agentio.CodeBadInput, "--network-max-pages must be a positive integer", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.NetworkMaxPages = p
		case "--network-max-elapsed":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--network-max-elapsed requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			i++
			e, err := time.ParseDuration(args[i])
			if err != nil || e < 0 {
				return agentio.NewError(agentio.CodeBadInput, "--network-max-elapsed must be a valid Go duration (e.g. 2m, 5m)", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			opts.NetworkMaxElapsed = e
		case "--":

			// POSIX flag/positional separator: everything after is positional.
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "missing repository argument after --", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			if i+2 < len(args) {
				return agentio.NewError(agentio.CodeBadInput, "unexpected positional argument after --: "+args[i+2], agentio.RemediationBadInput("forks", "list")).Emit(stderr)
			}
			repo = args[i+1]
			i = len(args)
		default:
			// Match a single leading dash, not just "--": otherwise a typo like
			// `-tier 1` is silently swallowed as the positional repo argument and
			// the error points at the wrong token.
			if strings.HasPrefix(args[i], "-") {
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
	// --commit-file-budget is read only while enriching commit files, so it is a
	// silent no-op without --commit-files. Reject it rather than accept-and-ignore.
	if opts.CommitFileBudget > 0 && !opts.CommitFiles {
		return agentio.NewError(agentio.CodeBadInput, "--commit-file-budget requires --commit-files", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	if shortlistRuleSet && opts.ShortlistN == 0 {
		return agentio.NewError(agentio.CodeBadInput, "--shortlist-rule requires --shortlist", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	if opts.RankDiagnostics && opts.ShortlistN == 0 {
		return agentio.NewError(agentio.CodeBadInput, "--rank-diagnostics requires --shortlist", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	if opts.EB && opts.ShortlistN == 0 {
		return agentio.NewError(agentio.CodeBadInput, "--eb requires --shortlist", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	if priorScaleSet && !opts.EB {
		return agentio.NewError(agentio.CodeBadInput, "--prior-scale requires --eb", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	var rankReport forksops.RankReport
	if opts.ShortlistN > 0 {
		opts.RankReport = &rankReport
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

	// Default the network traversal caps to their planned values when not explicitly set.
	// The zero-value check covers both "never mentioned" and "set to 0 explicitly".
	if opts.NetworkMaxNodes == 0 {
		opts.NetworkMaxNodes = 5000
	}
	if opts.NetworkMaxDepth == 0 {
		opts.NetworkMaxDepth = 3
	}
	if opts.NetworkMaxPages == 0 {
		opts.NetworkMaxPages = 200
	}
	if opts.NetworkMaxElapsed == 0 {
		opts.NetworkMaxElapsed = 2 * time.Minute
	}

	// fastembed is the only embedder and runs by default: it powers
	// persistence, the semantic index, and — when available — clustering plus
	// zero-shot categories. --no-embed (or SPOON_NO_EMBED=1) opts out. If
	// onnxruntime/fastembed cannot initialize, the run degrades with a warning
	// (clustering falls back to the built-in lexical embedder) rather than
	// aborting — a missing native runtime must never kill fork listing.
	// searchEmbedders holds every active semantic embedder. Each one indexes
	// independently: the store keys embeddings by (document_id, model), so a
	// second provider adds a partition rather than replacing the first.
	// semanticModelID is only a "is any embedder active" sentinel — the document
	// body it gates is model-independent by design (see semantic.BuildDocument).
	var searchEmbedders []embed.SearchEmbedder
	var semanticModelID string
	fastembedActive := false
	if embedderHookForTest == nil && !noEmbed {
		instance, embedderID, closeEmbedder, err := embed.SelectBackendConfig(embed.BackendFastEmbed, embed.BackendConfig{FastEmbed: resolveFastEmbedConfigEffective(effective, "", "")})
		if err != nil {
			_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
				"code":        "embed_unavailable",
				"message":     "fastembed embedder unavailable; continuing without semantic indexing (clustering uses the built-in lexical embedder)",
				"remediation": "Set ONNX_PATH to libonnxruntime.so and run 'spoon setup', or pass --no-embed to silence this.",
			}})
		} else {
			defer closeEmbedder()
			fastembedActive = true
			if searchable, ok := instance.(embed.SearchEmbedder); ok {
				searchEmbedders = append(searchEmbedders, searchable)
				semanticModelID = searchable.ModelID()
			}
			opts.Cluster.Embedder = instance
			opts.Cluster.EmbedderID = embedderID
			opts.Cluster.Categorize = true // zero-shot categories need a semantic embedder
		}
	}
	// Only fill the backend default when the user did not set epsilon: a struct
	// zero and an explicit --cluster-epsilon 0 (force singleton clusters) are
	// otherwise indistinguishable, and the latter was silently overwritten.
	if !clusterEpsilonSet {
		if fastembedActive {
			opts.Cluster.Epsilon = 0.35
		} else {
			opts.Cluster.Epsilon = 0.55
		}
	}
	// A single run-scoped commit-file budget, shared across every repo. Topic
	// mode calls Stream once per selected repo, so without this each repo would
	// get a fresh budget and issue up to N x the documented cap (#88).
	if opts.CommitFiles {
		budget := opts.CommitFileBudget
		if budget <= 0 {
			budget = 100
		}
		remaining := &atomic.Int64{}
		remaining.Store(int64(budget))
		opts.CommitFileRunBudget = remaining
	}
	// Query relevance uses the Voyage cross-encoder when a key is configured and
	// the built-in lexical scorer otherwise (opts.QueryScorer nil). Resolved only
	// when a query was actually given, so an unused key costs nothing.
	opts.Query = query

	if githubRPM != 0 {
		effective.GitHub.RequestsPerMinute = config.ResolvedString{
			Value: strconv.FormatFloat(githubRPM, 'f', -1, 64), Source: config.SourceFlag,
			Inactive: effective.GitHub.RequestsPerMinute.Source == config.SourceFile || effective.GitHub.RequestsPerMinute.Inactive,
		}
	}
	ctx := context.WithValue(context.Background(), githubRPMContextKey{}, githubRPM)
	ctx = context.WithValue(ctx, effectiveConfigContextKey{}, effective)
	ctx = context.WithValue(ctx, environmentContextKey{}, env)

	// Cluster-pipeline progress logs are silenced to keep NDJSON stable;
	// only structured ClusterSkip warnings are emitted on stderr via
	// emitClusterWarning. The discard is intentional — do not wire stderr
	// here, prose log lines would interleave with the agent envelopes.
	// SPOON_DEBUG=1 overrides for troubleshooting.
	opts.Logger = debugDataLogger(io.Discard)
	if os.Getenv("SPOON_DEBUG") == "1" {
		opts.Logger = debugDataLogger(stderr)
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
		_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
			"code": "web_diff_unstable", "message": "GitHub web diff HTML is an unsupported, unstable fallback",
		}})
	}

	// The global store is mandatory: it is both the persistence layer and the
	// cross-invocation cache, and a silently uncached run would refetch every
	// compare. Fail loudly instead of degrading.
	db, storeErr := store.OpenDefault()
	if storeErr != nil {
		return agentio.NewError(agentio.CodeInternal,
			"cannot open spoon store: "+storeErr.Error(),
			"Check disk space and permissions on ~/.config/spoon; the store is required for every run.").Emit(stderr)
	}
	defer db.Close()

	// Voyage is resolved here, after the store, because the store is also its
	// paid-response cache: an embedder built without it would re-pay for
	// documents this machine has already embedded. Voyage indexes in addition to
	// fastembed, never instead of it. Clustering deliberately stays on the local
	// embedder — it needs only within-run comparability, and routing four
	// modality blobs per fork through a paid API would multiply the spend for no
	// ranking gain.
	if embedderHookForTest == nil && !noEmbed {
		if voyage := resolveVoyageEmbedderEffective(context.Background(), effective, env, noVoyage, db, stderr); voyage != nil {
			searchEmbedders = append(searchEmbedders, voyage)
			if semanticModelID == "" {
				semanticModelID = voyage.ModelID()
			}
		}
	}
	// Query relevance uses the Voyage cross-encoder when a key is configured and
	// the built-in lexical scorer otherwise (opts.QueryScorer nil). Resolved only
	// when a query was given, so an unused key costs nothing.
	if query != "" && !noVoyage {
		if cfg, active, err := resolveVoyageConfigEffective(context.Background(), effective, env, noVoyage, db); err != nil {
			emitVoyageWarning(stderr, "is configured but unusable; scoring --query lexically", err)
		} else if active {
			if reranker, rerr := embed.NewVoyageReranker(cfg); rerr != nil {
				emitVoyageWarning(stderr, "reranker could not be created; scoring --query lexically", rerr)
			} else {
				opts.QueryScorer = reranker
			}
		}
	}

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
			return emitDataError(stderr, e)
		}
		enableWebDiffIfRequested(provider, webDiffEnabled)
		enableLocalBranchScanIfRequested(provider, localBranchScanEnabled)
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
		warnDuplicateIdentityFor(provider, stderr)
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
			_ = agentio.WriteNDJSON(stderr, map[string]any{"info": map[string]any{
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
		emitSemanticIndexWarning(ctx, db, searchEmbedders, stderr)
		return 0
	}

	provider, repoArg, e := providerFactory(ctx, repo, forgeFlag, forgeHost)
	if e != nil {
		return emitDataError(stderr, e)
	}
	enableWebDiffIfRequested(provider, webDiffEnabled)
	enableLocalBranchScanIfRequested(provider, localBranchScanEnabled)
	owner, name := splitRepoArg(repoArg)
	warnDuplicateIdentityFor(provider, stderr)
	if owner == "" || name == "" {
		return agentio.NewError(agentio.CodeBadInput, "invalid repo: "+repo, agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	auth, _ := provider.Auth(ctx)
	if ghp, ok := provider.(*gh.GHProvider); ok {
		if client := ghp.Client(); client != nil {
			opts.Cluster.SiblingSearcher = gh.NewGHSiblingSearcher(client)
		}
	}
	opts.CachedT2 = storeCachedT2(ctx, db, auth, owner, name, opts.Refresh)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if opts.Report != nil {
		*opts.Report = forge.AcquisitionReport{}
	}
	ch, streamErr := forksops.Stream(ctx, provider, owner, name, opts)
	if streamErr != nil {
		var rejected *gh.AllBackendsRejectedError
		if errors.As(streamErr, &rejected) {
			// Every configured token was rejected (401): a non-retryable auth
			// failure, not a rate limit. Surfacing it as rate_limited yields
			// retry_after_seconds=0 and an agent tight-retry loop (#79).
			return agentio.NewError(agentio.CodeAuthRequired, streamErr.Error(), agentio.RemediationAuthRequired()).Emit(stderr)
		}
		var rl *gh.RateLimitError
		if errors.As(streamErr, &rl) {
			resetAt := rl.ResetAt.UTC().Format("2006-01-02T15:04:05Z07:00")
			secs := rl.RetryAfterSeconds()
			e := agentio.NewError(agentio.CodeRateLimited, streamErr.Error(), agentio.RemediationRateLimited(resetAt, secs)).
				WithDetails(map[string]any{"reset_at": resetAt, "retry_after_seconds": secs, "remaining": rl.Remaining})
			if secs > 0 {
				e = e.WithRetryAfter(secs)
			}
			return emitDataError(stderr, e)
		}
		return agentio.NewError(agentio.CodeUpstream, streamErr.Error(), agentio.RemediationUpstream()).Emit(stderr)
	}
	if csvMode {
		code := emitForksCSV(ctx, db, auth, owner, name, semanticModelID, stdout, stderr, ch)
		if code == 0 {
			emitAcquisitionReport(stderr, opts.Report)
			emitRankReport(stderr, opts.RankReport)
			// CSV scans persist documents too; index them like the NDJSON path.
			emitSemanticIndexWarning(ctx, db, searchEmbedders, stderr)
		}
		return code
	}
	degraded, total := 0, 0
	storeWarned := false
	truncWarned := false
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
			_ = emitPerForkError(stderr, r.Err)
			continue
		}
		total++
		if r.BudgetSkip != nil {
			degraded++
		}
		persistSnapshotBestEffort(ctx, db, auth, owner, name, semanticModelID, r, &storeWarned, &truncWarned, stderr)
		if err := emitForkRecord(stdout, r, details, ""); err != nil {
			return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
	}
	if degraded > 0 {
		_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
			"code":        "degraded_rate_reserve",
			"message":     fmt.Sprintf("%d/%d forks left un-enriched at the rate-limit reserve; their divergence is absent, not zero", degraded, total),
			"remediation": "Re-run after the rate window resets to backfill (cached compares resume), or set SPOON_NO_RESERVE=1 to drain the full budget.",
		}})
	}
	emitAcquisitionReport(stderr, opts.Report)
	emitRankReport(stderr, opts.RankReport)
	emitSemanticIndexWarning(ctx, db, searchEmbedders, stderr)
	return 0
}

// storeCachedT2 returns a compare-lookup closure over the repo's stored
// snapshot, or nil when the cache must not be consulted (--refresh) or no
// snapshot exists. Validity is content-addressed (see store.ValidT2): a push
// moves pushed_at, so stale compares never surface and re-fetching overwrites
// them (self-eviction). Lookups are O(1) via the snapshot's fork index.
func storeCachedT2(ctx context.Context, db *store.Store, auth forge.AuthInfo, owner, name string, refresh bool) func(forge.T1Data) *forge.T2Data {
	if db == nil || refresh {
		return nil
	}
	host := auth.Host
	if host == "" {
		host = forge.DefaultHost(auth.Provider)
	}
	snap, err := db.LoadRepoSnapshotExact(ctx, auth.Provider.String(), host, owner, name,
		auth.APIVersion, auth.AuthMode, auth.AuthScopeID)
	if err != nil || snap == nil {
		return nil
	}
	return snap.ValidT2
}

// persistForkSnapshot stores a fork snapshot and reports whether the fork's
// embedding document had its diff section truncated to fit the model window.
func persistForkSnapshot(ctx context.Context, db *store.Store, auth forge.AuthInfo, owner, name, modelID string, r forksops.Result) (diffTruncated bool, err error) {
	now := time.Now().UTC()
	host := auth.Host
	if host == "" {
		host = forge.DefaultHost(auth.Provider)
	}
	firstSeen := r.Fork.CreatedAt
	if firstSeen.IsZero() {
		firstSeen = now
	}
	snapshot := store.Snapshot{
		Repo: store.RepoRecord{
			Provider:          auth.Provider.String(),
			Host:              host,
			Owner:             owner,
			Name:              name,
			FirstSeen:         firstSeen,
			LastSeen:          now,
			APIVersion:        auth.APIVersion,
			AcquisitionMethod: auth.AuthMode,
			AuthScopeID:       auth.AuthScopeID,
		},
		Fork: store.ForkRecord{
			ForgeID: r.Fork.ID, Owner: r.Fork.Owner, Name: r.Fork.Name, URL: r.Fork.URL,
			Description: r.Fork.Description, Language: r.Fork.Language, Topics: r.Fork.Topics,
			Stars: r.Fork.Stars, PushedAt: r.Fork.PushedAt, Heat: r.Heat.Score,
			Tier: r.Heat.Tier, UpdatedAt: now,
			// Linear-history scalars lifted from r.Fork so the CLI upsert
			// writes the same columns the TUI sweep produces.
			MergeCommits:         r.Fork.MergeCommits,
			MergeCommitTruncated: r.Fork.MergeCommitTruncated,
		},
		// Full-fidelity halves: T1 makes the fork reusable as a listing entry
		// (t1_json), T2 preserves the triage scalars alongside the relational
		// compare rows so later runs can serve the compare from the store.
		T1: &r.Fork,
	}
	if r.T2 != nil && !r.T2FromCache {
		// T2 was fetched live: its compare/commit data is authoritative and
		// replaces any stored rows. A degraded scan (r.T2 == nil) leaves
		// T2Present false so UpsertSnapshot preserves previously stored
		// enrichment instead of erasing it — and so does a cache-served T2:
		// the store's read path skips patch text, so re-persisting it would
		// overwrite full rows with patch-less ones, and its document is
		// unchanged from the run that stored it.
		snapshot.T2 = r.T2
		snapshot.T2Present = true
		snapshot.CompareFiles = storeFiles(r.T2.Diffs)
		snapshot.Commits = make([]store.CommitRecord, 0, len(r.T2.Commits))
		for _, commit := range r.T2.Commits {
			snapshot.Commits = append(snapshot.Commits, store.CommitRecord{
				SHA: commit.SHA, Message: commit.Message, AuthorLogin: commit.AuthorLogin,
				AuthorEmail: commit.AuthorEmail, CommittedAt: commit.Timestamp, Files: storeFiles(commit.Files),
			})
		}
		if modelID != "" {
			repoKey := store.RepoKey(snapshot.Repo.Provider, snapshot.Repo.Host, owner, name)
			forkKey := store.ForkKey(repoKey, r.Fork.ID)
			snapshot.Document, diffTruncated = semantic.BuildDocument(forkKey, r.Fork, r.T2)
		}
	} else if modelID != "" && r.T2 == nil {
		// No compare at all: the T1-only document still indexes the fork.
		repoKey := store.RepoKey(snapshot.Repo.Provider, snapshot.Repo.Host, owner, name)
		forkKey := store.ForkKey(repoKey, r.Fork.ID)
		snapshot.Document, diffTruncated = semantic.BuildDocument(forkKey, r.Fork, nil)
	}
	return diffTruncated, db.UpsertSnapshot(ctx, snapshot)
}

// enableWebDiffIfRequested turns on cookie-authenticated web diff scraping for
// GitHub providers. Non-GitHub forges have no equivalent and are left alone.
func enableWebDiffIfRequested(provider forge.Forge, enabled bool) {
	if !enabled {
		return
	}
	if ghp, ok := provider.(*gh.GHProvider); ok && ghp.Client() != nil {
		ghp.Client().EnableWebDiff(os.Getenv("SPOON_GH_COOKIE"))
	}
}

// enableLocalBranchScanIfRequested turns on the git-ls-remote/fetch/merge-base
// branch scan for GitHub providers. Non-GitHub forges have no equivalent and
// are left alone.
func enableLocalBranchScanIfRequested(provider forge.Forge, enabled bool) {
	if !enabled {
		return
	}
	if ghp, ok := provider.(*gh.GHProvider); ok && ghp.Client() != nil {
		ghp.Client().EnableLocalBranchScan()
	}
}

// warnDuplicateIdentityFor emits the duplicate-token warning for GitHub
// providers. Call it only after the credentials have actually been resolved —
// the count it reports is client state accumulated during authentication.
func warnDuplicateIdentityFor(provider forge.Forge, stderr io.Writer) {
	if ghp, ok := provider.(*gh.GHProvider); ok {
		emitDuplicateIdentityWarning(stderr, ghp.Client())
	}
}

func emitDuplicateIdentityWarning(stderr io.Writer, client *gh.Client) {
	if client == nil || client.DuplicateIdentities() == 0 {
		return
	}
	_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
		"code":        "duplicate_github_identity",
		"message":     fmt.Sprintf("%d configured token(s) were deduplicated because they resolve to an already-active GitHub login", client.DuplicateIdentities()),
		"remediation": "Use credentials for distinct GitHub users to gain independent primary budgets; REST and GraphQL quotas remain separate.",
	}})
}

// emitSemanticIndexWarning indexes pending documents once per active embedder.
// Each embedder's pending set is computed independently from its own model ID,
// so a provider added later backfills on its own schedule and one provider's
// failure never blocks the other's index.
func emitSemanticIndexWarning(ctx context.Context, db *store.Store, models []embed.SearchEmbedder, stderr io.Writer) {
	if db == nil {
		return
	}
	for _, model := range models {
		if model == nil {
			continue
		}
		voyage, isVoyage := model.(*embed.VoyageEmbedder)
		if isVoyage {
			emitVoyageIndexingNotice(ctx, db, model, stderr)
		}
		if _, err := semantic.IndexPending(ctx, db, model); err != nil {
			if isVoyage {
				emitVoyageWarning(stderr, "indexing failed; the fastembed index and the relational snapshot are unaffected", err)
				continue
			}
			_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
				"code": "semantic_index_failed", "message": err.Error(),
				"remediation": "The relational snapshot was saved; rerun the same command after fixing FastEmbed to retry missing embeddings.",
			}})
			continue
		}
		if isVoyage {
			emitVoyageTokensNotice(stderr, voyage)
		}
	}
}

// emitVoyageIndexingNotice reports how many documents this run will send to
// Voyage before it sends them. Emitted even when the count is zero: Voyage
// indexing is automatic and billed, so "this re-run cost nothing" has to be
// observable rather than indistinguishable from "Voyage never ran".
func emitVoyageIndexingNotice(ctx context.Context, db *store.Store, model embed.SearchEmbedder, stderr io.Writer) {
	pending, err := db.PendingDocuments(ctx, model.ModelID())
	if err != nil {
		// Not worth a warning of its own — IndexPending is about to surface the
		// same failure with a better message.
		return
	}
	_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
		"code":    "voyage_indexing",
		"message": fmt.Sprintf("embedding %d document(s) with %s", len(pending), model.ModelID()),
		"details": map[string]any{"documents": len(pending), "model": model.ModelID()},
	}})
}

// emitVoyageTokensNotice reports what Voyage actually billed and what the cache
// saved, so the spend and the savings both land in the output stream rather than
// only on the invoice.
func emitVoyageTokensNotice(stderr io.Writer, voyage *embed.VoyageEmbedder) {
	tokens := voyage.TokensUsed()
	hits, misses, deduped := voyage.CacheStats()
	if tokens == 0 && hits == 0 && deduped == 0 {
		return
	}
	_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
		"code": "voyage_tokens",
		"message": fmt.Sprintf("%s billed %d token(s) for %d document(s); %d served from cache, %d deduplicated",
			voyage.ModelID(), tokens, misses, hits, deduped),
		"details": map[string]any{
			"totalTokens": tokens, "model": voyage.ModelID(),
			"billed": misses, "cacheHits": hits, "deduplicated": deduped,
		},
	}})
}

// emitVoyageWarning reports a Voyage failure as a degrade, never a fatal error.
// Voyage indexing is a side effect of listing forks, and this repo's contract is
// that an optional model backend going missing must not fail a run. The one
// place a Voyage failure is fatal is `spn search --voyage`, where the user named
// Voyage on that invocation and silently serving other results would be worse.
func emitVoyageWarning(stderr io.Writer, message string, err error) {
	_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
		"code":        "voyage_unavailable",
		"message":     "voyage " + message + ": " + err.Error(),
		"remediation": voyageRemediation(err),
	}})
}

// voyageRemediation picks advice matching why Voyage failed; only a bad key is
// something the user can act on immediately.
func voyageRemediation(err error) string {
	switch {
	case embed.IsVoyageAuthError(err):
		return "Check the key in " + embed.VoyageAPIKeyEnv + " (or embedder.voyage.apiKeyFile), or set " + embed.VoyageDisableEnv + "=1 to skip Voyage."
	case embed.IsVoyageRateLimited(err):
		return "Voyage rate-limited this run; retry later or reduce run size. Set " + embed.VoyageDisableEnv + "=1 to skip Voyage."
	default:
		return "Retry when api.voyageai.com is reachable, or set " + embed.VoyageDisableEnv + "=1 to skip Voyage."
	}
}

func resolveVoyageEmbedderEffective(ctx context.Context, effective config.EffectiveConfig, env map[string]string, disable bool, cache embed.ResponseCache, stderr io.Writer) *embed.VoyageEmbedder {
	cfg, active, err := resolveVoyageConfigEffective(ctx, effective, env, disable, cache)
	if err != nil {
		emitVoyageWarning(stderr, "is configured but unusable", err)
		return nil
	}
	if !active {
		return nil
	}
	embedder, err := embed.NewVoyageEmbedder(cfg)
	if err != nil {
		emitVoyageWarning(stderr, "embedder could not be created", err)
		return nil
	}
	return embedder
}

func resolveVoyageConfigEffective(ctx context.Context, effective config.EffectiveConfig, env map[string]string, disable bool, cache embed.ResponseCache) (embed.VoyageConfig, bool, error) {
	return embed.ResolveVoyageEffective(ctx, effective, disable, cache, env)
}

// resolveVoyageEmbedder is retained for focused tests. Command paths use the
// Bootstrap-owned effective variant above.
func resolveVoyageEmbedder(ctx context.Context, disable bool, cache embed.ResponseCache, stderr io.Writer) *embed.VoyageEmbedder {
	boot := config.Bootstrap(io.Discard)
	env := config.EnvironmentSnapshot()
	return resolveVoyageEmbedderEffective(ctx, config.ResolveEffectiveConfig(boot.Config, nil, env), env, disable, cache, stderr)
}

// persistSnapshotBestEffort stores a fork snapshot without ever failing the
// run — the durable store is an enhancement, not a prerequisite for listing
// forks. It no-ops when the store is unavailable (db == nil) and emits at most
// one structured warning per run (via warned) so a persistent write failure
// can't flood stderr.
func persistSnapshotBestEffort(ctx context.Context, db *store.Store, auth forge.AuthInfo, owner, name, modelID string, r forksops.Result, warned, truncWarned *bool, stderr io.Writer) {
	if db == nil {
		return
	}
	diffTruncated, err := persistForkSnapshot(ctx, db, auth, owner, name, modelID, r)
	if err != nil && !*warned {
		*warned = true
		_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
			"code":        "store_unavailable",
			"message":     "failed to persist fork snapshot; continuing without persistence: " + err.Error(),
			"remediation": "Check disk space and permissions on ~/.local/share/spoon; listing/output is unaffected.",
		}})
	}
	if err == nil && diffTruncated && !*truncWarned {
		*truncWarned = true
		_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
			"code":        "embed_diff_truncated",
			"message":     "one or more fork diffs exceeded the embedding window and were truncated for indexing",
			"remediation": "Semantic ranking uses the leading portion of large diffs; no action needed unless recall on big changes matters.",
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

// splitRepoArg splits an "owner/repo" argument the way repos are actually keyed:
// the repo is the LAST path segment and the owner is everything before it, joined
// by "/". That matches forge.ParseRepoURL (detect.go: `owner = strings.Join(
// parts[:len(parts)-1], "/")`), which is what wrote the owner and name into the
// store in the first place.
//
// Splitting on the first slash instead — as this did — disagreed with that on
// every nested path, so a GitLab subgroup repo silently matched nothing:
// "group/subgroup/repo" became owner "group", name "subgroup/repo", while the
// stored row had owner "group/subgroup", name "repo". Nested GitLab groups are a
// supported input (see forge.ParseRepoURL's doc comment), so rejecting multi-slash
// arguments outright would drop a real capability rather than fix the mismatch.
//
// Any malformed shape — no slash, or an empty segment anywhere — returns ("", "").
// Callers all treat an empty owner or repo as bad input, so one sentinel covers
// leading, trailing and doubled slashes without each caller re-checking.
func splitRepoArg(s string) (owner, repo string) {
	if strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") || strings.Contains(s, "//") {
		return "", ""
	}
	cut := strings.LastIndexByte(s, '/')
	if cut < 0 {
		return "", ""
	}
	return s[:cut], s[cut+1:]
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
	// Lineage describes the fork's position in the fork network tree.
	// Emit when any lineage field is known: depth>0 (a known hop from the
	// root) or any of the path strings set. Omit-when-unknown avoids
	// fabricating fields the provider did not return (REST has no parent
	// data and must not emit guessed values).
	if r.Lineage.DepthFromRoot > 0 || r.Lineage.NetworkRoot != "" || r.Lineage.DirectParent != "" {
		out["lineage"] = map[string]any{
			"networkRoot":   r.Lineage.NetworkRoot,
			"directParent":  r.Lineage.DirectParent,
			"depthFromRoot": r.Lineage.DepthFromRoot,
		}
	}

	// Linear-history fields. linearHistory itself stays nil when the
	// provider never computed it (the three-state semantics matter for
	// downstream tools). The scalar count and the raw vector ride only
	// when known; the truncate flag is meaningful only when we have a
	// boolean result, so it rides the same conditional.
	if r.Fork.LinearHistory != nil {
		out["linearHistory"] = *r.Fork.LinearHistory
		out["mergeCommits"] = r.Fork.MergeCommits
		if r.Fork.MergeCommitHistory != nil {
			out["mergeCommitHistory"] = r.Fork.MergeCommitHistory
		}
		out["mergeCommitTruncated"] = r.Fork.MergeCommitTruncated
	}

	// Coverage reports how completely the fork list covers the network.
	// Emit when either count is known (non-zero from GraphQL); a known
	// zero unresolved gap (e.g. direct=whole=5) is still emitted. The
	// REST path leaves both at zero and produces no entry.
	if r.Coverage.DirectTotalCount > 0 || r.Coverage.WholeNetworkForkCount > 0 {
		out["coverage"] = map[string]any{
			"directTotalCount":      r.Coverage.DirectTotalCount,
			"wholeNetworkForkCount": r.Coverage.WholeNetworkForkCount,
			"unresolved":            r.Coverage.Unresolved,
		}
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
	// Full rank summary over the same pool (P-score = SUCRA, P(rank ≤ k),
	// P(rank = 1), 95% rank interval). Emitted only when Stream computed it.
	if r.Rank != nil {
		out["pScore"] = r.Rank.PScore
		out["pTopK"] = r.Rank.PTopK
		out["pFirst"] = r.Rank.PFirst
		out["rankLo"] = r.Rank.Lo
		out["rankHi"] = r.Rank.Hi
		if r.Rank.PothResidual != nil {
			out["pothResidual"] = nanToNil(*r.Rank.PothResidual)
		}
	}
	if r.EB != nil {
		out["ebTheta"] = r.EB.Theta
		out["ebSigma"] = r.EB.PostSigma
		out["ebResidual"] = r.EB.Residual
		out["ebLeverage"] = r.EB.Leverage
		out["ebFlag"] = r.EB.Flag
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
	w := newDataCSV(stdout)
	header := []string{
		"id", "owner", "name", "url", "stars", "pushed_at", "is_archived",
		"sub_forks", "releases", "heat", "tier",
		"t2_ahead", "t2_behind", "t2_mna",
		"t3_contributors", "t3_commit_span_days",
		"cluster_name", "cluster_score",
		"expected_rank", "p_score", "p_top_k", "p_first", "rank_lo", "rank_hi",
	}
	if err := w.writeRecord(header); err != nil {
		return agentio.NewError(agentio.CodeInternal, "write csv header: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	storeWarned := false
	truncWarned := false
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
		persistSnapshotBestEffort(ctx, db, auth, owner, name, modelID, r, &storeWarned, &truncWarned, stderr)
		if err := w.writeRecord(forkToCSVRow(r)); err != nil {
			return agentio.NewError(agentio.CodeInternal, "write csv row: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
	}
	if err := w.flush(); err != nil {
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
	expectedRank, pScore, pTopK, pFirst, rankLo, rankHi := "", "", "", "", "", ""
	if r.Rank != nil {
		expectedRank = strconv.FormatFloat(r.Rank.ExpectedRank, 'f', 3, 64)
		pScore = strconv.FormatFloat(r.Rank.PScore, 'f', 4, 64)
		pTopK = strconv.FormatFloat(r.Rank.PTopK, 'f', 4, 64)
		pFirst = strconv.FormatFloat(r.Rank.PFirst, 'f', 4, 64)
		rankLo = strconv.Itoa(r.Rank.Lo)
		rankHi = strconv.Itoa(r.Rank.Hi)
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
		expectedRank,
		pScore,
		pTopK,
		pFirst,
		rankLo,
		rankHi,
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
	_ = agentio.WriteNDJSON(stderr, envelope)
}

// emitAcquisitionReport writes one terminal metadata envelope after a Stream
// channel has closed. It never writes to stdout and silently ignores providers
// that omit the optional terminal report.
// emitRankReport writes the shortlist pool summary to stderr as a
// rank_report info envelope. Nil report (no --shortlist) emits nothing. NaN
// POTH values (pool or shortlist below the POTH minimum) are emitted as null.
func emitRankReport(stderr io.Writer, report *forksops.RankReport) {
	if report == nil || report.PoolSize == 0 {
		return
	}
	details := map[string]any{
		"poolSize":      report.PoolSize,
		"nonzeroPool":   report.NonzeroPool,
		"shortlistN":    report.ShortlistN,
		"shortlistRule": report.ShortlistRule,
		"poth":          nanToNil(report.POTH),
		"cpothK":        nanToNil(report.CPOTHk),
	}
	summary := fmt.Sprintf("ranked %d forks (%d with heat > 0); shortlist %d by %s; POTH %s, cPOTH_k %s",
		report.PoolSize, report.NonzeroPool, report.ShortlistN, report.ShortlistRule,
		fmtProb(report.POTH), fmtProb(report.CPOTHk))
	if report.EBRegime != "" {
		details["ebRegime"] = report.EBRegime
		details["ebPool"] = report.EBPool
		details["tauHat"] = report.TauHat
		details["ebMean"] = report.EBMean
		details["priorScale"] = report.PriorScale
		details["dBarOverK"] = report.DBarOverK
		details["pD"] = report.PD
		switch report.EBRegime {
		case forksops.EBRegimeInsufficient:
			summary += fmt.Sprintf("; EB skipped: only %d forks with heat > 0 (need %d)", report.EBPool, 3)
		case forksops.EBRegimePooled:
			summary += "; EB skipped: no heterogeneity beyond measurement noise (tau_hat = 0) — ranking unchanged"
		case forksops.EBRegimeClamped:
			summary += fmt.Sprintf("; EB applied with tau_hat clamped to %.2f (prior scale %.2f), D_bar/k %.2f", report.TauHat, report.PriorScale, report.DBarOverK)
		default:
			summary += fmt.Sprintf("; EB applied: tau_hat %.2f, mean %.2f, D_bar/k %.2f", report.TauHat, report.EBMean, report.DBarOverK)
		}
	}
	_ = agentio.WriteNDJSON(stderr, map[string]any{"info": map[string]any{
		"code":    "rank_report",
		"message": summary,
		"details": details,
	}})
}

// nanToNil maps NaN to nil so JSON encoding never fails on an undefined
// statistic.
func nanToNil(v float64) any {
	if math.IsNaN(v) {
		return nil
	}
	return v
}

func fmtProb(v float64) string {
	if math.IsNaN(v) {
		return "n/a"
	}
	return strconv.FormatFloat(v, 'f', 3, 64)
}

func emitAcquisitionReport(stderr io.Writer, report *forge.AcquisitionReport) {
	if report == nil || report.Method == "" {
		return
	}
	summary := fmt.Sprintf("%s acquisition via %s; %d unique forks across %d pages",
		report.Method, strings.Join(report.FallbackChain, "/"), report.UniqueRows, report.Pages)
	_ = agentio.WriteNDJSON(stderr, map[string]any{"info": map[string]any{
		"code":    "acquisition_report",
		"message": summary,
		"details": report,
	}})
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
	_ = agentio.WriteNDJSON(stderr, envelope)
}

func resolveFastEmbedConfigEffective(effective config.EffectiveConfig, model, cacheDir string) embed.FastEmbedConfig {
	resolved, err := effective.FastEmbedConfig()
	if err != nil {
		return embed.FastEmbedConfig{}
	}
	if model != "" {
		resolved.Model = model
	}
	if cacheDir != "" {
		resolved.CacheDir = cacheDir
	}
	return embed.FastEmbedConfig{Model: resolved.Model, CacheDir: resolved.CacheDir, MaxLength: resolved.MaxLength, BatchSize: resolved.BatchSize}
}

// resolveFastEmbedConfig is retained for focused tests. Command paths use the
// Bootstrap-owned effective variant above.
func resolveFastEmbedConfig(model, cacheDir string) embed.FastEmbedConfig {
	boot := config.Bootstrap(io.Discard)
	env := config.EnvironmentSnapshot()
	return resolveFastEmbedConfigEffective(config.ResolveEffectiveConfig(boot.Config, nil, env), model, cacheDir)
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
	opts.CachedT2 = storeCachedT2(ctx, db, auth, owner, name, opts.Refresh)
	// Cancel on any early return so the background goroutine spawned by
	// forksops.Stream doesn't keep consuming API rate limit after we stop
	// draining ch (e.g. a stdout write failure below).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if opts.Report != nil {
		*opts.Report = forge.AcquisitionReport{}
	}
	ch, streamErr := forksops.Stream(ctx, provider, owner, name, opts)
	if streamErr != nil {
		var rejected *gh.AllBackendsRejectedError
		if errors.As(streamErr, &rejected) {
			// No usable identity: every token was rejected (401). This affects
			// every repo in the topic set, so fail the whole run rather than
			// degrading per-repo and hammering dead credentials (#79).
			return agentio.NewError(agentio.CodeAuthRequired, streamErr.Error(), agentio.RemediationAuthRequired()).Emit(stderr)
		}
		var rl *gh.RateLimitError
		if errors.As(streamErr, &rl) {
			resetAt := rl.ResetAt.UTC().Format("2006-01-02T15:04:05Z07:00")
			secs := rl.RetryAfterSeconds()
			e := agentio.NewError(agentio.CodeRateLimited, streamErr.Error(), agentio.RemediationRateLimited(resetAt, secs))
			if secs > 0 {
				e = e.WithRetryAfter(secs)
			}
			return emitDataError(stderr, e)
		}
		// A failing repo in a topic set degrades to a structured warning so
		// the remaining repos still get evaluated.
		_ = agentio.WriteNDJSON(stderr, map[string]any{
			"warning": map[string]any{
				"code":    "topic_repo_failed",
				"message": upstream + ": " + streamErr.Error(),
			},
		})
		return 0
	}
	degraded, total := 0, 0
	storeWarned := false
	truncWarned := false
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
			_ = emitTopicPerForkError(stderr, r.Err)
			continue
		}
		total++
		if r.BudgetSkip != nil {
			degraded++
		}
		persistSnapshotBestEffort(ctx, db, auth, owner, name, modelID, r, &storeWarned, &truncWarned, stderr)
		if err := emitForkRecord(stdout, r, details, upstream); err != nil {
			return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
	}
	if degraded > 0 {
		_ = agentio.WriteNDJSON(stderr, map[string]any{
			"warning": map[string]any{
				"code":        "degraded_rate_reserve",
				"message":     fmt.Sprintf("%s: %d/%d forks left un-enriched at the rate-limit reserve", upstream, degraded, total),
				"remediation": "Re-run after the rate window resets to backfill (cached compares resume), or set SPOON_NO_RESERVE=1.",
			},
		})
	}
	emitAcquisitionReport(stderr, opts.Report)
	emitRankReport(stderr, opts.RankReport)
	return 0
}

// forkToJSONUpstream is forkToJSON plus an optional upstream tag (topic mode).
func forkToJSONUpstream(r forksops.Result, upstream string) map[string]any {
	return forkToJSONDetailedUpstream(r, detailOptions{}, upstream)
}

func emitForkRecord(stdout io.Writer, r forksops.Result, details detailOptions, upstream string) error {
	return writeDataNDJSON(stdout, forkToJSONDetailedUpstream(r, details, upstream))
}

func emitPerForkError(stderr io.Writer, e *forksops.Error) error {
	return writeDataNDJSON(stderr, perForkErrorEnvelope(e))
}

func emitTopicPerForkError(stderr io.Writer, e *forksops.Error) error {
	return writeDataNDJSON(stderr, map[string]any{
		"error": map[string]any{
			"code":    e.Code,
			"message": e.Message,
			"details": e.Details,
		},
	})
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
