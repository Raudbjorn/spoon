---
name: using-spn-forks
description: Use when discovering or scoring forks of a repository, comparing fork novelty, clustering forks with the Ollama embedder, managing the embedding model (probe / pull / list), or fetching directory centrality data for a repo. Applies when the `spn` CLI is on PATH (`command -v spn`). Output is NDJSON streaming by default; pass `--csv` for batched tabular output.
---

# Using `spn` for Fork Discovery and Clustering

`spn` enumerates and scores forks of a GitHub or GitLab repository. Per-fork JSON includes a "heat" score (additive 0–100 across T1/T2/T3 signals), cluster label when clustering ran, and optional T2/T3 enrichment.

## When to Use

- Discovering interesting forks of an upstream repo
- Bulk-scoring forks for triage
- Clustering forks by novelty (requires a reachable Ollama embedder)
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

Collects all enriched forks and emits a single CSV blob on stdout with a fixed header (18 columns covering identity, T1, T2, T3, and cluster fields). Per-fork enrichment errors still go to stderr as compact JSON. Use this when downstream tooling expects tabular data; use the default NDJSON when streaming or jq pipelines fit better.

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

## Query-driven fork search (no embedder required)

`spn forks list <repo> --query "intent"` scores each fork's change digest against the query and outputs sorted by relevance. Each NDJSON record gains a `queryScore` (0..1) and a `queryMethod` field. When the OpenVINO reranker is configured, `queryMethod` is `"openvino"`; otherwise it falls back to lexical cosine scoring. No external embedder service is required for any path.

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
