# Zoekt → Spoon Integration Research Artifact

**Question**: Whether and how to integrate Google/Sourcegraph Zoekt (a trigram-based code search engine) into Spoon (a Go+TS GitHub fork analyzer with FastEmbed/lexical fallback for fork novelty scoring and clustering) — specifically, whether Zoekt's code-search architecture can replace, augment, or pre-filter Spoon's current embedding-based similarity pipeline for fork-level comparison.

**Framing Pushback**: The framing "integrate Zoekt into Spoon" conflates three distinct problems that Zoekt does not natively solve: (1) **Fork-level similarity**, not file-level code search — Zoekt indexes and queries individual files/functions/symbols; Spoon needs to compare entire forks (metadata + diff + commits + README) as single semantic units. (2) **Semantic similarity**, not lexical trigram overlap — Zoekt's ranking is BM25 + symbol/code signals over trigrams; Spoon's FastEmbed (BGE-small-en-v1.5, 384-dim) captures semantic code similarity that trigram overlap cannot (e.g., renamed variables, refactored logic, different implementations of same algorithm). (3) **Index freshness for ad-hoc fork sets** — Zoekt assumes a relatively stable corpus with incremental git ingestion; Spoon evaluates arbitrary fork sets per-run with content-hash+model-keyed cache invalidation. The framing also ignores license friction: Zoekt is Apache-2.0 with NOTICE redistribution obligation; Spoon is MIT. Vendoring Zoekt (27+ transitive deps, 60k+ LoC) into an MIT project forces NOTICE propagation and patent-grant tracking. No evidence in either codebase suggests a clean library boundary — Zoekt exports concrete `*index.Builder` and `*search.Searcher`, not interfaces, making mocking/testing harder.

**Versions Pinned**:

- zoekt: commit 6e01b543d1834fbe9f0659a1fd844150e2a3991a (main, grafted single commit, no tags/history) — go 1.25.9, toolchain go1.26.5
- spoon: commit 1a74fa2e490487f0efc9b8e06b0bd10c66fcbf60 (hound branch) — FastEmbed-Go v1.0.0 (anush008/fastembed-go), model fast-bge-small-en-v1.5 (384-dim, maxlen 512), libSQL persistence
- Go: 1.22+ (both), CGO_ENABLED=0 for zoekt Docker build
- No submodules/vendored deps in zoekt (go.mod only); spoon vendors fastembed-go in vendor/

**Scope**:

- **In**: Zoekt architecture, indexing/query/storage; Spoon fork pipeline (export→embed→cluster→score→report); Concrete overlap mapping; Decision matrix (vendoring/lib/pattern-adaptation/none × fork discovery/clustering/similarity/query/index-freshness); Adversarial self-attack; Committed recommendations with falsifiers
- **Out**: Implementation code, PR authoring, benchmarking, deployment, TUI changes, unrelated Spoon features (search subcommand, repo command)

**Context**:

