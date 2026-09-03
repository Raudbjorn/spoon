// cmd/spn/main.go
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/config"
)

var version = "0.1.0-dev"

// commandDeps carries startup-resolved command dependencies by value. Production
// always uses defaults; tests provide a complete immutable replacement.
type commandDeps struct {
	now            func() time.Time
	searchEmbedder searchEmbedderFactory
}

func defaultCommandDeps() commandDeps {
	return commandDeps{now: time.Now, searchEmbedder: searchEmbedderFor}
}

func (d commandDeps) withDefaults() commandDeps {
	defaults := defaultCommandDeps()
	if d.now == nil {
		d.now = defaults.now
	}
	if d.searchEmbedder == nil {
		d.searchEmbedder = defaults.searchEmbedder
	}
	return d
}

// usageRemediation lists the valid nouns for the two top-level failure paths.
const usageRemediation = "Run 'spn --help' for usage. Nouns: threads, pr, forks, search, repo."

func main() { os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr)) }

// dispatch routes a top-level invocation and returns the process exit code. Both
// failure paths (no subcommand, unknown subcommand) emit the structured agentio
// envelope on stderr and leave stdout clean, so a consuming agent branches on
// code:"bad_input" instead of choking on a plain-text line plus a stdout usage
// dump.
func dispatch(args []string, stdout, stderr io.Writer) int {
	return dispatchWithDeps(args, stdout, stderr, defaultCommandDeps())
}

func dispatchWithDeps(args []string, stdout, stderr io.Writer, deps commandDeps) int {
	deps = deps.withDefaults()
	args, noColor, err := parseGlobalOptions(args)
	if err != nil {
		return agentio.NewError(agentio.CodeBadInput, err.Error(), usageRemediation).Emit(stderr)
	}
	presentation, err := resolveStartupPresentation(noColor, stdout)
	if err != nil {
		return agentio.NewError(agentio.CodeBadInput, err.Error(), usageRemediation).Emit(stderr)
	}
	if len(args) < 1 {
		return agentio.NewError(agentio.CodeBadInput, "missing subcommand", usageRemediation).Emit(stderr)
	}
	switch args[0] {
	case "-h", "--help":
		_ = writeHuman(stdout, presentation, roleTextStrong, helpText())
		return 0
	case "-v", "--version":
		_ = writeHuman(stdout, presentation, roleAccent, fmt.Sprintf("spn %s\n", version))
		return 0
	case "threads", "pr", "forks", "search", "repo":
		return dispatchConfigured(args, stdout, stderr, presentation, deps)
	default:
		return agentio.NewError(agentio.CodeBadInput, fmt.Sprintf("unknown subcommand %q", args[0]), usageRemediation).Emit(stderr)
	}
}

// parseGlobalOptions consumes only leading global options. Once a noun or
// `--` begins command arguments, --no-color is preserved verbatim for that
// command; duplicate leading globals are rejected rather than guessed at.
func parseGlobalOptions(args []string) ([]string, bool, error) {
	noColor := false
	for len(args) > 0 && args[0] == "--no-color" {
		if noColor {
			return nil, false, fmt.Errorf("duplicate global option --no-color")
		}
		noColor = true
		args = args[1:]
	}
	return args, noColor, nil
}

// dispatchConfigured resolves the one runtime configuration only after routing
// confirms that the requested noun consumes configuration.
func dispatchConfigured(args []string, stdout, stderr io.Writer, presentation presentation, deps commandDeps) int {
	var bootstrapStderr bytes.Buffer
	boot := config.Bootstrap(&bootstrapStderr)
	if bootstrapStderr.Len() > 0 {
		_ = writeError(stderr, presentation, roleWarning, bootstrapStderr.String())
	}
	if boot.Warning != nil {
		_ = writeError(stderr, presentation, roleWarning, fmt.Sprintf("warning: ignoring config: %v\n", boot.Warning))
	}
	env := config.EnvironmentSnapshot()
	effective := config.ResolveEffectiveConfig(boot.Config, nil, env)
	switch args[0] {
	case "threads":
		return runThreadsWithEffective(args[1:], stdout, stderr, effective)
	case "pr":
		return runPRWithEffective(args[1:], stdout, stderr, effective)
	case "forks":
		if len(args) >= 2 && args[1] == "eval" {
			return runEvalWithEffective(args[2:], stdout, stderr, effective, env)
		}
		return runForksWithEffectiveDeps(args[1:], stdout, stderr, effective, env, deps)
	case "search":
		return runSearchWithEffectiveDeps(args[1:], stdout, stderr, effective, env, deps)
	case "repo":
		return runRepoWithEffective(args[1:], stdout, stderr, effective)
	}
	panic("configured dispatch called with unrecognized noun")
}

