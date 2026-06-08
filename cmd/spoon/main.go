package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/gitlab"
	"github.com/svnbjrn/spoon/internal/tui"
)

var version = "0.3.0-dev"

func main() {
	// Subcommand dispatch: "spoon threads <pr-ref> ..."
	if len(os.Args) >= 2 && os.Args[1] == "threads" {
		os.Exit(runThreads(os.Args[2:]))
	}

	// Subcommand dispatch: "spoon embed <verb> ..."
	if len(os.Args) >= 2 && os.Args[1] == "embed" {
		os.Exit(runSpoonEmbed(os.Args[2:]))
	}

	// Subcommand dispatch: "spoon setup ..."
	if len(os.Args) >= 2 && os.Args[1] == "setup" {
		os.Exit(runSetup(os.Args[2:]))
	}

	// Subcommand dispatch: "spoon sidecar <verb> ..."
	if len(os.Args) >= 2 && os.Args[1] == "sidecar" {
		os.Exit(runSidecar(os.Args[2:]))
	}

	var repo string
	noColor := false
	refresh := false
	concurrency := 0
	forgeFlag := ""
	forgeHost := ""

	// Cluster pipeline flags (T9). Clustering is ON by default; --no-cluster
	// turns it off. When enabled, the pipeline still degrades silently if the
	// embedder is unreachable or no embedding model is installed.
	noCluster := false
	clusterTop := 50
	embedderURL := ""
	embedderModel := ""
	embedderBackend := ""
	sidecarEndpoint := ""
	labelerURL := ""
	labelerModel := ""
	clusterEpsilon := 0.35
	clusterMinSize := 3
	autoPull := os.Getenv("SPOON_AUTO_PULL") == "1"
	noPrompt := false

	// MDG centrality backend. Off by default; --full-mdg opts in. --no-mdg
	// reverts to off (useful for users who set the env var elsewhere).
	fullMDG := false

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
			if forgeFlag != "github" && forgeFlag != "gitlab" {
				fmt.Fprintln(os.Stderr, "Error: --forge must be 'github' or 'gitlab'")
				os.Exit(1)
			}
		case "--forge-host":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --forge-host requires a value")
				os.Exit(1)
			}
			i++
			forgeHost = args[i]
		case "--concurrency":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --concurrency requires a value")
				os.Exit(1)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				fmt.Fprintln(os.Stderr, "Error: --concurrency requires a positive integer")
				os.Exit(1)
			}
			concurrency = n
		case "--heat-weights":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --heat-weights requires a value")
				os.Exit(1)
			}
			i++
			if err := validateHeatWeights(args[i]); err != nil {
				fmt.Fprintf(os.Stderr, "Error: --heat-weights: %v\n", err)
				os.Exit(1)
			}
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
		case "--embedder":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --embedder requires a value")
				os.Exit(1)
			}
			i++
			embedderURL = args[i]
		case "--embedder-model":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --embedder-model requires a value")
				os.Exit(1)
			}
			i++
			embedderModel = args[i]
		case "--embedder-backend":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --embedder-backend requires a value")
				os.Exit(1)
			}
			i++
			val := strings.ToLower(args[i])
			if val != "ollama" && val != "sidecar" && val != "openai" {
				fmt.Fprintln(os.Stderr, "Error: --embedder-backend must be 'ollama', 'sidecar', or 'openai'")
				os.Exit(1)
			}
			embedderBackend = val
		case "--sidecar-endpoint":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --sidecar-endpoint requires a value")
				os.Exit(1)
			}
			i++
			sidecarEndpoint = args[i]
		case "--labeler":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --labeler requires a value")
				os.Exit(1)
			}
			i++
			labelerURL = args[i]
		case "--labeler-model":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --labeler-model requires a value")
				os.Exit(1)
			}
			i++
			labelerModel = args[i]
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
		case "--auto-pull":
			autoPull = true
		case "--no-prompt":
			noPrompt = true
		case "--full-mdg":
			fullMDG = true
		case "--no-mdg":
			fullMDG = false
		default:
			if !strings.HasPrefix(args[i], "-") && strings.Contains(args[i], "/") {
				repo = args[i]
			} else {
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[i])
				printHelp()
				os.Exit(1)
			}
		}
	}

	if noColor {
		os.Setenv("NO_COLOR", "1")
	}

	// Layer saved embedder defaults under flags/env (flags > env > config >
	// built-in). Provider/host are intentionally NOT layered — the repo URL
	// determines the forge. A bad config warns but never blocks a run.
	if cfg, cerr := config.LoadDefault(); cerr != nil {
		fmt.Fprintf(os.Stderr, "warning: ignoring spoon config: %v\n", cerr)
	} else if cfg != nil {
		embedderBackend = strings.ToLower(config.Coalesce(embedderBackend, os.Getenv("SPOON_EMBEDDER_BACKEND"), cfg.Embedder.Backend))
		embedderURL = config.Coalesce(embedderURL, os.Getenv("SPOON_EMBEDDER_URL"), cfg.Embedder.Endpoint)
		embedderModel = config.Coalesce(embedderModel, cfg.Embedder.Model)
		sidecarEndpoint = config.Coalesce(sidecarEndpoint, os.Getenv("SPOON_SIDECAR_ENDPOINT"), cfg.Embedder.SidecarEndpoint)
		labelerModel = config.Coalesce(labelerModel, cfg.Embedder.LabelerModel)
	}

	_ = concurrency // TODO: pass to auth overrides

	// Detect provider from repo URL and flags
	ctx := context.Background()
	provider, auth, repoArg, err := createProvider(ctx, repo, forgeFlag, forgeHost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	tuiClusterOpts := tui.ClusterOptions{
		Enabled:           !noCluster,
		TopN:              clusterTop,
		Endpoint:          embedderURL,
		ModelOverride:     embedderModel,
		Backend:           embedderBackend,
		SidecarEndpoint:   sidecarEndpoint,
		LabelerEndpoint:   labelerURL,
		LabelerModel:      labelerModel,
		Epsilon:           clusterEpsilon,
		MinClusterSize:    clusterMinSize,
		AutoPull:          autoPull,
		NoPrompt:          noPrompt,
		Refresh:           refresh,
		CentralityBackend: backendFor(fullMDG),
	}
	m := tui.NewModelWithCluster(provider, auth, repoArg, refresh, tuiClusterOpts)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
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
	}

	// If no repo given, default to GitHub provider for interactive mode
	if repo == "" {
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
		client, status, err := gh.CheckAuth()
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
		client, status, err := gh.CheckAuth()
		if err != nil {
			return nil, forge.AuthInfo{}, "", fmt.Errorf("GitHub auth: %w", err)
		}
		provider := gh.NewGHProvider(client, status)
		auth, _ := provider.Auth(ctx)
		return provider, auth, repoArg, nil

	default:
		return nil, forge.AuthInfo{}, "", forge.ErrUnsupportedProvider
	}
}