- Zoekt (sourcegraph/zoekt@6e01b543): Root API (api.go:917-1119) exports Searcher interface (Search, List, Close, String), Sender/Streamer. Query AST (query.go:36-496) supports substring/regexp/symbol/filename/repo/branch/lang + And/Or/Not/Boost. Indexing via concrete `*index.Builder` (builder.go:562-807) → `*index.ShardBuilder` (shard_builder.go:542-643) with trigram postings (newSearchableString shard_builder.go:89-271, ASCII direct 21-bit array bits.go:76-87, non-ASCII map). Storage: tagged TOC over mmap sections (write.go:34-246, read.go:103-428), delta-varint postings (hititer.go:180-275), shard <4GiB/1GiB content (design.md:57-68). Query execution: frequency-based ngram selection (indexdata.go:349-501), match-tree planner (matchtree.go:1001-1450), per-shard eval (eval.go:138-377), cross-shard aggregation (aggregate.go:27-181). Ranking: standard (word/partial/filename/symbol/ctags-kind/boost, score.go:98-198) + optional BM25 (score.go:206-242). Sharding: directory searcher (shards.go:234-370), parallel shard search (shards.go:804-1091), compound Merge/Explode (merge.go:20-277). License: Apache-2.0 (LICENSE:1-9), NOTICE redistribution clause (LICENSE:79-126), no top-level NOTICE file. No release tags in local checkout — cadence/breaking-history unverifiable locally.
- Spoon (svnbjrn/spoon@1a74fa2e): CLI entry cmd/spn/main.go:34-122 (forks/list, forks/eval, search, repo). Pipeline: forksops.Stream (stream.go:6-856) — Options, Result facets (priors/profile/visibility/momentum), T1 heat scoring, T2 Compare/T3 Contributors workers, collect-then-emit with sort ladder (shortlist→query desc→priors→heat). Cluster adapter (stream.go:952-1045) wires GH TreeSource/CommitSource/ReadmeFetcher → cluster.RunPipeline (pipeline.go:119-520) — top-N eligible, cache, centrality (centrality.go:35-168), BuildFeatures (features.go:18-105), MultiModalEmbed (multimodal.go:39-115: paths/.3, commits/.3, readme/.2, diff/.2 → 4*dim concat + L2), lexical fallback (local.go:12-110: FNV unigram+bigram TF×batch-IDF), single-link clustering (cluster.go:45-190, epsilon=0.55 lexical), novelty scoring (pipeline.go:438-523). Embedding: fastembed.go:13-156 (fast-bge-small-en-v1.5, dim384, cache $XDG_CACHE_HOME/spoon/models/fastembed), vendor anush008/fastembed-go v1.0.0. Persistence: semantic.BuildDocument (semantic.go:18-133) single doc per fork (name/desc/diff/commits/paths/lang/topics, content hash), batch upsert 32, libSQL store SearchRows (store.go:1364-1459). NDJSON emission (forks.go:865-913) with persistSnapshotBestEffort. No zoekt/code-search code present.

**Constraints**:

1. **Fork-level vs file-level** — Zoekt indexes files; Spoon compares forks (semantic.go:18-102 builds one document per fork). [S1: semantic.go:18-102]
2. **Semantic vs lexical** — Zoekt trigram/BM25; Spoon FastEmbed 384-dim semantic. [S2: fastembed.go:13-156, S3: local.go:12-110]
3. **License** — Zoekt Apache-2.0 (NOTICE redistribution LICENSE:79-126); Spoon MIT. [S4: LICENSE:1-9,79-126]
4. **No library interface** — Zoekt exports concrete types (*index.Builder,*search.Searcher), not interfaces. [S5: builder.go:562-650, api.go:917-927]
5. **Index freshness model mismatch** — Zoekt gitindex incremental (gitindex/index.go:387-480); Spoon content-hash+model-keyed cache invalidation (semantic.go:113-133, store.go:1364-1459). [S6: gitindex/index.go:387-480, S7: semantic.go:113-133]
6. **Dependency surface** — Zoekt 27+ transitive deps (roaring, go-git, go-enry, go-ctags, gRPC, OpenTelemetry, RE2/wazero, etc.) [S8: go.mod:1-160]
7. **No release history locally** — Grafted single commit, no tags. [S9: ExploreZoekt summary]
8. **Spoon already model-swap aware** — Changing model identity/dimension requires separate embedding partition/recompute. [S10: LibrarianAlternatives answer]

**Mechanisms**:

