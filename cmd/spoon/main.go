package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/dump"
	"github.com/svnbjrn/spoon/internal/tui"
)

var version = "0.3.0-dev"

func main() {
	var repo string
	noColor := false
	jsonMode := false
	csvMode := false
	refresh := false
	concurrency := 0
	tier := 0
	topN := 0
	botAllowlist := ""

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

	// Dump modes bypass the TUI
	if jsonMode || csvMode {
		if repo == "" {
			fmt.Fprintln(os.Stderr, "Error: owner/repo is required for --json and --csv modes")
			os.Exit(1)
		}
		parts := strings.SplitN(repo, "/", 2)
		if len(parts) != 2 {
			fmt.Fprintln(os.Stderr, "Error: invalid repository format, use owner/repo")
			os.Exit(1)
		}

		format := "json"
		if csvMode {
			format = "csv"
		}

		err := dump.Run(parts[0], parts[1], dump.Options{
			Format:       format,
			Refresh:      refresh,
			Concurrency:  concurrency,
			Tier:         tier,
			TopN:         topN,
			BotAllowlist: bots,
		}, os.Stdout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	m := tui.NewModel(repo, refresh)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

var validHeatWeightKeys = map[string]bool{
	"recency": true, "stars": true, "sub_forks": true, "releases": true,
	"mna": true, "sync_ratio": true, "feature_ratio": true,
	"lone_wolf": true, "span": true,
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
  spoon [flags] [owner/repo]

Flags:
  --json                   Output JSON to stdout (no TUI)
  --csv                    Output CSV to stdout (no TUI)
  --refresh, --no-cache    Bypass cache (re-fetch all data)
  --concurrency N          Override worker pool size (default: 10 authed, 2 unauthed)
  --tier 1|2|3             Cap enrichment depth
  --top N                  Only enrich top N forks by T1 score
  --bot-allowlist a,b,c    Treat these logins as human contributors
  --heat-weights path      Path to JSON weight override file
  --no-color               Disable colors
  -h, --help               Show help
  -v, --version            Show version

Examples:
  spoon                                  Interactive mode
  spoon golang/go                        Search forks of golang/go
  spoon --json charmbracelet/bubbletea   JSON output
  spoon --csv charmbracelet/bubbletea    CSV output
  spoon --tier 1 golang/go               T1 only (no compare calls)
  spoon --top 5 golang/go                Enrich only top 5 forks

Keybindings (TUI mode):
  ↑/↓, j/k     Navigate table
  Enter         View fork details
  o             Open fork in browser
  y             Yank clone command to clipboard
  /             Filter forks
  n             Search new repository
  s             Cycle sort column
  t             Cycle max enrichment tier
  r             Refresh (bypass cache)
  Space         Mark/unmark fork
  e             Export marked forks to JSON
  E             Export all forks to JSON
  ?             Help
  q             Quit

Tip: Run 'gh auth login' first for 5,000 requests/hour (vs 60 unauthenticated).
`)
}
