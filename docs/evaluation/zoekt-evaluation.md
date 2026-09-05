# Zoekt Evaluation for Spoon Integration

**Status**: Complete — **No Integration for Fork Similarity/Clustering**

**Research Artifact**: `local://research/phase5-artifact.md`

## Executive Summary

Google/Sourcegraph Zoekt was evaluated for potential integration into Spoon (a Go+TS GitHub fork analyzer using FastEmbed + lexical fallback for fork novelty scoring and clustering). The evaluation concluded with a **primary recommendation of No Integration (D)** for fork similarity/clustering, with a fallback for query-time search subcommand only (B), and an explicit veto on vendoring Zoekt for fork clustering (A).

## Decision Rationale

### Category Error: File-Level vs Fork-Level

Zoekt is a **file-level code search engine** — it indexes and queries individual files, functions, and symbols using trigram-based indexing with BM25/symbol ranking. Spoon is a **fork-level semantic similarity analyzer** — it builds a single semantic document per fork (metadata + diff + commits + README) and clusters forks using single-link clustering on embedding cosine similarity.

The mismatch is fundamental:

- Zoekt's unit of analysis: **file/function/symbol**
- Spoon's unit of analysis: **entire fork repository**

No natural aggregation layer exists to map file-level trigram signals to fork-level semantic similarity without re-implementing Spoon's existing multimodal embedding pipeline.

### Semantic Gap

Zoekt's ranking relies on trigram overlap + symbol signals. It cannot capture:

- Renamed variables / refactored logic
- Different implementations of the same algorithm
- Semantic equivalence across syntactic divergence

Spoon's FastEmbed (BGE-small-en-v1.5, 384-dim) captures these by design. No independent benchmark shows trigram Jaccard correlates with embedding cosine on fork-level similarity.

### License & Maintenance Friction

- Zoekt: **Apache-2.0 with NOTICE redistribution clause** (no top-level NOTICE file)
- Spoon: **MIT**

Vendoring Zoekt (60k+ LoC, 27+ transitive dependencies) into an MIT project forces NOTICE propagation and patent-grant tracking. Zoekt exports concrete types (`*index.Builder`, `*search.Searcher`), not interfaces — making mocking and testing harder.

### Index Freshness Mismatch

Zoekt assumes a relatively stable corpus with incremental git ingestion. Spoon evaluates arbitrary fork sets per-run with content-hash + model-keyed cache invalidation. The ingestion models are incompatible.

## Recommendations

| Label | Recommendation | When |
| ------- | ---------------- | ------ |
| **D (Primary)** | **No Zoekt integration for fork similarity/clustering** | Always |
| **C (Conditional)** | Pattern adaptation ONLY (trigram extraction for lexical pre-filter) | If query-time search becomes a measured bottleneck |
| **B (Fallback)** | Library dependency for `cmd/spn/search.go` only, behind feature flag, with NOTICE generation | If `spn search` becomes primary user workflow |
| **A (Veto)** | **Never vendor Zoekt for fork clustering/similarity** | Never |

### Falsifiers

**For D (would invalidate no-integration):**

- Spoon's fork clustering produces >15% false positives/negatives on a labeled fork-eval benchmark AND trigram pre-filter reduces error by >5pp
- Independent benchmark shows trigram Jaccard on fork diffs correlates ≥0.3 (Spearman) with FastEmbed cosine on same pairs

**For C (would invalidate pattern adaptation):**

- Extracted trigram code exceeds 8k LoC or pulls >5 Zoekt deps transitively

**For B (would invalidate fallback):**

- `go get github.com/sourcegraph/zoekt` pulls incompatible Go version or breaks build
- NOTICE generation at build fails
- `spn search` latency without Zoekt <100ms p99

**For A (would make vendoring viable — unlikely):**

- Zoekt adds first-class fork-level semantic similarity API

## Implementation Strategy (Tiered)

### Default / Workhorse (Current Path)

Keep **FastEmbed + lexical fallback** as primary. Add **FAISS/HNSW ANN index on libSQL vectors** ONLY when brute-force cosine similarity on fork vectors exceeds measured latency budget.

- Current pipeline: `semantic.BuildDocument` → batch upsert → `SearchRows` → cosine in Go
- ANN library: native Go HNSW (separate dep needed)

### Expensive / Escalation (Different Product)

If Spoon pivots to "code search inside forks" as primary product:

- Deploy Zoekt indexserver as sidecar service (Docker, `CGO_ENABLED=0`)
- Ingest fork repos via Zoekt's `gitindex`
- Expose gRPC/JSON API to Spoon CLI

**Trigger**: User research shows >50% sessions use search subcommand AND fork eval is secondary.

## Testable Hypotheses

1. **H**: Trigram Jaccard similarity between fork diff sets correlates <0.3 with FastEmbed cosine similarity on same fork pairs
2. **H**: Adding trigram pre-filter to FastEmbed search reduces recall@k by <2% while improving latency >2x (predicted: false — pre-filter loses semantic matches)
3. **H**: Spoon's current brute-force cosine on 10k fork vectors completes in <500ms on typical hardware (if true, ANN not needed)

## Adjacent Leads

- **LSIF/SCIP Code Intelligence**: Sourcegraph's SCIP + embeddings pipeline is closer to Spoon's approach (semantic indexing via embeddings)
- **RAG over fork corpus**: If query-time search becomes primary, consider embedding-based RAG over fork documents rather than trigram search

## Meta-Observation

The research framing assumed Zoekt could plug into Spoon's fork analysis pipeline. The data reveals a category error: Zoekt is a file-level code search engine; Spoon is a fork-level semantic similarity analyzer. The overlap is near-zero. The only plausible integration is pattern-adapting Zoekt's trigram extraction as a lexical pre-filter for query-time search — but Spoon's search subcommand is not the primary product.

The adversarial self-attack correctly identified the semantic gap, license friction, and maintenance burden as veto signals. Spoon's pipeline (multimodal embedding, content-hash cache, model-swap awareness, libSQL persistence) is more sophisticated than initially assumed and doesn't need Zoekt's machinery.

**Next cycle**: If user confirms `spn search` is priority, prototype Fallback B (Zoekt library for search only) with NOTICE generation and feature flag.