- **Trigram indexing** (high): Zoekt scans UTF-8 runes, stores each 3-rune trigram at rune offset; ASCII uses 21-bit direct array, non-ASCII uses map; delta-varint posting offsets; runeOffsets sampled for byte<->rune conversion. [S11: shard_builder.go:89-271, bits.go:76-87,168-198]
- **Frequency-based ngram selection** (high): Query planner sorts query trigrams by corpus frequency, picks two least-common, uses distance iterator or single trigram iterator. [S12: indexdata.go:349-501, hititer.go:95-275]
- **Match-tree planner** (high): Boolean AST -> match tree with regex literal superset extraction, conjunction via andLineMatchTree, pruning. [S13: matchtree.go:1001-1450]
- **Per-shard parallel search** (high): One goroutine per shard; large repos must be split across shards for parallelism. [S14: design.md:65-68, shards.go:804-1091]
- **Compound shard Merge/Explode** (high): Merge sorts by repo priority, copies non-tombstoned docs into compound shard named hash of repo names; Explode reverses. [S15: merge.go:20-277]
- **Standard scoring** (high): Word boundary (500), partial (50), filename base (7000), partial (4000), symbol exact (7000), partial (4000), ctags kind (100), boost cap (400), line order (1), repo-rank tie-break (100). [S16: contentprovider.go:589-610, score.go:98-198]
- **BM25 alternative** (medium): Term frequency + length ratio, low-priority penalty; gated by UseBM25Scoring. [S17: score.go:206-242,363-400]
- **Spoon multimodal embedding** (high): Four weighted blocks (paths .3, commits .3, readme .2, diff .2) -> concat 4xdim -> L2 normalize. [S18: multimodal.go:39-115]
- **Spoon single-link clustering** (high): Cosine distance <= epsilon (default 0.55 lexical) over all pairs; components <3 -> noise; centroid normalized; novelty = distance/eps clamped. [S19: cluster.go:45-190, pipeline.go:438-523]
- **Spoon lexical fallback** (high): FNV unigram+bigram TF×batch-IDF, per-call corpus, L2 norm; triggers on non-lexical failure. [S20: local.go:12-110]
- **Spoon centrality** (medium): Dir file-share (0.6) + commit keyword (0.4) normalized; de-dupes touched parent dirs, averages. [S21: centrality.go:35-168]

**Canonical Sources**:

1. Zoekt design: "doc/design.md" (sourcegraph/zoekt@6e01b543) — positional trigrams, shard format, ranking, query semantics
2. Zoekt API: "api.go" (sourcegraph/zoekt@6e01b543) — Searcher, SearchOptions, Sender, Streamer interfaces
3. Zoekt indexing: "index/builder.go", "index/shard_builder.go", "index/write.go", "index/read.go" (sourcegraph/zoekt@6e01b543)
4. Zoekt query: "query/parse.go", "index/indexdata.go", "index/matchtree.go", "index/eval.go" (sourcegraph/zoekt@6e01b543)
5. Zoekt scoring: "index/score.go", "index/contentprovider.go" (sourcegraph/zoekt@6e01b543)
6. Zoekt sharding: "search/shards.go", "search/aggregate.go", "index/merge.go" (sourcegraph/zoekt@6e01b543)
7. Zoekt git ingestion: "gitindex/index.go" (sourcegraph/zoekt@6e01b543)
8. Zoekt license: "LICENSE" (sourcegraph/zoekt@6e01b543) — Apache-2.0 with NOTICE clause
9. Spoon pipeline: "internal/forksops/stream.go" (svnbjrn/spoon@1a74fa2e) — Options, Result, T1/T2/T3, sort ladder
10. Spoon cluster: "internal/cluster/pipeline.go", "internal/cluster/cluster.go" (svnbjrn/spoon@1a74fa2e)
11. Spoon embedding: "internal/embed/fastembed.go", "internal/embed/multimodal.go", "internal/embed/local.go" (svnbjrn/spoon@1a74fa2e)
12. Spoon semantic: "internal/semantic/semantic.go" (svnbjrn/spoon@1a74fa2e) — BuildDocument, batch upsert
13. Spoon store: "internal/store/store.go" (svnbjrn/spoon@1a74fa2e) — PendingDocuments, UpsertEmbeddings, SearchRows
14. Spoon centrality: "internal/repo/centrality.go" (svnbjrn/spoon@1a74fa2e)
15. Bleve vectors: "docs/vectors.md" (blevesearch/bleve@v2.4.0) — FAISS, float32, 1-2048 dim, l2_norm/dot_product, "vectors" build tag
16. Tantivy: "Cargo.toml" (quickwit-oss/tantivy@v0.27.0) — MIT, Rust; Go binding "anyproto/tantivy-go" — cgo/static lib, production at Anytype
17. Meilisearch: LICENSE (meilisearch/meilisearch@v1.13.0) — MIT; OpenAPI embedders/filters
18. Typesense: "docs/27.1/api/vector-search.md" (typesense.org@v27.1) — GPL-3.0, external vectors, auto-embedding, hybrid search
19. FastEmbed-Go: github.com/anush008/fastembed-go@v1.0.0 — ONNX in-process, BGE-small-en-v1.5

