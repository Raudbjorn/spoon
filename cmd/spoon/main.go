package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/dump"
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

	var repo string
	noColor := false
	jsonMode := false
	csvMode := false
	refresh := false
	concurrency := 0
	tier := 0
	topN := 0
	botAllowlist := ""
	forgeFlag := ""
	forgeHost := ""
	outputPath := ""

	// Cluster pipeline flags (T9). Default: clustering is OFF unless --no-cluster
	// is absent AND another cluster flag is explicitly set. To keep the existing
	// dump behaviour stable while still allowing opt-in, clustering is enabled
	// when the user does NOT pass --no-cluster — i.e. defaulting to ON only when
	// embedder is reachable. The pipeline degrades silently otherwise.
	noCluster := false
	clusterTop := 50
	embedderURL := ""
	embedderModel := ""
	labelerURL := ""
	labelerModel := ""
	clusterEpsilon := 0.35
	clusterMinSize := 3
	autoPull := os.Getenv("SPOON_AUTO_PULL") == "1"
	noPrompt := false

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
		case "--json":
			jsonMode = true
		case "--csv":
			csvMode = true
		case "--refresh", "--no-cache":
			refresh = true
		case "--forge":
			if i+1 < len(args) {
				i++
				forgeFlag = strings.ToLower(args[i])
				if forgeFlag != "github" && forgeFlag != "gitlab" {
					fmt.Fprintln(os.Stderr, "Error: --forge must be 'github' or 'gitlab'")
					os.Exit(1)
				}
			}
		case "--forge-host":
			if i+1 < len(args) {
				i++
				forgeHost = args[i]
			}
		case "-o", "--output":
			if i+1 < len(args) {
				i++
				outputPath = args[i]
			}
		case "--concurrency":
			if i+1 < len(args) {
				i++
				n, err := strconv.Atoi(args[i])
				if err != nil || n < 1 {
					fmt.Fprintln(os.Stderr, "Error: --concurrency requires a positive integer")
					os.Exit(1)
				}
				concurrency = n
			}
		case "--tier":
			if i+1 < len(args) {
				i++
				n, err := strconv.Atoi(args[i])
				if err != nil || n < 1 || n > 3 {
					fmt.Fprintln(os.Stderr, "Error: --tier must be 1, 2, or 3")
					os.Exit(1)
				}
				tier = n
			}
		case "--top":
			if i+1 < len(args) {
				i++
				n, err := strconv.Atoi(args[i])
				if err != nil || n < 1 {
					fmt.Fprintln(os.Stderr, "Error: --top requires a positive integer")
					os.Exit(1)
				}
				topN = n
			}
		case "--bot-allowlist":
			if i+1 < len(args) {
				i++
				botAllowlist = args[i]
			}
		case "--heat-weights":
			if i+1 < len(args) {
				i++
				if err := validateHeatWeights(args[i]); err != nil {
					fmt.Fprintf(os.Stderr, "Error: --heat-weights: %v\n", err)
					os.Exit(1)
				}
			}
		case "--no-cluster":
			noCluster = true
		case "--cluster-top":
			if i+1 < len(args) {
				i++
				n, err := strconv.Atoi(args[i])
				if err != nil || n < 1 {
					fmt.Fprintln(os.Stderr, "Error: --cluster-top requires a positive integer")
					os.Exit(1)
				}
				clusterTop = n
			}
		case "--embedder":
			if i+1 < len(args) {
				i++
				embedderURL = args[i]
			}
		case "--embedder-model":
			if i+1 < len(args) {
				i++
				embedderModel = args[i]
			}
		case "--labeler":
			if i+1 < len(args) {
				i++
				labelerURL = args[i]
			}
		case "--labeler-model":
			if i+1 < len(args) {
				i++
				labelerModel = args[i]
			}
		case "--cluster-epsilon":
			if i+1 < len(args) {
				i++
				f, err := strconv.ParseFloat(args[i], 64)
				if err != nil || f < 0 {
					fmt.Fprintln(os.Stderr, "Error: --cluster-epsilon requires a non-negative number")
					os.Exit(1)
				}
				clusterEpsilon = f
			}
		case "--cluster-min-size":
			if i+1 < len(args) {
				i++
				n, err := strconv.Atoi(args[i])
				if err != nil || n < 1 {
					fmt.Fprintln(os.Stderr, "Error: --cluster-min-size requires a positive integer")
					os.Exit(1)
				}
				clusterMinSize = n
			}
		case "--auto-pull":
			autoPull = true
		case "--no-prompt":
			noPrompt = true
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

	// Parse bot allowlist
	var bots map[string]bool
	if botAllowlist != "" {
		bots = make(map[string]bool)
		for _, b := range strings.Split(botAllowlist, ",") {
			b = strings.TrimSpace(b)
			if b != "" {
				bots[strings.ToLower(b)] = true
			}
		}
	}

	_ = concurrency // TODO: pass to auth overrides

	// Detect provider from repo URL and flags
	ctx := context.Background()
	provider, auth, repoArg, err := createProvider(ctx, repo, forgeFlag, forgeHost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Dump modes bypass the TUI
	if jsonMode || csvMode {
		if repoArg == "" {
			fmt.Fprintln(os.Stderr, "Error: owner/repo is required for --json and --csv modes")
			os.Exit(1)
		}

		// Split into owner/repo for the dump API
		owner, repoName := splitRepo(repoArg)
		if owner == "" || repoName == "" {
			fmt.Fprintln(os.Stderr, "Error: invalid repository format, use owner/repo")
			os.Exit(1)
		}

		format := "json"
		if csvMode {
			format = "csv"
		}

		var w *os.File
		if outputPath != "" {
			w, err = os.Create(outputPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error creating output file: %v\n", err)
				os.Exit(1)
			}
			defer w.Close()
		} else {
			w = os.Stdout
		}

		err = dump.Run(provider, auth, owner, repoName, dump.Options{
			Format:       format,
			Refresh:      refresh,
			Tier:         tier,
			TopN:         topN,
			BotAllowlist: bots,
			Cluster: dump.ClusterOptions{
				Enabled:         !noCluster,
				TopN:            clusterTop,
				Endpoint:        embedderURL,
				ModelOverride:   embedderModel,
				LabelerEndpoint: labelerURL,
				LabelerModel:    labelerModel,
				Epsilon:         clusterEpsilon,
				MinClusterSize:  clusterMinSize,
				AutoPull:        autoPull,
				NoPrompt:        noPrompt,
				NonInteractive:  true, // --json / --csv runs are always non-interactive
				Refresh:         refresh,
			},
		}, w)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if outputPath != "" {
			fmt.Fprintf(os.Stderr, "Exported to %s\n", outputPath)
		}
		return
	}

	tuiClusterOpts := tui.ClusterOptions{
		Enabled:         !noCluster,
		TopN:            clusterTop,
		Endpoint:        embedderURL,
		ModelOverride:   embedderModel,
		LabelerEndpoint: labelerURL,
		LabelerModel:    labelerModel,
		Epsilon:         clusterEpsilon,
		MinClusterSize:  clusterMinSize,
		AutoPull:        autoPull,
		NoPrompt:        noPrompt,
		Refresh:         refresh,
	}
	m := tui.NewModelWithCluster(provider, auth, repoArg, refresh, tuiClusterOpts)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
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

// splitRepo splits "owner/repo" or "group/subgroup/repo" into owner and repo parts.
func splitRepo(s string) (owner, repo string) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
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
  --json                   Output JSON (no TUI; stdout or -o file)
  --csv                    Output CSV (no TUI; stdout or -o file)
  -o, --output PATH        Write output to file instead of stdout
  --forge github|gitlab    Override provider detection
  --forge-host HOSTNAME    Self-hosted GitLab/GHES hostname
  --refresh, --no-cache    Bypass cache (re-fetch all data)
  --concurrency N          Override worker pool size (default: 10 authed, 2 unauthed)
  --tier 1|2|3             Cap enrichment depth
  --top N                  Only enrich top N forks by T1 score
  --bot-allowlist a,b,c    Treat these logins as human contributors
  --heat-weights path      Path to JSON weight override file
  --no-cluster             Disable the embedding + clustering pass
  --cluster-top N          Max forks fed to the embedder (default 50)
  --embedder URL           Embedding endpoint (default $SPOON_EMBEDDER_URL)
  --embedder-model NAME    Explicit embedding model (default: auto-pick)
  --labeler URL            Optional LLM polish endpoint (default: heuristic)
  --labeler-model NAME     LLM polish model (default: llama3.2:3b)
  --cluster-epsilon F      Cosine distance cutoff (default 0.35)
  --cluster-min-size N     Minimum cluster size (default 3)
  --auto-pull              Pull missing embedding model without prompting
  --no-prompt              Skip pull prompt when no embedding model is installed
  --no-color               Disable colors
  -h, --help               Show help
  -v, --version            Show version

Examples:
  spoon                                          Interactive mode (GitHub)
  spoon golang/go                                Search forks of golang/go
  spoon gitlab.com/inkscape/inkscape             GitLab (auto-detected)
  spoon --forge gitlab group/repo                Force GitLab provider
  spoon --forge-host gitlab.example.com g/repo   Self-hosted GitLab
  spoon --json charmbracelet/bubbletea           JSON output
  spoon --csv charmbracelet/bubbletea            CSV output
  spoon --tier 1 golang/go                       T1 only (no compare calls)
  spoon --top 5 golang/go                        Enrich only top 5 forks

Keybindings (TUI mode):
  ↑/↓, j/k     Navigate table
  Enter         View fork details
  o             Open fork in browser
  c             Compare fork vs upstream
  y             Yank clone command to clipboard
  /             Filter forks
  n             Search new repository
  s             Cycle sort column
  r             Refresh (bypass cache)
  Space         Mark/unmark fork
  e             Export marked forks to JSON
  E             Export all forks to JSON
  ?             Help
  q             Quit

Subcommands:
  spoon threads <pr-ref>   Operate on PR review threads (see 'spoon threads --help')

Tip: Run 'gh auth login' (GitHub) or set GITLAB_TOKEN (GitLab) for higher rate limits.
`)
}
