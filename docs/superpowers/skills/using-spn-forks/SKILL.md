---
name: using-spn-forks
description: Use when discovering or scoring forks of a repository, comparing fork novelty, clustering forks by novelty, ranking forks against a free-text intent, or fetching directory centrality data for a repo. Applies when the `spn` CLI is on PATH (`command -v spn`). Output is NDJSON streaming by default; pass `--csv` for batched tabular output.
---

# Using `spn` for Fork Discovery and Clustering

`spn` enumerates and scores forks of a GitHub or GitLab repository. Per-fork JSON includes a "heat" score (additive 0–100 across T1/T2/T3 signals), cluster label when clustering ran, and optional T2/T3 enrichment.

## When to Use

- Discovering interesting forks of an upstream repo
- Bulk-scoring forks for triage
- Clustering forks by novelty (runs in-process; no service required)
- Per-directory centrality for a repo (which directories drive activity)

Do NOT use for: PR review work (see the `using-spn` skill for that) or non-fork repository analysis.

**First check:** `command -v spn`. If absent, fall back to hand-rolled `gh api` calls; otherwise prefer spn for streamed, scored output.

## Core Loop

```bash
spn forks list owner/repo | jq -c '. | select(.heat > 60)'
```

Streaming NDJSON: one fork per line, ordered by heat-score descending after sorting completes.

## CSV Mode

```bash
spn forks list owner/repo --csv > forks.csv
```

Collects all enriched forks and emits a single CSV blob on stdout with a fixed header (24 columns covering identity, T1, T2, T3, cluster, and shortlist rank fields — the six rank columns are empty without `--shortlist`). Per-fork enrichment errors still go to stderr as compact JSON. Use this when downstream tooling expects tabular data; use the default NDJSON when streaming or jq pipelines fit better.

## Output Contract (shared with the PR-review skill)

| Case | Where | Shape |
| --- | --- | --- |
| Success — NDJSON | stdout | one JSON object per line |
| Success — CSV | stdout | header + rows |
| Failure | stderr | `{"error": {...}}` envelope |

Stdout is exclusively success data. Per-fork enrichment errors go to stderr (NDJSON-shaped); fatal errors go to stderr (full envelope) and exit non-zero.

**Exit codes:** 0 success; 2 user/policy error (do not retry); 1 transient/upstream (check `error.retryable`).

## Rate Limits

GitHub rate-limit hits produce a `rate_limited` envelope with `retry_after_seconds`. See the `using-spn` skill's Rate Limits section for the full envelope shape and retry pattern — identical here.

Detection works on REST API paths. GitHub's GraphQL endpoint (used by the forks-list GraphQL fast path) returns rate-limit hits as the generic `upstream_error` code instead.

## Query-driven fork search

`spn forks list <repo> --query "intent"` scores each fork's change digest against the query and outputs sorted by relevance. Each NDJSON record gains a `queryScore` (0..1) and a `queryMethod` field.

`queryMethod` tells you which scorer ran, and the two are not interchangeable in quality: `"voyage"` is a cross-encoder (only when `VOYAGE_AI_API_KEY` is set), `"lexical"` is the built-in deterministic cosine fallback. The lexical path needs no key, no service and no native runtime, so this flag always works.

`spn search "<query>"` is a different operation: vector retrieval over the persistent index built by `forks list`, rather than scoring an already-enumerated set. With a Voyage key it also reranks its candidates, adding `rerankScore`/`rerankModel` while `score` stays the retrieval cosine; `--voyage` ranks against the Voyage index instead of the fastembed one. `--voyage` or `--rerank` without a key exits 2 rather than silently answering from the other index.

## Touched-path filtering (`--touching`)

`spn forks list <repo> --touching '<path|glob>'` (repeatable) prints only forks whose **own ahead commits** changed a matching path, reading the merge-base-relative compare (`base...head`) already cached per fork — a re-run against a scanned network costs no API calls. Patterns are repo-relative; `**` spans directories, `*` does not. Every printed record gains a `touching` block with `status` and `partial`; a matched record additionally carries `impact`, `centrality_method`, and `files[]` (`path`/`previousPath`/`status`/`additions`/`deletions`/`pattern`/`centrality`), and is pinned into the output (`visibility.status: "pinned"`, `profile: "touches_target"`) — `impact` and `centrality_method` are omitted entirely, not zero-valued, on unmatched/unknown records, since there is nothing to score. `touching.partial: true` marks a fork whose file list hit GitHub's 300-file compare cap; such a fork is still printed even when unmatched, so the gap in coverage is visible.

Before any compare runs, `spn` resolves the whole network's ahead/behind status in a GraphQL batch; forks with nothing ahead never pay a compare call, and every literal (non-glob) `--touching` target on a still-divergent fork is checked against GitHub's `tree-commit-info` page first — a matching last-touch commit skips that fork's REST compare too (`touching.reason: "last_touch"` on the resulting `unmatched` verdict, and `t2.files_unfetched: true`). A stderr summary tallies matched/unmatched/unknown/never_pushed, plus a `last_touch` sub-object (`gated`/`looked_up`/`skipped`/`mismatch`/`unavailable`); since zero-ahead forks are already resolved by the batch before dispatch, `unknown` now mostly means the rate reserve was hit for a fork that still needed a REST compare — re-run to resolve. `--no-batch-compare` also disables this last-touch skip entirely, since the gate is built only from the batch's output — every fork falls through to its ordinary REST compare. NDJSON only — `--csv` and `--tier 1` are both rejected since `--touching` needs compare data.