**Disagreements in the Wild**:

- **Trigram vs Embedding for Code Similarity**: Zoekt/Sourcegraph camp asserts trigram + symbol signals suffice for code search (design.md:22-70); Embedding camp (Bleve, Meilisearch, Typesense, Spoon) argues semantic vectors capture renamed/refactored equivalence that trigrams miss. No independent benchmark on fork-level similarity exists — verification lead.
- **Library vs Service**: Zoekt README documents both embeddable Go API and indexserver/webserver/gRPC; Sourcegraph runs it as service. Spoon is a CLI tool — service deployment adds operational burden. Tension unresolved.
- **License Compatibility**: Apache-2.0 + MIT is generally compatible (Apache-2.0 code can be included in MIT project with NOTICE), but patent grant termination on litigation and NOTICE propagation are friction points some legal teams flag. No consensus in Go ecosystem — verification lead.
- **Release Stability**: Zoekt has no semver tags in local checkout; upstream Sourcegraph may cut releases differently than google/zoekt fork. Community reports vary — verification lead.

**Decision Table**:

| Scenario / Workload Shape | Vendoring Zoekt (copy source) | Library Dependency (go get) | Pattern Adaptation (trigram/ranking only) | No Integration (stay FastEmbed) |
| --- | --- | --- | --- | --- |
| **Fork Discovery** (find forks of a repo) | No — Zoekt doesn't do fork discovery; GitHub API does | Same | Same | Yes — Spoon already uses GH API |
| **Fork Clustering** (group similar forks) | Marginal — File-level index must aggregate to fork-level; high effort | Same | Marginal — Trigram similarity != semantic fork similarity | Yes — FastEmbed + single-link already works (cluster.go:45-190) |
| **Code Similarity** (semantic fork comparison) | No — Trigram/BM25 misses semantic equivalence | Same | Marginal — Could adapt ranking signals but no embedding | Yes — FastEmbed 384-dim semantic (multimodal.go:39-115) |
| **Query-time Search** (user searches fork code) | Yes — Zoekt excels: sub-50ms design (design.md:1-21) | Same | Marginal — Re-implementing query planner is high effort | No — Spoon search subcommand exists but limited (cmd/spn/search.go:64-248) |
| **Index Freshness** (ad-hoc fork sets per run) | No — Zoekt assumes stable corpus; Spoon content-hash+model cache | Same | Marginal — Incremental trigram index possible but unproven | Yes — Spoon cache invalidation works (semantic.go:113-133) |
| **Implementation Effort** | Very High (60k+ LoC, 27 deps, no interfaces) | High (version pin, breaking changes, NOTICE) | Medium (extract trigram/ranking ~5k LoC) | Zero (already done) |
| **Runtime Cost** | High (shard mmap, per-shard goroutine, trigram postings) | Same | Low (only needed components) | Low (FastEmbed ONNX, libSQL vectors) |
| **Maintenance Burden** | Very High (upstream sync, breaking changes, no semver) | High (version pin, NOTICE tracking) | Low (owned code) | Zero |
| **Correctness Risk** | High (file->fork aggregation unproven, semantic gap) | High (same) | Medium (ranking signals may not transfer) | Low (validated in production) |
| **License Compatibility** | Marginal — Apache-2.0 NOTICE + patent grant tracking required | Same | Yes — Own code, MIT | Yes — MIT |

