// cmd/spn/main.go
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/svnbjrn/spoon/internal/agentio"
)

var version = "0.1.0-dev"

// usageRemediation lists the valid nouns for the two top-level failure paths.
const usageRemediation = "Run 'spn --help' for usage. Nouns: threads, pr, forks, search, repo."

func main() { os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr)) }

// dispatch routes a top-level invocation and returns the process exit code. Both
// failure paths (no subcommand, unknown subcommand) emit the structured agentio
// envelope on stderr and leave stdout clean, so a consuming agent branches on
// code:"bad_input" instead of choking on a plain-text line plus a stdout usage
// dump.
func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		return agentio.NewError(agentio.CodeBadInput, "missing subcommand", usageRemediation).Emit(stderr)
	}
	switch args[0] {
	case "-h", "--help":
		printHelp(stdout)
		return 0
	case "-v", "--version":
		fmt.Fprintf(stdout, "spn %s\n", version)
		return 0
	case "threads":
		return runThreads(args[1:])
	case "pr":
		return runPR(args[1:])
	case "forks":
		// `spn forks eval` is a sub-verb of forks; dispatch by args[1] when
		// present. Everything else (incl. `spn forks list`) keeps runForks.
		if len(args) >= 2 && args[1] == "eval" {
			return runEval(args[2:])
		}
		return runForks(args[1:])
	case "search":
		return runSearch(args[1:])
	case "repo":
		return runRepo(args[1:])
	default:
		return agentio.NewError(agentio.CodeBadInput, fmt.Sprintf("unknown subcommand %q", args[0]), usageRemediation).Emit(stderr)
	}
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `spn — agent-shaped CLI for spoon

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
  forks list <repo|topic:NAME> [--tier 1|2|3] [--top N] [--budget N] [--shortlist N] [--bot-allowlist L] [--refresh|--no-cache] [--csv] [--forge github|gitlab] [--forge-host H]
                    [--rpm N] [--files] [--commits] [--commit-files]
                    [--commit-file-budget N] [--web-diff]
                    [--no-cluster] [--cluster-top N] [--cluster-epsilon F] [--cluster-min-size N]
                    [--no-embed] [--heat-weights W] [--strict-mdg] [--full-mdg] [--no-mdg]
                    [--query "intent"] [--priors PATH]
                    [--sibling-sim | --no-sibling-sim] [--sibling-sim-mode MODE] [--owner-cache-ttl DUR]
                    [--topic-repos N] [--topic-lanes LIST] [--topic-lane-budget N]
        --budget N caps the expensive per-fork compare/contributors calls to N.
        --top N instead deep-scans the top N by surface score.
        --shortlist N emits only the top N forks by Robbins expected rank (lower
        = more likely best), adding expectedRank + rankConfidence to each.
        Flags go AFTER 'forks list <repo>'. The fastembed embedder powers
        persistence + semantic indexing (and 'spn search') by default; it
        needs onnxruntime (set ONNX_PATH to libonnxruntime.so, run 'spoon
        setup'). If it is unavailable the run degrades with a warning rather
        than failing. Pass --no-embed (or SPOON_NO_EMBED=1) to skip embedding.
        Clustering runs in-process and needs nothing installed.
        FastEmbed requires ONNX_PATH and is the only backend used for durable
        semantic indexing and 'spn search'. --commit-files implies --files and
        --commits and defaults to a 100-commit run budget. --web-diff is an
        unstable SPOON_GH_COOKIE-gated HTML fallback, not a supported API.
        topic:NAME evaluates the fork networks of the best repositories
        representing a GitHub topic (selection by stars + fork-network size +
        recency; cap with --topic-repos N, default 5). Each record gains an
        "upstream" field; selections are reported as info envelopes on stderr.
        --topic-lanes LIST opts into comma-separated topic candidate lanes
        (default,stars,updated,forks); --topic-lane-budget N caps each lane.
        --query "intent" scores every enriched fork against a free-text
        intent (cross-encoder reranker when configured, lexical fallback
        otherwise), sorts by relevance, and adds queryScore/queryMethod to
        each record. With the openvino backend each fork also gets a
        zero-shot 'category' facet, and a configured labeler polishes
        cluster labels with an in-process LLM.
        --priors PATH scores each fork against a JSON interest spec
        (paths/keywords/languages/owners allow+deny); adds
        priorScore/priorReasons and, when neither --query nor --shortlist
        is set, lists matched forks before unmatched (heat order within
        each lane); never hides forks or changes heat.
        --sibling-sim / --no-sibling-sim toggles P2 distant-relation
        discovery: one /search/repositories + ~50 README fetches + one
        batched embed. The default upstream_readme mode folds the max cosine
        to the upstream README into every fork's Heat.SiblingSim; opt in to
        per-fork digest matching with --sibling-sim-mode fork_intent
        (accepted values: upstream_readme, fork_intent). On by default for
        standard runs; opt-in via --sibling-sim or --sibling-sim-mode for
        topic mode (5x cost multiplier per upstream).
        --owner-cache-ttl DUR overrides the owner-profile on-disk
        cache TTL (default 24h). Use 0 to force a fresh fetch every
        run, or a short value (e.g. 1h) for more aggressive refresh.
  search "query" [--repo owner/repo] [--top N]
        Ranks indexed forks against the query using the fastembed semantic
        index built by 'forks list'. NDJSON on stdout is the default; there is
        no --json flag.
  repo centrality <owner/repo> [--forge github] [--forge-host H]

Output:
  Success: bare JSON on stdout (single value for reads; NDJSON for forks list).
  Failure: structured JSON envelope on stderr with code, message, remediation,
           retryable, optional retry_after_seconds, and details.

Exit codes: 0 success; 2 user error / policy; 1 everything else.

Authentication: gh auth login (GitHub).
`)
}
