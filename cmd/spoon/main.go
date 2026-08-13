package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/gitea"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/gitlab"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/store"
	"github.com/svnbjrn/spoon/internal/tui"
	"github.com/svnbjrn/spoon/internal/tui/settings"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

var version = "0.3.0-dev"

func main() {
	// Zero-configuration first run: make sure a documented default config
	// exists before anything consults it. Never fatal — a bad or unwritable
	// config degrades to built-in defaults with a warning.
	boot := config.Bootstrap(os.Stderr)
	cfg := boot.Config
	if boot.Warning != nil {
		fmt.Fprintf(os.Stderr, "warning: ignoring config: %v\n", boot.Warning)
	}

	// Subcommand dispatch: "spoon threads <pr-ref> ..."
	if len(os.Args) >= 2 && os.Args[1] == "threads" {
		os.Exit(runThreads(os.Args[2:]))
	}

	// Subcommand dispatch: "spoon setup ..."
	if len(os.Args) >= 2 && os.Args[1] == "setup" {
		os.Exit(runSetup(os.Args[2:]))
	}

	var repo string
	noColor := false
	refresh := false
	forgeFlag := ""
	forgeHost := ""

	// Cluster pipeline flags (T9). Clustering is ON by default; --no-cluster
	// turns it off. Embedding runs in-process — no external services involved.
	noCluster := false
	var heatWeights map[string]float64
	maxTier := 3
	clusterTop := 50
	clusterEpsilon := 0.0 // 0.55 (lexical) unless set explicitly
	clusterMinSize := 3

	// MDG centrality backend. Off by default; --full-mdg opts in. --no-mdg
	// reverts to off (useful for users who set the env var elsewhere).
	// --strict-mdg implies --full-mdg and turns the silent-fallback path
	// into a hard skip surfaced as a ClusterSkip with code "mdg_unavailable".
	fullMDG := false
	strictMDG := false
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			printHelp()
			os.Exit(0)
		case "-v", "--version":
			fmt.Printf("spoon %s\n", version)
			os.Exit(0)
		case "--no-color":
			noColor = true
		case "--refresh", "--no-cache":
			refresh = true
		case "--forge":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --forge requires a value")
				os.Exit(1)
			}
			i++
			forgeFlag = strings.ToLower(args[i])
			switch forgeFlag {
			case "github", "gitlab", "gitea", "forgejo", "codeberg":
			default:
				fmt.Fprintln(os.Stderr, "Error: --forge must be 'github', 'gitlab', or 'gitea' (forgejo/codeberg)")
				os.Exit(1)
			}
		case "--forge-host":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --forge-host requires a value")
				os.Exit(1)
			}
			i++
			forgeHost = args[i]
		case "--heat-weights":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --heat-weights requires a value")
				os.Exit(1)
			}
			i++
			w, err := heat.LoadWeights(args[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: --heat-weights: %v\n", err)
				os.Exit(1)
			}
			heatWeights = w
		case "--tier":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --tier requires a value")
				os.Exit(1)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > 3 {
				fmt.Fprintln(os.Stderr, "Error: --tier must be 1, 2, or 3")
				os.Exit(1)
			}
			maxTier = n
		case "--no-cluster":
			noCluster = true
		case "--cluster-top":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --cluster-top requires a value")
				os.Exit(1)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				fmt.Fprintln(os.Stderr, "Error: --cluster-top requires a positive integer")
				os.Exit(1)
			}
			clusterTop = n
		case "--cluster-epsilon":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --cluster-epsilon requires a value")
				os.Exit(1)
			}
			i++
			f, err := strconv.ParseFloat(args[i], 64)
			if err != nil || f < 0 {
				fmt.Fprintln(os.Stderr, "Error: --cluster-epsilon requires a non-negative number")
				os.Exit(1)
			}
			clusterEpsilon = f
		case "--cluster-min-size":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --cluster-min-size requires a value")
				os.Exit(1)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				fmt.Fprintln(os.Stderr, "Error: --cluster-min-size requires a positive integer")
				os.Exit(1)
			}
			clusterMinSize = n
		case "--full-mdg":
			fullMDG = true
		case "--no-mdg":
			fullMDG = false
		case "--strict-mdg":
			fullMDG = true
			strictMDG = true
		default:
			if !strings.HasPrefix(args[i], "-") && (strings.Contains(args[i], "/") || strings.HasPrefix(args[i], "topic:")) {
				repo = args[i]
			} else {
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[i])
				printHelp()
				os.Exit(1)
			}
		}
	}

	explicitForgeFlag, explicitForgeHost := forgeFlag, forgeHost
	if forgeFlag == "" && cfg != nil {
		forgeFlag = cfg.Forge.Provider
	}
	if forgeHost == "" && cfg != nil {
		forgeHost = cfg.Forge.Host
	}

	tuiContext, err := resolveTUIContextWithNoColor(cfg, noColor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	theme.PinColorProfile(tuiContext)

	ctx := context.Background()

	// The interactive TUI clusters with the built-in lexical embedder
	// (zero-setup, portable, no native runtime). Semantic search and
	// persistence — the fastembed paths — live in the `spn` agent CLI.
	if clusterEpsilon == 0 {
		clusterEpsilon = 0.55
	}

	// Detect provider from repo URL and flags
	provider, auth, repoArg, err := createProvider(ctx, repo, forgeFlag, forgeHost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	tuiClusterOpts := tui.ClusterOptions{
		Enabled:           !noCluster,
		TopN:              clusterTop,
		Epsilon:           clusterEpsilon,
		MinClusterSize:    clusterMinSize,
		Refresh:           refresh,
		CentralityBackend: backendFor(fullMDG),
		StrictMDG:         strictMDG,
	}
	// The global store is mandatory: every run reads and writes it, and an
	// unusable store means silently uncached, unpersisted sessions — fail
	// loudly instead.
	db, err := store.OpenDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot open spoon store: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	settingsFlags := map[string]string{}
	if explicitForgeFlag != "" {
		settingsFlags["forge.provider"] = explicitForgeFlag
	}
	if explicitForgeHost != "" {
		settingsFlags["forge.host"] = explicitForgeHost
	}
	if noColor {
		settingsFlags["ui.color"] = "no-color"
	}
	settingsModel := settings.NewFromLayer(boot.Layer, db).WithFlags(settingsFlags)
	m := tui.NewModelWithCluster(provider, auth, repoArg, refresh, tuiClusterOpts).
		WithHeatWeights(heatWeights).WithMaxTier(maxTier).WithStore(db).
		WithQueryScorer(tuiQueryScorer(db, cfg)).WithTheme(tuiContext).WithSettings(settingsModel)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// tuiQueryScorer returns the scorer the `R` intent ranking uses from the
// startup-resolved configuration snapshot. Reusing Bootstrap's exact result
// prevents a second load from silently selecting a different layer than the
// runtime and settings editor.
func tuiQueryScorer(db *store.Store, cfg *config.Config) embed.QueryScorer {
	var fileCfg config.VoyageConfig
	if cfg != nil {
		fileCfg = cfg.Embedder.Voyage
	}
	voyageCfg, active, err := embed.ResolveVoyageConfig(context.Background(), fileCfg, false, db)
	if err != nil || !active {
		return nil
	}
	reranker, err := embed.NewVoyageReranker(voyageCfg)
	if err != nil {
		return nil
	}
	return reranker
}

// backendFor maps the --full-mdg flag to the cluster.PipelineOptions
// CentralityBackend string. Off (default) → "" (directory proxy). On → "mdg".
func backendFor(fullMDG bool) string {
	if fullMDG {
		return "mdg"
	}
	return ""
}

// createProvider detects the forge provider from the repo URL and flags,
// creates the appropriate Forge implementation, and returns it with auth info.
// repoArg is returned as the owner/repo string to pass to TUI (without host prefix).
func createProvider(ctx context.Context, repo, forgeFlag, forgeHost string) (forge.Forge, forge.AuthInfo, string, error) {
	// Determine provider
	var forceProvider forge.Provider
	switch forgeFlag {
	case "gitlab":
		forceProvider = forge.ProviderGitLab
	case "github":
		forceProvider = forge.ProviderGitHub
	case "gitea", "forgejo", "codeberg":
		forceProvider = forge.ProviderGitea
	}

	// If no repo given, default to GitHub provider for interactive mode
	if repo == "" {
		if forceProvider == forge.ProviderGitea {
			host := forgeHost
			if host == "" {
				host = "codeberg.org"
			}
			auth, client := gitea.DetectAuth(ctx, host)
			return gitea.NewProvider(client, auth, host), auth, "", nil
		}
		if forceProvider == forge.ProviderGitLab || forgeHost != "" {
			host := forgeHost
			if host == "" {
				host = "gitlab.com"
			}
			auth, err := gitlab.DetectAuth(ctx, host)
			if err != nil {
				return nil, forge.AuthInfo{}, "", fmt.Errorf("GitLab auth: %w", err)
			}
			token := gitlab.TokenFromAuth(ctx, host)
			client := gitlab.NewClient(host, token)
			return gitlab.NewProvider(client, auth), auth, "", nil
		}
		// Default: GitHub
		client, status, err := gh.CheckAuthConfigured(0)
		if err != nil {
			return nil, forge.AuthInfo{}, "", fmt.Errorf("GitHub auth: %w", err)
		}
		provider := gh.NewGHProvider(client, status)
		auth, _ := provider.Auth(ctx)
		return provider, auth, "", nil
	}

	// Parse repo URL to detect provider
	parsed, err := forge.Parse(forge.Config{
		RepoURL:       repo,
		ForceProvider: forceProvider,
		ForgeHost:     forgeHost,
	})
	if err != nil {
		return nil, forge.AuthInfo{}, "", err
	}

	repoArg := parsed.Owner + "/" + parsed.Repo

	switch parsed.Provider {
	case forge.ProviderGitLab:
		auth, err := gitlab.DetectAuth(ctx, parsed.Host)
		if err != nil {
			return nil, forge.AuthInfo{}, "", fmt.Errorf("GitLab auth: %w", err)
		}
		token := gitlab.TokenFromAuth(ctx, parsed.Host)
		client := gitlab.NewClient(parsed.Host, token)
		return gitlab.NewProvider(client, auth), auth, repoArg, nil

	case forge.ProviderGitHub:
		client, status, err := gh.CheckAuthConfigured(0)
		if err != nil {
			return nil, forge.AuthInfo{}, "", fmt.Errorf("GitHub auth: %w", err)
		}
		provider := gh.NewGHProvider(client, status)
		auth, _ := provider.Auth(ctx)
		return provider, auth, repoArg, nil

	case forge.ProviderGitea:
		auth, client := gitea.DetectAuth(ctx, parsed.Host)
		return gitea.NewProvider(client, auth, parsed.Host), auth, repoArg, nil

	default:
		return nil, forge.AuthInfo{}, "", forge.ErrUnsupportedProvider
	}
}

func printHelp() {
	fmt.Print(`spoon - find useful forks

Usage:
  spoon [flags] [owner/repo | https://gitlab.com/group/repo | topic:NAME]

Flags:
  --forge github|gitlab    Override provider detection
  --forge-host HOSTNAME    Self-hosted GitLab/GHES hostname
  --refresh, --no-cache    Bypass cache (re-fetch all data)
  --heat-weights path      Path to JSON weight override file
  --tier N                 Max enrichment tier 1-3 (default 3). 1 fetches no
                           compares at all; 2 skips lone-wolf scoring. Cycled
                           at runtime with the t key.
  --no-cluster             Disable the embedding + clustering pass
  --cluster-top N          Max forks fed to the embedder (default 50)
  --cluster-epsilon F      Cosine distance cutoff (default 0.55)
  --cluster-min-size N     Minimum cluster size (default 3)
                           (The interactive TUI clusters with the built-in
                           lexical embedder — zero-setup, no native runtime.
                           Semantic search + persistence live in the 'spn' CLI.)
  --full-mdg               Build a real Module Dependency Graph for the upstream
                           using personalized PageRank centrality. Phase A
                           supports Go repositories; other languages silently
                           fall back to the directory-centrality proxy. Requires
                           'git' (and 'gh' for GitHub) on PATH. Adds 10-60 s and
                           up to ~1 GB peak disk on first run; cached for 24 h
                           under ~/.cache/spoon/mdg/. Default: off.
  --no-mdg                 Force the directory-centrality proxy even if an
                           earlier flag enabled --full-mdg.
  --no-color               Disable colors
  -h, --help               Show help
  -v, --version            Show version

Examples:
  spoon                                          Interactive mode (GitHub)
  spoon golang/go                                Search forks of golang/go
  spoon topic:terminal                           Pick from the best repos of a
                                                 GitHub topic, then prospect the
                                                 chosen repo's forks
  spoon gitlab.com/inkscape/inkscape             GitLab (auto-detected)
  spoon --forge gitlab group/repo                Force GitLab provider
  spoon --forge-host gitlab.example.com g/repo   Self-hosted GitLab

Keybindings (TUI mode):
  ↑/↓, j/k     Navigate table
  Enter         View fork details
  o             Open fork in browser
  c             Compare fork vs upstream
  y             Yank clone command to clipboard
  /             Filter forks
  n             Search new repository
  s             Cycle sort column
  g             Toggle cluster grouping in the table view (when clusters available)
  r             Refresh (bypass cache)
  Space         Mark/unmark fork
  e             Export marked forks to JSON
  E             Export all forks to JSON
  ?             Help
  q             Quit

Subcommands:
  spoon setup              Check credentials + the FastEmbed embedder
  spoon threads <pr-ref>   Operate on PR review threads (see 'spoon threads --help')

Concepts:
  Provider (forge)   The Git host spoon queries for forks: GitHub or GitLab.
                     It is auto-detected from the repo URL (e.g. a gitlab.com
                     link forces GitLab); override detection with --forge and
                     point at a self-hosted GitLab/GHES instance with
                     --forge-host. Auth is per-provider: the gh CLI / a GitHub
                     token, or the glab CLI / GITLAB_TOKEN.

  Clustering         spoon turns each fork into a vector and groups similar
                     forks using an in-process deterministic lexical embedder
                     over paths/commits/README/diff. Zero setup, no external
                     services. Tune with --cluster-epsilon / --cluster-min-size;
                     disable with --no-cluster.

Tip: Run 'gh auth login' (GitHub) or set GITLAB_TOKEN (GitLab) for higher rate limits.
`)
}