**Recommended Commitments**:

**Fork**: Primary integration strategy for Zoekt -> Spoon
**Camps**:

- A) Vendor Zoekt as library dependency for fork clustering + query search
- B) Library dependency (go get github.com/sourcegraph/zoekt) for query search only
- C) Pattern adaptation: extract trigram indexing + ranking signals as owned Spoon code
- D) No integration: keep FastEmbed + lexical fallback, add ANN index if needed

**Each Commitment Forces**:

- A) Vendoring: Import 60k+ LoC, 27 deps, Apache-2.0 NOTICE propagation, no interface mocking, file->fork aggregation layer, semantic gap unaddressed, upstream sync burden
- B) Library: Version pin at grafted commit (no tags), breaking change risk, NOTICE in binary, same semantic gap, service-style deployment if indexserver needed
- C) Pattern Adaptation: Own trigram extraction (shard_builder.go:89-271), ngram selection (indexdata.go:349-501), ranking constants (contentprovider.go:589-610) — ~5k LoC, MIT, no semantic similarity, but enables lexical pre-filter for fork clustering
- D) No Integration: FastEmbed semantic similarity remains primary; add FAISS/HNSW ANN index on libSQL vectors only if brute-force cosine proves insufficient at scale

**Recommendation**: **D) No Zoekt integration for fork similarity/clustering. C) Pattern adaptation ONLY if lexical pre-filter for query-time search becomes a measured bottleneck.**

**Why**:

1. Zoekt solves file-level code search; Spoon needs fork-level semantic similarity — category error
2. Trigram overlap != semantic code equivalence (renamed vars, refactored logic, different impl same algo)
3. FastEmbed + single-link clustering already validated in production (cluster.go:45-190, pipeline.go:438-523)
4. License friction (Apache-2.0 NOTICE + patent grant) for zero semantic gain
5. Maintenance burden of 60k LoC + 27 deps with no semver tags is unjustified
6. Query-time search (cmd/spn/search.go:64-248) could use trigram pre-filter IF profiling shows bottleneck — but Spoon's search subcommand is secondary to fork eval

**Falsifiers for D (would invalidate no-integration)**:

- Spoon's fork clustering produces measurably wrong groupings (false positives/negatives >15%) on a labeled fork-eval benchmark AND adding trigram pre-filter reduces error rate by >5pp
- Spoon's search subcommand latency >500ms p99 on 10k+ fork corpus AND trigram index brings it <50ms
- FastEmbed model swap (e.g., to nomic-embed-text-v1.5) breaks clustering AND trigram is stable alternative

**Falsifiers for C (would invalidate pattern adaptation)**:

- Extracted trigram code >8k LoC or pulls in >5 Zoekt deps transitively
- Trigram pre-filter adds >50ms latency per fork eval run
- Lexical similarity correlates <0.3 with semantic similarity on fork-eval benchmark

**Fallback Recommendation**: **B) Library dependency ONLY for query-time search subcommand (cmd/spn/search.go), gated behind feature flag, with NOTICE file generated at build.**

**Falsifiers for Fallback**:

- go get github.com/sourcegraph/zoekt@v0.0.0-2024... pulls incompatible Go version or breaks build
- Index build time for ad-hoc fork set >30s (Spoon pipeline target <10s)
- User never uses `spn search` — telemetry shows <1% invocation rate

**Veto Recommendation**: **A) Vendoring Zoekt for fork clustering/similarity — NEVER DO THIS.**

**Falsifiers for Veto (would make vendoring viable)**:

