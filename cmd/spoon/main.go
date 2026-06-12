package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/genai"
	"github.com/svnbjrn/spoon/internal/gitea"
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

	// Subcommand dispatch: "spoon setup ..."
	if len(os.Args) >= 2 && os.Args[1] == "setup" {
		os.Exit(runSetup(os.Args[2:]))
	}

	var repo string
	noColor := false
	refresh := false
	concurrency := 0
	forgeFlag := ""
	forgeHost := ""

	// Cluster pipeline flags (T9). Clustering is ON by default; --no-cluster
	// turns it off. Embedding runs in-process — no external services involved.
	noCluster := false
	clusterTop := 50
	clusterEpsilon := 0.0 // resolved per backend below unless set explicitly
	clusterMinSize := 3
	embedderBackend := ""
	openvinoModel := ""
	openvinoDevice := ""
	openvinoPooling := ""

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
		case "--embedder-backend":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --embedder-backend requires a value")
				os.Exit(1)
			}
			i++
			embedderBackend = strings.ToLower(args[i])
		case "--openvino-model":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --openvino-model requires a value")
				os.Exit(1)
			}
			i++
			openvinoModel = args[i]
		case "--openvino-device":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --openvino-device requires a value")
				os.Exit(1)
			}
			i++
			openvinoDevice = args[i]
		case "--openvino-pooling":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --openvino-pooling requires a value")
				os.Exit(1)
			}
			i++
			openvinoPooling = strings.ToLower(args[i])
			if _, ok := embed.ParsePooling(openvinoPooling); !ok {
				fmt.Fprintln(os.Stderr, "Error: --openvino-pooling must be 'cls', 'mean', or 'last'")
				os.Exit(1)
			}
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

	_ = concurrency // TODO: pass to auth overrides

	ctx := context.Background()

	// Resolve the embedder backend (flag > env > config > builtin) and
	// construct it up front so a misconfigured openvino setup fails fast
	// instead of silently degrading clustering mid-run.
	embedderBackend, ovCfg := resolveEmbedderConfig(embedderBackend, openvinoModel, openvinoDevice, openvinoPooling)
	embedder, embedderID, closeEmbedder, err := embed.SelectBackend(embedderBackend, ovCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer closeEmbedder()
	if clusterEpsilon == 0 {
		clusterEpsilon = defaultEpsilonFor(embedderBackend)
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
	}
	if embedderBackend != "" && embedderBackend != embed.BackendBuiltin {
		tuiClusterOpts.Embedder = embedder
		tuiClusterOpts.EmbedderID = embedderID
		// Zero-shot categories need a semantic embedder.
		tuiClusterOpts.Categorize = embedderBackend == embed.BackendOpenVINO
	}
	if !noCluster {
		polisher, closePolisher, lerr := newLabelPolisher()
		if lerr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", lerr)
			os.Exit(1)
		}
		if polisher != nil {
			tuiClusterOpts.LabelPolisher = polisher
			defer closePolisher()
		}
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

// resolveEmbedderConfig layers the embedder backend settings: flag > env >
// config file. Returns the resolved backend name and OpenVINO config.
func resolveEmbedderConfig(backend, model, device, pooling string) (string, embed.OpenVINOConfig) {
	var fileCfg config.EmbedderConfig
	if cfg, cerr := config.LoadDefault(); cerr != nil {
		fmt.Fprintf(os.Stderr, "warning: ignoring spoon config: %v\n", cerr)
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

// defaultEpsilonFor returns the per-backend default cosine-distance cutoff:
// neural embeddings (openvino) separate at a tighter scale than the lexical
// embedder.
func defaultEpsilonFor(backend string) float64 {
	if backend == embed.BackendOpenVINO {
		return 0.35
	}
	return 0.55
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

	case forge.ProviderGitea:
		auth, client := gitea.DetectAuth(ctx, parsed.Host)
		return gitea.NewProvider(client, auth, parsed.Host), auth, repoArg, nil

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
  --cluster-epsilon F      Cosine distance cutoff (default 0.55 builtin,
                           0.35 openvino)
  --cluster-min-size N     Minimum cluster size (default 3)
  --embedder-backend NAME  'builtin' (default; zero-setup lexical embedder)
                           or 'openvino' (in-process transformer encoder on
                           an Intel GPU; needs a binary built with
                           -tags openvino). Env: $SPOON_EMBEDDER_BACKEND
  --openvino-model PATH    Model dir with openvino_model.xml +
                           openvino_tokenizer.xml (export via
                           'ovms --pull --task embeddings' or optimum-cli).
                           Env: $SPOON_OPENVINO_MODEL
  --openvino-device DEV    OpenVINO device for the encoder (default GPU).
                           Env: $SPOON_OPENVINO_DEVICE
  --openvino-pooling MODE  cls|mean|last (default: the model dir's
                           graph.pbtxt, else cls)
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
  spoon setup              Check credentials + provision OpenVINO models
  spoon threads <pr-ref>   Operate on PR review threads (see 'spoon threads --help')

Concepts:
  Provider (forge)   The Git host spoon queries for forks: GitHub or GitLab.
                     It is auto-detected from the repo URL (e.g. a gitlab.com
                     link forces GitLab); override detection with --forge and
                     point at a self-hosted GitLab/GHES instance with
                     --forge-host. Auth is per-provider: the gh CLI / a GitHub
                     token, or the glab CLI / GITLAB_TOKEN.

  Clustering         spoon turns each fork into a vector and groups similar
                     forks. Two in-process backends (no external services):
                       builtin   (default) deterministic lexical embedder
                                 over paths/commits/README/diff. Zero setup.
                       openvino  a transformer encoder (e.g. arctic-embed)
                                 run via the OpenVINO runtime on an Intel
                                 GPU. Needs 'go build -tags openvino', an
                                 exported model dir (--openvino-model), and
                                 the OpenVINO + tokenizers runtime libs.
                     Tune with --cluster-epsilon / --cluster-min-size;
                     disable with --no-cluster.

Tip: Run 'gh auth login' (GitHub) or set GITLAB_TOKEN (GitLab) for higher rate limits.
`)
}
