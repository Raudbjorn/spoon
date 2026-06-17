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
                    [--no-cluster] [--cluster-top N] [--cluster-epsilon F] [--cluster-min-size N]
                    [--embedder-backend builtin|openvino] [--openvino-model PATH]
                    [--openvino-device DEV] [--openvino-pooling cls|mean|last]
                    [--query "intent"] [--full-mdg] [--no-mdg]
                    [--sibling-sim | --no-sibling-sim] [--owner-cache-ttl DUR]
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
        topic:NAME evaluates the fork networks of the best repositories
        representing a GitHub topic (selection by stars + fork-network size +
        recency; cap with --topic-repos N, default 5). Each record gains an
        "upstream" field; selections are reported as info envelopes on stderr.
        --query "intent" scores every enriched fork against a free-text
        intent (cross-encoder reranker when configured, lexical fallback
        otherwise), sorts by relevance, and adds queryScore/queryMethod to
        each record. With the openvino backend each fork also gets a
        zero-shot 'category' facet, and a configured labeler polishes
        cluster labels with an in-process LLM.
        --sibling-sim / --no-sibling-sim toggles P2 distant-relation
        discovery: one /search/repositories + ~50 README fetches + one
        batched embed, with the max cosine to the upstream README
        folded into Heat.SiblingSim (capped by the combined-novelty
        cap of 7.5). On by default for standard runs; opt-in via
        --sibling-sim for topic mode (5x cost multiplier per upstream).
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