- Zoekt adds first-class fork-level semantic similarity API (unlikely, not in design.md)
- Sourcegraph cuts semver releases with stability guarantees AND provides fork-level aggregation
- Spoon relicenses to Apache-2.0 AND user demands file-level code search inside forks as primary feature

**Implementation Strategies (Tiered)**:

**Cheap / Lossy — DO NOT SHIP**: Copy zoekt's trigram extraction (shard_builder.go:89-271) and ranking constants (contentprovider.go:589-610) into Spoon as `internal/index/trigram.go` for lexical pre-filter. Tempting because ~500 LoC. Fails because: (1) misses case-folding variants (bits.go:26-44), (2) misses rune/byte offset mapping, (3) no shard/storage layer -> rebuilds trigram index per run, (4) semantic gap remains.

**Default / Workhorse**: Keep FastEmbed + lexical fallback as primary. Add FAISS/HNSW ANN index on libSQL vectors (store.go:1364-1459) ONLY when brute-force cosine similarity on fork vectors exceeds latency budget (measured). Current path: semantic.BuildDocument -> batch upsert -> SearchRows -> cosine in Go. ANN library: github.com/klauspost/compress (already indirect dep via zoekt go.mod) or native Go HNSW (github.com/klauspost/compress has no HNSW; need separate dep).

**Expensive / Escalation**: If Spoon pivots to "code search inside forks" as primary product (not fork analysis), deploy Zoekt indexserver as sidecar service (Docker, CGO_ENABLED=0), ingest fork repos via gitindex (gitindex/index.go:387-480), expose gRPC/JSON API to Spoon CLI. Trigger: user research shows >50% sessions use search subcommand AND fork eval is secondary.

**Testable Hypotheses**:

1. H: Trigram Jaccard similarity between fork diff sets correlates <0.3 with FastEmbed cosine similarity on same fork pairs. T: Sample 100 fork pairs from spoon eval corpus, compute both, measure Spearman rho. Predicted: rho < 0.3.
2. H: Zoekt index build for 100 forks (avg 50 files each) takes >60s vs Spoon FastEmbed embed <10s. T: Build zoekt index via gitindex for 100 forks, measure wall time. Predicted: Zoekt >60s (shard build + merge).
3. H: Adding trigram pre-filter before FastEmbed clustering reduces Spoon pipeline latency by >20% at 1k forks. T: Instrument stream.go cluster path, add trigram Jaccard filter (threshold 0.1), measure end-to-end. Predicted: <5% improvement (FastEmbed ONNX already fast, trigram index build dominates).
4. H: Spoon search subcommand (cmd/spn/search.go) latency p99 >500ms on 10k indexed forks. T: Load 10k forks, run 100 search queries, measure. Predicted: <200ms (libSQL vector search is fast).
5. H: FastEmbed model swap (bge-small-en-v1.5 -> nomic-embed-text-v1.5) changes fork clustering assignments >30%. T: Re-embed corpus with new model, re-cluster, measure adjusted Rand index vs old clustering. Predicted: >30% change (model-swap known to shift semantic space).
6. H: Zoekt's symbol-aware ranking (ctags) improves fork novelty detection vs FastEmbed alone. T: Run zoekt search for fork's unique symbols, compare to Spoon novelty score (pipeline.go:438-523), measure correlation. Predicted: <0.2 correlation (symbol overlap != novelty).

**Failure Modes** (for Recommended D + Fallback B):

- **Slow Path**: FAISS/HNSW ANN index build O(n log n) dominates pipeline at 100k+ forks. Telemetry: index_build_duration_ms histogram, alert >30s.
- **Wrong Path**: FastEmbed model swap silently changes clustering without detection. Telemetry: cluster_assignment_hash per model+corpus hash, alert on drift.
- **Unsafe Path**: Library dependency on zoekt pulls vulnerable transitive dep (roaring, go-git, gRPC). Telemetry: govulncheck in CI, fail on HIGH/CRITICAL.
- **License Path**: Apache-2.0 NOTICE not propagated in binary distribution. Telemetry: build-time NOTICE file generation check, fail if missing.
- **Operational Path (Fallback B)**: Zoekt indexserver OOM on large corpus (shard >4GiB). Telemetry: container memory usage, alert >80% limit.

