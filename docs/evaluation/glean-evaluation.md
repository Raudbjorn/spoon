# Glean Evaluation for Spoon Integration

**Status**: Complete — **No Integration for Fork Similarity/Clustering**

**Research Artifact**: `GleanIntegrationResearch` agent yield, 2026-08-25

## Executive Summary

Facebook's Glean was evaluated for possible integration into Spoon, a Go-based GitHub fork analyzer that uses FastEmbed plus a lexical fallback for fork novelty scoring and single-link clustering. The evaluation reached a **primary recommendation of No Integration (D)**. Glean's schema and codemarkup unification ideas are useful as design references only (C), while the supplied Glean fork export contains no actionable indexer, schema, or query changes worth porting (B).

Glean is a file- and symbol-level code-intelligence platform. It collects language-specific facts, stores them in versioned schemas, and exposes cross-references through Angle queries and Thrift services. Spoon evaluates whole repositories: it builds one document per fork from metadata, diff, commit messages, and README content, then compares fork-level vectors and heat signals. The units of analysis, deployment model, and correctness goals do not align.

## Decision Rationale

### Category Error: File-Level Facts vs Fork-Level Similarity

Glean's atomic data is a structured fact such as an entity, predicate, definition, call, type, or reference. Spoon's atomic data is a fork-level document and its embedding. Glean answers questions such as:

- Which function calls this symbol?
- Where is this type defined?
- Which files reference this entity?

Spoon answers questions such as:

- Which forks contain meaningful feature work?
- Which forks are semantically similar?
- Which fork deserves human review first?

There is no demonstrated projection from Glean facts to Spoon's fork-level similarity signal. A proposed projection would have to aggregate per-file facts across a fork, encode them into vectors, and prove that the resulting representation improves ranking over Spoon's existing diff-aware document and embedding pipeline. Without that bridge, “use Glean for fork similarity” is a category error rather than an integration plan.

### Semantic and Aggregation Gap

Glean provides precise structural facts and cross-references. It does not directly express fork intent, divergence quality, contributor behavior, feature-vs-configuration work, or novelty. A one-line bug fix and a large refactor may both produce changed facts; fact counts are not a substitute for semantic divergence.

Spoon already captures the relevant fork-level signals through:

- FastEmbed vectors for the single document built per fork
- A deterministic lexical embedder when the native runtime is unavailable
- Heat components for activity, divergence, contributor mix, change shape, and novelty
- Single-link clustering over fork-level cosine similarity
- A libSQL/SQLite-backed cache keyed by repository, embedder, and configuration

Replacing that pipeline with an unproven fact-to-vector projection would add complexity while discarding the signal Spoon already measures.

### Architecture and Deployment Mismatch

Glean is a large Haskell/C++ system with Thrift interfaces, RocksDB/LMDB-backed persistence, Angle query evaluation, and language-specific indexers. Its client API is a service boundary, not a stable Go library API. Spoon is a single-process Go CLI/TUI with per-run acquisition, local persistence, and optional ONNX/FastEmbed execution; it has no required daemon or Thrift runtime.

Embedding Glean would require one of three unattractive paths:

1. Vendor and build a substantial Haskell/C++/Thrift/RocksDB stack.
2. Run a Glean server as a sidecar and maintain a Go-to-Thrift client.
3. Reimplement only a small slice of Glean's fact and query model, losing the indexer ecosystem that makes Glean useful.

None of these paths closes a measured Spoon capability gap.

### License and Maintenance Friction

Glean uses a BSD-style license with notice and attribution obligations. Spoon's existing dependency set is Go-native and avoids a server-side Haskell/C++ toolchain. The legal burden is manageable in isolation, but the operational burden is not: schema versions, indexer compatibility, RocksDB/LMDB behavior, Thrift APIs, and upstream release maintenance would become part of Spoon's support surface.

### Fork Export Findings

The supplied export contains 93 Glean forks, 31 with commits ahead of upstream. The most divergent examples are configuration or deployment changes, including Nix flakes and minor fixes. The top fork has 16 commits ahead across four files; other high-ranked forks similarly lack changes to Glean's indexer, schema, or query engine. Novelty scores are low and the clusters are noise.

The export therefore validates that this fork network is not a source of portable Glean improvements for Spoon. It does not justify porting fork code or treating the export as evidence that Glean improves Spoon's ranking.

## Recommendations

| Label | Recommendation | When |
| ------- | -------------- | ------ |
| **D (Primary)** | **Do not integrate Glean into fork similarity or clustering** | Current and default path |
| **C (Conditional)** | Use Glean's codemarkup unification and schema-evolution ideas as design references only | When documenting future structural-fact work |
| **B (Export disposition)** | Treat the supplied Glean fork export as low-novelty validation; port no fork changes | Unless a future fork changes indexers, schemas, or query execution materially |
| **A (Veto)** | Do not vendor the Glean runtime, server, or full fact pipeline | Unless a new product requirement makes code intelligence itself the primary product |