func helpText() string {
	return `spn — agent-shaped CLI for spoon

Usage:
  spn [--no-color] <noun> <verb> [args]

Global options:
  --no-color  Disable human/error presentation color. It must precede the noun;
              after the noun (or --) it is passed to that command unchanged.
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
  forks eval <repo> --judgments FILE [--from-export FILE --rank-variant heat|erank|pscore|membership|eb
                    [--shortlist N] [--prior-scale F]] [--no-cluster] [--cluster-top N]
        Scores the fork pipeline against hand-labelled judgments. With
        --from-export the run is offline: forks are read from a spoon export
        JSON, ordered by the chosen rank variant over the same top-200 pool
        every variant sees, and the report adds rankVariant, rankReport and
        the ordered list with each fork's key. This is the gate for changing
        --shortlist-rule / --eb defaults.
  forks list <repo|topic:NAME> [--tier 1|2|3] [--top N] [--budget N] [--shortlist N] [--shortlist-rule expected|membership] [--rank-diagnostics] [--eb [--prior-scale F]] [--bot-allowlist L] [--refresh|--no-cache] [--csv] [--forge github|gitlab] [--forge-host H]
                    [--rpm N] [--files] [--commits] [--commit-files]
                    [--commit-file-budget N] [--web-diff] [--local-branch-scan]
                    [--no-batch-compare] [--no-tree-commit-info]
                    [--no-cluster] [--cluster-top N] [--cluster-epsilon F] [--cluster-min-size N]
                    [--no-embed] [--heat-weights W] [--strict-mdg] [--full-mdg] [--no-mdg]
                    [--query "intent"] [--priors PATH] [--touching PATH]
                    [--sibling-sim | --no-sibling-sim] [--sibling-sim-mode MODE] [--owner-cache-ttl DUR]
                    [--topic-repos N] [--topic-lanes LIST] [--topic-lane-budget N]
        --budget N caps the expensive per-fork compare/contributors calls to N.
        --top N instead deep-scans the top N by surface score.
        --shortlist N emits only the top N forks by Robbins expected rank (lower
        = more likely best), adding expectedRank + rankConfidence plus the rank
        summary pScore (= SUCRA, 1 best), pTopK (P(rank <= N)), pFirst
        (P(rank = 1)) and rankLo/rankHi (95% rank interval) to each.
        --shortlist-rule membership picks the N forks with the highest pTopK
        (P(rank <= N)) instead of the lowest expected rank, then orders them
        by expected rank; it only differs near the cut. Default: expected.
        Every --shortlist run also writes a rank_report info envelope to stderr
        (poolSize, nonzeroPool, poth = precision of the whole hierarchy in
        [0,1], cpothK = the same within the shortlist). --rank-diagnostics adds
        pothResidual per fork (negative = this fork blurs the hierarchy).
        --eb ranks on empirical-Bayes shrunken scores instead of raw heat:
        between-fork spread tau_hat is estimated (DerSimonian-Laird) over the
        ranked forks with heat > 0, each score is pulled toward the pooled
        mean in proportion to its tier noise, and the posterior sigma replaces
        the tier sigma. Records gain ebTheta, ebSigma, ebResidual, ebLeverage,
        ebFlag; rank_report gains ebRegime, tauHat, ebMean, dBarOverK, pD.
        Skipped (ranking unchanged, regime reported) when fewer than 3 forks
        have heat > 0 or tau_hat = 0. --prior-scale F caps tau_hat at 2F
        (half-normal prior); default F = 1.4826 x MAD of the scores.
        Flags go AFTER 'forks list <repo>'. The fastembed embedder powers
        persistence + semantic indexing (and 'spn search') by default; it
        needs onnxruntime (set ONNX_PATH to libonnxruntime.so, run 'spoon
        setup'). If it is unavailable the run degrades with a warning rather
        than failing. Pass --no-embed (or SPOON_NO_EMBED=1) to skip embedding.
        Clustering runs in-process and needs nothing installed.
        FastEmbed requires ONNX_PATH and is the only local backend for durable
        semantic indexing and 'spn search'. --commit-files implies --files and
        --commits and defaults to a 100-commit run budget. --web-diff is an
        unstable SPOON_GH_COOKIE-gated HTML fallback, not a supported API.
        Divergence is resolved for the whole network in a few GraphQL
        queries first; REST compares are then made only for forks with
        ahead work. --no-batch-compare restores one REST compare per fork.
        --local-branch-scan falls back to git ls-remote/fetch/merge-base
        when the default branch shows no work; the GraphQL batch
        supersedes it whenever the provider supports batching. With
        --touching and literal paths, GitHub's undocumented
        tree-commit-info page is consulted anonymously to skip compares
        for forks whose last commit touching the target equals upstream's;
        best-effort, off the API budgets. --no-tree-commit-info disables it.
        topic:NAME evaluates the fork networks of the best repositories
        representing a GitHub topic (selection by stars + fork-network size +
        recency; cap with --topic-repos N, default 5). Each record gains an
        "upstream" field; selections are reported as info envelopes on stderr.
        --topic-lanes LIST opts into comma-separated topic candidate lanes
        (default,stars,updated,forks); --topic-lane-budget N caps each lane.
        --query "intent" scores every enriched fork against a free-text
        intent (the Voyage cross-encoder when VOYAGE_AI_API_KEY is set,
        lexical fallback otherwise), sorts by relevance, and adds
        queryScore/queryMethod to each record. With fastembed active each
        fork also gets a zero-shot 'category' facet. Cluster labels are
        always heuristic and deterministic.
        VOYAGE_AI_API_KEY additionally indexes each document with
        voyage-code-3 alongside fastembed (both are kept, neither replaces
        the other) for 'spn search --voyage'. Voyage is billed per token, so
        responses are cached in the store and re-runs re-request nothing
        unchanged; it is skipped entirely when the store is not writable, or
        with --no-voyage / SPOON_NO_VOYAGE=1.
        --priors PATH scores each fork against a JSON interest spec
        (paths/keywords/languages/owners allow+deny); adds
        priorScore/priorReasons and, when neither --query nor --shortlist
        is set, lists matched forks before unmatched (heat order within
        each lane); never hides forks or changes heat.
        --touching PATH|GLOB only lists forks whose own ahead commits touched
        the path (repeatable; ** matches directories). Uses the
        merge-base-relative compare already cached per fork, so re-runs cost
        no API calls. Adds a "touching" block to each record; unmatched
        forks are omitted. NDJSON only (rejects --csv), and needs compare
        data (rejects --tier 1).
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
  search "query" [--repo owner/repo] [--top N] [--voyage]
                 [--no-rerank] [--rerank-overfetch N]
        Ranks indexed forks against the query using the fastembed semantic
        index built by 'forks list'. NDJSON on stdout is the default; there is
        no --json flag.
        --voyage ranks against the voyage-code-3 index instead (requires
        VOYAGE_AI_API_KEY; needs no ONNX Runtime). With a Voyage key, results
        are additionally reranked by a cross-encoder over the candidate
        documents, adding rerankScore/rerankModel while 'score' stays the
        retrieval cosine. Reranking works against either index, since it scores
        (query, document) pairs rather than vectors. --no-rerank skips it;
        --rerank-overfetch N widens the candidate pool (N x --top, default 5).
        A reranker outage degrades to cosine order with a warning; --voyage or
        --rerank without a key is an error, not a silent fallback.
  repo centrality <owner/repo> [--forge github] [--forge-host H]

Output:
  Success: bare JSON on stdout (single value for reads; NDJSON for forks list).
  Failure: structured JSON envelope on stderr with code, message, remediation,
           retryable, optional retry_after_seconds, and details.

Exit codes: 0 success; 2 user error / policy; 1 everything else.

Authentication: gh auth login (GitHub).
`
}