**Adjacent / Cross-Domain Leads**:

- **LSIF/SCIP Code Intelligence** (disanalogy: LSIF is precise cross-repo symbol navigation; Zoekt is fuzzy text search; Spoon needs neither — but SCIP's semantic indexing via embeddings is closer to Spoon's FastEmbed). Lead: Sourcegraph's SCIP + embeddings pipeline.
- **Vector Databases** (Qdrant, Weaviate, Milvus) — disanalogy: service deployment vs in-process; but Spoon already uses libSQL (SQLite+vector) which is in-process. Lead: libSQL vector extension maturity.
- **MinHash/LSH for Code Similarity** — disanalogy: MinHash estimates Jaccard on sets; trigram is exact Jaccard on 3-grams; embeddings are cosine on dense vectors. Lead: "Scalable Similarity Search for Code" (SASL 2022) — but fork-level not file-level.
- **Git Diff Semantic Analysis** — disanalogy: AST diff vs embedding diff; Spoon uses diff text as one embedding block (multimodal.go:39-115 weight .2). Lead: GumTree, Diffy — but heavyweight.

**Open Questions for the Human**:

1. Is `spn search` (query-time code search inside forks) a current or planned primary user workflow? If yes, Fallback B gains weight.
2. What is the acceptable license friction budget? Apache-2.0 NOTICE + patent grant tracking — legal review needed?
3. Is fork clustering quality currently a pain point? (False positive/negative rate on manual eval?)
4. Does Spoon need to support file-level code search *inside* forks as a feature, or only fork-level comparison?
5. What is the target fork corpus size for a single `spn forks eval` run? (100? 1k? 10k? 100k?)

**Critical Files** (planner MUST read first):

1. `/home/svnbjrn/dev/spoon/internal/forksops/stream.go` — entire pipeline, integration seams
2. `/home/svnbjrn/dev/spoon/internal/cluster/pipeline.go` — clustering, embedding, novelty
3. `/home/svnbjrn/dev/spoon/internal/embed/fastembed.go` — FastEmbed integration, cache
4. `/home/svnbjrn/dev/spoon/internal/embed/multimodal.go` — 4-block fork embedding
5. `/home/svnbjrn/dev/spoon/internal/semantic/semantic.go` — BuildDocument, content hash
6. `/home/svnbjrn/dev/spoon/internal/store/store.go` — libSQL persistence, SearchRows
7. `/home/svnbjrn/rsrch/projects-mrgr/sources/zoekt/api.go` — Searcher interface, public API
8. `/home/svnbjrn/rsrch/projects-mrgr/sources/zoekt/index/builder.go` — concrete Builder, no interface
9. `/home/svnbjrn/rsrch/projects-mrgr/sources/zoekt/index/shard_builder.go` — trigram extraction core
10. `/home/svnbjrn/rsrch/projects-mrgr/sources/zoekt/doc/design.md` — design rationale, limitations
11. `/home/svnbjrn/rsrch/projects-mrgr/sources/zoekt/LICENSE` — Apache-2.0 NOTICE clause
12. `/home/svnbjrn/dev/spoon/go.mod` — deps, FastEmbed vendor, replace directives