### Falsifiers

The no-integration decision should be revisited only if evidence closes the actual gap:

- A published held-out fork-similarity benchmark shows Glean-derived fact representations outperform Spoon's FastEmbed baseline on ranking quality.
- A measured Spoon workload requires per-file or per-symbol similarity rather than fork-level similarity.
- A maintained Go binding or stable service contract makes Glean integration materially cheaper than the current architecture implies.
- A future Glean fork contains substantial, verified indexer, schema, or query improvements rather than configuration changes.

## Implementation Strategy (Tiered)

### Default / Workhorse (Current Path)

Keep Spoon's existing path:

1. Acquire forks through the existing GitHub/GitLab adapters.
2. Build one fork-level semantic document from metadata, diff, commits, and README content.
3. Embed with FastEmbed or the deterministic lexical fallback.
4. Score heat and cluster by fork-level cosine similarity.
5. Persist snapshots, vectors, and cache metadata in libSQL/SQLite.

Improve model selection, prompt/document construction, or labeled evaluation before adding a code-intelligence runtime.

### Conditional Design Reference

Glean's useful portable ideas are conceptual rather than executable code:

- Versioned schemas and explicit evolution rules for durable Spoon records.
- A language-agnostic structural vocabulary if Spoon later adds per-file facts.
- Predicate-like query boundaries for future store APIs, without adopting Angle or Thrift.

These ideas do not require a Glean dependency or runtime.

### Expensive / Escalation

If Spoon later needs cross-repository symbol search or per-file similarity, evaluate SCIP/LSIF ingestion directly. That path targets the required file-level index format without importing Glean's server, Haskell runtime, or full schema system. It must be justified by a measured workload and a separate design decision.

## Testable Hypotheses

1. **Glean fact counts do not improve fork ranking**: On a labeled fork corpus, fact-count and cross-reference-density features will not produce a meaningful AUC/nDCG improvement over Spoon's existing heat and FastEmbed features without a new learned projection.
2. **Glean incremental indexing is not a per-run win**: Repeated `spn forks list` runs over the current workload will remain simpler and faster with Spoon's existing content-hash cache than with a cold or sidecar Glean server.
3. **Codemarkup adaptation is feature engineering, not integration**: Adding structural predicates such as `HasTests`, `HasCI`, or `ChangesPublicAPI` directly to Spoon's heat scorer can be evaluated independently and does not require Glean.
4. **Angle is not a drop-in query backend**: Porting a Spoon query to Angle would add service/schema coupling without improving fork-level semantic search unless Spoon first adopts a persistent fact corpus.

## Failure Modes

| Failure mode | Detection | Mitigation |
| ------------ | --------- | ---------- |
| Schema drift breaks a future client | Thrift/Angle schema and integration tests fail after upgrade | Pin versions and keep any future client behind a narrow adapter |
| Glean server consumes excessive resources | Startup, index-build, RSS, and disk telemetry | Do not make it a runtime dependency; require an explicit opt-in experiment |
| Fact counts masquerade as semantic divergence | Labeled ranking evaluation shows no AUC/nDCG gain | Keep fact features separate and reject unproven projection claims |
| Notice or attribution obligations are missed | Release artifact/license audit | Avoid the dependency; if adopted, automate NOTICE generation |
| Fork export noise is mistaken for novelty | Nonzero commits without indexer/schema/query changes | Inspect changed files and retain the no-port disposition |

## Adjacent Leads

- **SCIP/LSIF ingestion**: Revisit when Spoon needs per-file or per-symbol fork similarity.
- **Better code embeddings**: Compare BGE-small, Jina, and Voyage models on a labeled fork corpus; this is closer to Spoon's existing architecture than Glean.
- **Labeled fork dataset**: Build an independent “interesting fork” benchmark before changing heat or clustering.
- **Glean as a development-time tool**: Optionally index Spoon's own codebase for maintainer navigation; keep this separate from Spoon's runtime and fork-ranking product.

## Meta-Observation

The initial framing treated Glean's technical sophistication as evidence that it should be useful to Spoon. The adversarial analysis exposed the opposite: Glean solves precise code intelligence over facts and cross-references, while Spoon solves fork-level relevance under a semantic-similarity and ranking contract. The integration claim fails unless a concrete Spoon seam and a published improvement are identified first.

The design guard is simple: every integration claim must trace to a Spoon pipeline seam. If the claim requires projecting Glean facts into fork vectors without a published mechanism and benchmark, it is conjecture. Precision is not a substitute for relevance.

**Next cycle**: If per-file fork similarity becomes a real requirement, evaluate SCIP/LSIF ingestion as a separate architectural decision. If fork ranking remains the product, improve embeddings and labeled evaluation instead of adding Glean.
