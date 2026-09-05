# ADR 0006: Glean Integration Evaluation

**Status**: Accepted
**Date**: 2026-08-25
**Deciders**: Spoon core maintainers

## Context

Spoon is a GitHub fork analyzer that scores and clusters forks by novelty using:

- **FastEmbed** for semantic similarity, with a deterministic lexical fallback
- **Fork-level documents** containing metadata, diff, commit messages, and README content
- **Heat scoring** for activity, divergence, contributor, and change-shape signals
- **Single-link clustering** over fork-level similarity
- **libSQL/SQLite persistence** with content-hash and embedder-aware caching

We evaluated Facebook's **Glean**, a code-intelligence platform that collects language-specific facts, derives cross-references, stores versioned schemas, and exposes queries through Angle and Thrift services.

Glean's unit of analysis is a file, symbol, entity, predicate, or reference. Spoon's unit of analysis is an entire fork repository. Glean's deployment model is a Haskell/C++ service with RocksDB/LMDB, Thrift, and language-specific indexers; Spoon is a single-process Go CLI/TUI with an optional ONNX runtime and per-run fork acquisition.

The supplied Glean fork export contained 93 forks, 31 with commits ahead of upstream. Its highest-divergence forks were primarily configuration, deployment, or minor-fix work. No indexer, schema, or query-engine improvement was identified as actionable for Spoon.

## Decision

**Do not integrate Glean into Spoon's fork similarity or clustering pipeline.**

Use Glean's codemarkup unification and schema-evolution concepts as design references only. Do not vendor the Glean runtime, embed its Haskell/C++ implementation, add a Thrift sidecar, or treat the supplied fork export as a source of portable changes.

If Spoon later needs file-level or symbol-level similarity across forks, evaluate SCIP/LSIF ingestion directly in a separate ADR. That requirement would change the product boundary; it is not evidence that Glean should replace the current fork-level embedding pipeline.

## Consequences

### Positive

- Keeps FastEmbed plus the lexical fallback as the primary similarity path.
- Avoids importing a 60k+ LoC Haskell/C++/Thrift/RocksDB system into a Go CLI.
- Avoids a mandatory server, indexer, and schema-versioning lifecycle.
- Avoids an unproven projection from precise code facts to fork-level vectors.
- Preserves Spoon's per-run caching, model selection, and libSQL persistence model.
- Keeps future structural features independently testable as Go-side feature engineering.
- Treats the current Glean fork export accurately: low-novelty validation, not a porting source.

### Negative / risks accepted

- Spoon does not gain Glean's precise definitions, references, or language-agnostic symbol navigation.
- A future per-file similarity feature may require a new indexing subsystem.
- Glean-inspired structural predicates will not be available without implementing them in Spoon or adopting a different index format.

### Mitigation

- Keep model selection and document construction improvements on the existing Embedder and semantic seams.
- Build a labeled fork-ranking dataset before changing heat or clustering.
- Revisit SCIP/LSIF when per-file or per-symbol similarity is a demonstrated user need.
- If a Glean-derived development-time tool is useful for Spoon maintainers, keep it outside Spoon's runtime and distribution.

## Falsifiers

Reconsider this decision if any of the following becomes true:

- A published held-out benchmark shows Glean-derived fact representations outperform Spoon's FastEmbed baseline for fork ranking.
- A measured workload requires per-file or per-symbol similarity rather than fork-level similarity.
- A stable Go binding or service contract makes integration substantially cheaper than the current architecture implies.
- A future Glean fork contains verified indexer, schema, or query improvements with direct Spoon relevance.

## Related

- Evaluation summary: `docs/evaluation/glean-evaluation.md`
- Zoekt evaluation: `docs/evaluation/zoekt-evaluation.md`
- Spoon embedding and semantic pipeline: `internal/embed/`, `internal/semantic/semantic.go`
- Spoon fork pipeline: `internal/forksops/stream.go`, `internal/cluster/pipeline.go`
- Future per-file index option: SCIP/LSIF ingestion, deferred until the product requirement exists