**Sources**:
[S1] semantic.go:18-102 — Spoon BuildDocument single fork doc
[S2] fastembed.go:13-156 — FastEmbed model, dim, cache
[S3] local.go:12-110 — Lexical fallback FNV TF-IDF
[S4] LICENSE:1-9,79-126 — Apache-2.0, NOTICE redistribution
[S5] builder.go:562-650, api.go:917-927 — Concrete types, no interfaces
[S6] gitindex/index.go:387-480 — Zoekt git incremental ingestion
[S7] semantic.go:113-133 — Spoon content-hash+model cache invalidation
[S8] go.mod:1-160 — Zoekt 27+ transitive deps
[S9] ExploreZoekt summary — No release tags, grafted commit
[S10] LibrarianAlternatives answer — Spoon model-swap awareness
[S11] shard_builder.go:89-271, bits.go:76-87,168-198 — Trigram indexing mechanism
[S12] indexdata.go:349-501, hititer.go:95-275 — Frequency-based ngram selection
[S13] matchtree.go:1001-1450 — Match-tree planner
[S14] design.md:65-68, shards.go:804-1091 — Per-shard parallel search
[S15] merge.go:20-277 — Compound shard Merge/Explode
[S16] contentprovider.go:589-610, score.go:98-198 — Standard scoring constants
[S17] score.go:206-242,363-400 — BM25 alternative
[S18] multimodal.go:39-115 — Spoon 4-block weighted embedding
[S19] cluster.go:45-190, pipeline.go:438-523 — Single-link clustering, novelty
[S20] local.go:12-110 — Lexical fallback details
[S21] centrality.go:35-168 — Dir/file-share + commit keyword centrality

**Verification Leads** (for low-confidence claims):

- Zoekt release cadence / semver adherence / breaking change history: search "sourcegraph/zoekt releases" "github.com/sourcegraph/zoekt tags" — no tags in local checkout
- Zoekt production query latency p50/p99 at scale: search "zoekt benchmark" "sourcegraph zoekt latency" — design.md claims sub-50ms design goal only
- Zoekt index build time / memory for N repos: search "zoekt index build time" — not in source
- Apache-2.0 NOTICE propagation in Go binary: search "go binary NOTICE file Apache-2.0" — standard practice but verify
- FastEmbed model swap clustering drift measurement: no public benchmark — run spoon eval with two models
- Trigram vs embedding correlation on fork-level similarity: no public study — run Hypothesis 1 test
- Tantivy-go binding maturity for production: search "anyproto/tantivy-go issues" "tantivy-go production"
- libSQL vector extension ANN support: search "libsql vector index HNSW" — verify current capabilities

**Meta-Observation**: The research framing assumed Zoekt could plug into Spoon's fork analysis pipeline. The data reveals a category error: Zoekt is a **file-level code search engine** (trigram + symbol signals, sub-50ms query latency on stable corpora); Spoon is a **fork-level semantic similarity analyzer** (embedding + single-link clustering on whole-fork documents, ad-hoc corpus per run). The overlap is near-zero. The only plausible integration is pattern-adapting Zoert's trigram extraction as a lexical pre-filter for query-time search — but Spoon's search subcommand is not the primary product. The framing pushback was correct: "integration" was never well-defined, and the architectural mismatch makes most definitions negative-value. The LLM bias toward "more tools = better" would have invented a vendoring plan; the adversarial self-attack correctly identified the semantic gap, license friction, and maintenance burden as veto signals. What surprised me: Spoon's pipeline is more sophisticated than I assumed (multimodal embedding, content-hash cache, model-swap awareness, libSQL persistence) — it doesn't need Zoekt's machinery. What the framing missed: the user may actually want file-level search *inside* forks (a different product), not better fork clustering. Next cycle: if user confirms search subcommand is priority, prototype Fallback B (zoekt library for search only) with NOTICE generation and feature flag.

**Parking Lot**:

- Zoekt as sidecar service for enterprise fork-code-search product (different product)
- Tantivy via tantivy-go for higher-performance trigram if lexical pre-filter needed
- Bleve with FAISS vectors as alternative to FastEmbed+libSQL (but Apache-2.0 + FAISS cgo)
- SCIP/LSIF integration for precise symbol navigation (not fuzzy search)
- Incremental trigram index for ad-hoc fork sets (unproven, high effort)
- FastEmbed model benchmark suite for clustering stability across model swaps