var validHeatWeightKeys = map[string]bool{
	"recency": true, "stars": true, "sub_forks": true, "releases": true,
	"mna": true, "sync_ratio": true, "feature_ratio": true,
	"lone_wolf": true, "span": true, "novelty": true,
}

func validateHeatWeights(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	var weights map[string]float64
	if err := json.Unmarshal(data, &weights); err != nil {
		return fmt.Errorf("parsing JSON: %w", err)
	}

	for key, val := range weights {
		if !validHeatWeightKeys[key] {
			return fmt.Errorf("unknown key %q", key)
		}
		if val < 0 || val > 2.0 {
			return fmt.Errorf("value for %q must be in [0.0, 2.0], got %v", key, val)
		}
	}
	return nil
}

func printHelp() {
	fmt.Print(`spoon - find useful forks

Usage:
  spoon [flags] [owner/repo | https://gitlab.com/group/repo]

Flags:
  --forge github|gitlab    Override provider detection
  --forge-host HOSTNAME    Self-hosted GitLab/GHES hostname
  --refresh, --no-cache    Bypass cache (re-fetch all data)
  --concurrency N          Override worker pool size (default: 10 authed, 2 unauthed)
  --heat-weights path      Path to JSON weight override file
  --no-cluster             Disable the embedding + clustering pass
  --cluster-top N          Max forks fed to the embedder (default 50)
  --embedder URL           Embedding endpoint (default $SPOON_EMBEDDER_URL)
  --embedder-model NAME    Explicit embedding model (default: auto-pick)
  --embedder-backend NAME  Embedder backend: 'ollama' (default), 'sidecar', or
                           'openai' (any OpenAI-compatible endpoint, e.g. OVMS)
  --sidecar-endpoint URL   Python sidecar endpoint (default http://localhost:8766)
  --labeler URL            Optional LLM polish endpoint (default: heuristic)
  --labeler-model NAME     LLM polish model (default: llama3.2:3b)
  --cluster-epsilon F      Cosine distance cutoff (default 0.35)
  --cluster-min-size N     Minimum cluster size (default 3)
  --auto-pull              Pull missing embedding model without prompting
  --no-prompt              Skip pull prompt when no embedding model is installed
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
  spoon setup              Check provider credentials + embedder, pull/install fixes
  spoon sidecar <verb>     Install/status/uninstall the embedding sidecar service
  spoon threads <pr-ref>   Operate on PR review threads (see 'spoon threads --help')
  spoon embed status       Human-readable Ollama embedder status

Concepts:
  Provider (forge)   The Git host spoon queries for forks: GitHub or GitLab.
                     It is auto-detected from the repo URL (e.g. a gitlab.com
                     link forces GitLab); override detection with --forge and
                     point at a self-hosted GitLab/GHES instance with
                     --forge-host. Auth is per-provider: the gh CLI / a GitHub
                     token, or the glab CLI / GITLAB_TOKEN.

  Embedder backend   How spoon turns each fork into a vector so it can cluster
                     similar forks (--embedder-backend). Three choices:
                       ollama  (default) zero-setup, serves nomic-embed-text
                               locally via Ollama. Good enough, nothing to run.
                       sidecar a separate Python HTTP service you start
                               yourself (--sidecar-endpoint, default
                               http://localhost:8766) that serves a stronger
                               model (arctic-embed-l-v2). Sharper similarity
                               rankings at the cost of ~2 GB RAM and managing a
                               process. Setup: 'spoon sidecar install'.
                       openai  any OpenAI-compatible embeddings endpoint
                               (--embedder URL, --embedder-model NAME). Use this
                               to run embeddings on a GPU via OpenVINO Model
                               Server (OVMS) or to point at vLLM / the OpenAI API.

Tip: Run 'gh auth login' (GitHub) or set GITLAB_TOKEN (GitLab) for higher rate limits.
`)
}
