// cmd/spn/main.go
package main

import (
	"fmt"
	"os"
)

var version = "0.1.0-dev"

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
		// `spn forks eval` is a sub-verb of forks; dispatch by argv[2]
		// when present. Everything else (incl. `spn forks list`) keeps
		// its existing runForks path.
		if len(os.Args) >= 3 && os.Args[2] == "eval" {
			os.Exit(runEval(os.Args[3:]))
		}
		os.Exit(runForks(os.Args[2:]))
	case "search":
		os.Exit(runSearch(os.Args[2:]))
	case "repo":
		os.Exit(runRepo(os.Args[2:]))
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
  forks list <repo|topic:NAME> [--tier 1|2|3] [--top N] [--budget N] [--shortlist N] [--bot-allowlist L] [--refresh] [--csv] [--forge github|gitlab] [--forge-host H]
                    [--rpm N] [--files] [--commits] [--commit-files]
                    [--commit-file-budget N] [--web-diff]
                    [--no-cluster] [--cluster-top N] [--cluster-epsilon F] [--cluster-min-size N]
                    [--embedder-backend builtin|openvino|fastembed]
                    [--openvino-model PATH] [--openvino-device DEV] [--openvino-pooling cls|mean|last]
                    [--fastembed-model fast-bge-small-en-v1.5] [--fastembed-cache PATH]
                    [--query "intent"] [--priors PATH] [--full-mdg] [--no-mdg]
                    [--sibling-sim | --no-sibling-sim] [--owner-cache-ttl DUR]
  search "query" [--repo owner/repo] [--top N]
        --budget N caps the expensive per-fork compare/contributors calls to N,
        cache. --top N instead deep-scans the top N by surface score.
        --shortlist N emits only the top N forks by Robbins expected rank (lower
        = more likely best), adding expectedRank + rankConfidence to each.
        Flags go AFTER 'forks list <repo>'. Clustering runs in-process:
        the default 'builtin' lexical embedder needs nothing installed;
        '--embedder-backend openvino --openvino-model DIR' runs a
        transformer encoder on an Intel GPU (requires the OpenVINO runtime
        at run time; set SPOON_OPENVINO_LIB if it is not on the default
        path). Run 'spoon setup' once to download default models
        and persist the configuration.
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
  repo centrality <owner/repo> [--forge github] [--forge-host H]

Output:
  Success: bare JSON on stdout (single value for reads; NDJSON for forks list).
  Failure: structured JSON envelope on stderr with code, message, remediation,
           retryable, optional retry_after_seconds, and details.

Exit codes: 0 success; 2 user error / policy; 1 everything else.

Authentication: gh auth login (GitHub).
`)
}
