// cmd/spn/main.go
package main

import (
	"fmt"
	"os"
)

const version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "-h", "--help":
		printHelp()
		os.Exit(0)
	case "-v", "--version":
		fmt.Printf("spn %s\n", version)
		os.Exit(0)
	case "threads":
		os.Exit(runThreads(os.Args[2:]))
	case "pr":
		os.Exit(runPR(os.Args[2:]))
	case "forks":
		os.Exit(runForks(os.Args[2:]))
	case "embed":
		os.Exit(runEmbed(os.Args[2:]))
	case "repo":
		os.Exit(runRepo(os.Args[2:]))
	case "sidecar":
		os.Exit(runSidecar(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "spn: unknown subcommand %q\n", os.Args[1])
		printHelp()
		os.Exit(2)
	}
}

func printHelp() {
	fmt.Print(`spn — agent-shaped CLI for spoon

Usage:
  spn <noun> <verb> [args]

Nouns and verbs:
  threads list <pr-ref> [--all] [--filter MODE]
  threads next <pr-ref>
  threads reply <pr-ref> <thread-id> --body T | --body-file PATH
                                    | --suggest BODY [--intro TEXT]
                                    | --suggest-file PATH [--intro TEXT]
  threads resolve <pr-ref> <thread-id> [--body T | --body-file PATH] [--dry-run]
  threads resolve-all <pr-ref> [--outdated] [--dry-run]
  threads unresolve-all <pr-ref> [--dry-run]
  threads apply-suggestion <pr-ref> <thread-id>
        [--suggestion-index N] [--dry-run] [--force] [--repo-root PATH]
  threads list-prs <owner/repo> [--limit N] [--state open]

  --dry-run on resolve / resolve-all / unresolve-all previews the mutation:
            the fetch + policy gates run, but no GraphQL resolveReviewThread
            (or unresolveReviewThread) is issued. Output is marked dryRun=true.
  pr status <pr-ref>
  forks list <repo> [--tier 1|2|3] [--top N] [--budget N] [--shortlist N] [--bot-allowlist L] [--refresh] [--csv] [--forge github|gitlab] [--forge-host H]
                    [--no-cluster] [--cluster-top N] [--embedder URL] [--embedder-model NAME]
                    [--embedder-backend ollama|sidecar|openai] [--sidecar-endpoint URL]
                    [--labeler URL] [--labeler-model NAME] [--cluster-epsilon F] [--cluster-min-size N] [--auto-pull]
                    [--full-mdg] [--no-mdg]
        --budget N caps the expensive per-fork compare/contributors calls to N,
        choosing which forks to spend them on by an optimal-stopping ("secretary
        problem") gate over a cheap divergence signal — so a rate-limited scan of
        a huge fork network spends its budget on forks that likely diverged
        rather than the most popular ones. Re-running resumes via the 24h compare
        cache. --top N instead deep-scans the top N by surface score.
        --shortlist N emits only the top N forks by Robbins expected rank (lower
        = more likely best), adding expectedRank + rankConfidence to each.
        Flags go AFTER 'forks list <repo>'. --embedder URL is an Ollama API
        endpoint; the Python sidecar is selected with --embedder-backend sidecar
        --sidecar-endpoint URL (different protocol — not --embedder). For an
        OpenAI-compatible endpoint (e.g. OVMS on a GPU): --embedder-backend
        openai --embedder URL --embedder-model NAME.
  embed status [--endpoint URL]
  embed pull <model> [--endpoint URL]
  embed models
  repo centrality <owner/repo> [--forge github] [--forge-host H]
  sidecar install [--runtime uv|python|docker] [--port N] [--device cpu|cuda] [--no-enable]
  sidecar status
  sidecar uninstall [--purge]

PR refs accept:
  owner/repo#42
  https://github.com/owner/repo/pull/42
  #42                  (uses local repo context)

Output:
  Success: bare JSON on stdout (single value for reads; NDJSON for forks list).
  Failure: structured JSON envelope on stderr with code, message, remediation,
           retryable, optional retry_after_seconds, and details.

Exit codes: 0 success; 2 user error / policy; 1 everything else.

Authentication: gh auth login (GitHub).
`)
}
