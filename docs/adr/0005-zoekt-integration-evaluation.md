# ADR 0005: Zoekt Integration Evaluation

**Status**: Accepted
**Date**: 2026-08-25
**Deciders**: Spoon core maintainers

## Context

Spoon is a GitHub fork analyzer that scores and clusters forks by novelty using:

- **FastEmbed** (BGE-small-en-v1.5, 384-dim) for semantic similarity
- **Lexical fallback** (FNV TF-IDF) for offline/degraded operation
- **Single-link clustering** on fork-level documents (metadata + diff + commits + README)

We evaluated integrating **Zoekt** (Google/Sourcegraph, trigram-based code search engine) to replace or augment the similarity pipeline.

Key properties of Zoekt:

- File-level trigram index with BM25/symbol ranking
- Apache-2.0 license; no top-level NOTICE file at the pinned commit, so §4(d)
  NOTICE-propagation is not triggered — §4(a)/(c) license and attribution
  preservation still apply if any of it is vendored
- 60k+ LoC, 27+ transitive Go dependencies
- Index construction is concrete (`*index.Builder`, `*index.ShardBuilder`); the
  root API does export interfaces (`Searcher`, `Sender`, `Streamer` in
  `api.go:917`, `:1102`, `:1116` at the pinned commit), so the cost is in the
  indexing path, not in every seam
- Assumes stable corpus with incremental git ingestion

## Decision

**No Zoekt integration for fork similarity/clustering.**

**Pattern adaptation ONLY** if lexical pre-filter for query-time search (`spn search` subcommand) becomes a measured bottleneck.

**Explicit veto** on vendoring Zoekt for fork clustering/similarity.

## Consequences

### Positive (for No Integration)

- Avoids 60k+ LoC dependency and 27+ transitive deps
- Avoids Apache-2.0 license/attribution obligations in an MIT project
- Avoids semantic gap: trigram overlap ≠ fork-level semantic similarity
- Avoids index freshness mismatch: Zoekt stable corpus vs Spoon ad-hoc per-run
- Keeps FastEmbed + lexical fallback as primary, well-understood path
- Defers ANN index (FAISS/HNSW on libSQL vectors) until brute-force cosine exceeds latency budget
- Maintains model-swap awareness (FastEmbed model changes invalidate cache cleanly)

### Negative (risks accepted)

- If `spn search` (query-time code search inside forks) becomes primary workflow, we forgo a mature trigram search engine
- If fork clustering quality degrades measurably (>15% false pos/neg on labeled benchmark), we lack the trigram pre-filter that could help

### Mitigation

- **Fallback B**: Library dependency for `cmd/spn/search.go` only, behind feature flag, carrying the Apache-2.0 license text and attribution notices — if `spn search` becomes primary user workflow. (No NOTICE generation: Zoekt ships no NOTICE file at the pinned commit, so §4(d) does not apply. If a later version adds one, this obligation returns.)
- **Default**: Add FAISS/HNSW ANN index on libSQL vectors ONLY when measured latency budget exceeded
- **Testable hypothesis**: Trigram Jaccard on fork diffs correlates <0.3 with FastEmbed cosine (if falsified, reconsider C)

## Related

- Evaluation summary: `docs/evaluation/zoekt-evaluation.md`
- Research artifact: `local/research/phase5-artifact.md`
- Spoon pipeline: `internal/forksops/stream.go`, `internal/semantic/semantic.go`