## Owner evidence (`ownerEvidence`)

When a fork's owner profile was fetched or read from the 24h cache, its NDJSON record carries an `ownerEvidence` block (`login`, `observedRepos`, `forks`, `signalForks`, `nonForkRepos`, `sampleOrder`, `complete`, `fetchedAt`): the sample behind the `fork_farmer` penalty. The sample is at most the owner's 500 most recently pushed repositories (`sampleOrder: "pushed_desc"`). `complete: false` means the account had more, so `observedRepos` is a window rather than the account total, and the penalty is not applied to a partial sample. `signalForks` counts forks pushed within a year, which shows activity, not who authored the changes. The block is omitted when no profile was obtained (live-fetch cap reached, rate reserve, non-GitHub forge), so its absence means "no signal", not "owner has no repositories".

## Prior-biased ordering (`--priors`)

`spn forks list <repo> --priors PATH` scores every fork against a JSON interest spec (`paths`, `keywords`, `languages`, `owners.allow`/`owners.deny`) from data already fetched, at zero extra API cost. Each record gains `priorScore` (0–1) and `priorReasons` (e.g. `path:internal/auth`, `keyword:oauth`, `owner_deny:fork-farmer`); when neither `--query` nor `--shortlist` is active, matched forks list before unmatched ones (heat order within each lane). Priors never hide a fork or change its heat — a denied owner scores 0 but is still emitted. Path entries follow the same matcher as `--touching`: exact/directory-prefix without a glob, single-segment `path.Match` for a glob without `**`, and `**` for a segment wildcard spanning any depth.

## Shortlist with rank uncertainty

`spn forks list <repo> --shortlist N` buffers the run, ranks the strongest 200 forks by heat under a Gaussian utility model (mu = heat score, sigma from tier confidence), and emits the top N by Robbins expected rank. Each record gains:

| Field | Meaning |
|---|---|
| `expectedRank` | Robbins expected rank over the pool; lower = more likely best |
| `rankConfidence` | tier confidence (0.3 / 0.7 / 0.9) used for sigma |
| `pScore` | `(n − expectedRank)/(n − 1)` = SUCRA; 1 = certainly best, 0.5 = coin flip |
| `pTopK` | P(rank ≤ N): probability the fork genuinely belongs in the shortlist |
| `pFirst` | P(rank = 1) |
| `rankLo` / `rankHi` | 95% central rank interval (1-based) |
| `tieBand` | true when the fork is statistically indistinguishable from an adjacent fork in the emitted order — report the run as one cluster |

Read `pTopK` before trusting position: a tier-1 (unenriched) fork has wide sigma, so it can sit at rank 3 with `pTopK` 0.4. All probabilities are relative to the 200-fork pool, not the whole network.

`--shortlist-rule membership` selects the N forks by `pTopK` instead of expected rank (then orders by expected rank). Use it when the shortlist is a decision set ("which N do I open?"); keep the default `expected` when you want the full-ordering view.

Every shortlist run writes one `{"info":{"code":"rank_report",...}}` line to stderr: `poolSize`, `nonzeroPool`, `shortlistRule`, `poth` (precision of the whole hierarchy, 0 = coin flips, 1 = certain order) and `cpothK` (the same within the shortlist; `null` when the set is smaller than 3). High `poth` with low `cpothK` means "the shortlist beats the rest, but its internal order is noise". `--rank-diagnostics` adds `pothResidual` per record; the most negative residual is the fork whose wide uncertainty most blurs the ordering — fetch it deeper first.

`--eb` (needs `--shortlist`) replaces raw heat / tier sigma with empirical-Bayes shrunken scores and posterior sigmas fitted over the forks with heat > 0. Check `ebRegime` in the `rank_report` first: `pooled` or `insufficient` means nothing was shrunk. Per record: `ebTheta`, `ebSigma`, `ebResidual`, `ebLeverage`, `ebFlag` (true = the scoring model does not explain this fork; look at it). `--prior-scale F` caps `tauHat` at 2F. Experimental — compare against the default ordering before trusting it.

## Common Mistakes

| Mistake | What to do instead |
| --- | --- |
| Treating `forks list` as a one-shot batched command by default | NDJSON streaming is the default; pipe through `jq -c` to consume incrementally. Use `--csv` only when the consumer expects tabular data. |
| Running `forks list` expecting clusters to always run | Clustering needs an embedder; if no backend is configured, the pipeline silently skips the cluster stage and emits per-fork warnings on stderr. Check the run output for `cluster_skip` warnings. |

## Quick Reference

| Verb | Use |
| --- | --- |
| `spn forks list <repo>` | NDJSON stream of enriched forks |
| `spn forks list <repo> --csv` | Batched CSV with fixed 18-column header |
| `spn forks list <repo> --touching PATH` | Only forks whose own ahead commits changed a matching path |
| `spn forks list <repo> --no-batch-compare` | Restore one REST compare per fork instead of the pre-dispatch GraphQL batch |
| `spn forks list <repo> --no-tree-commit-info` | Disable the `tree-commit-info` last-touch skip (default: on only with `--touching` and literal paths) |
| `spn repo centrality <owner/repo>` | Per-directory centrality JSON for the upstream repo |
