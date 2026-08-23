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
| `spn repo centrality <owner/repo>` | Per-directory centrality JSON for the upstream repo |
