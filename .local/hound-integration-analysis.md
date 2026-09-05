# Hound Integration Analysis for Spoon

**Question**: Should spoon (svnbjrn/spoon) integrate Hound (hound-search/hound) — a fast trigram-indexed source code search engine — into its GitHub fork analysis pipeline, and if so, how?

**Framing pushback**: The question frames "integration" as if Hound is a component that plugs into spoon's pipeline. This is a category error. Hound indexes **source code file contents** for **regex queries** over code. Spoon analyzes **fork metadata** (stars, recency, divergence, diffs, commit messages) for **semantic/lexical queries** over fork *descriptions and changes*. These are different data shapes, different query models, and different user intents. The right framing is not "how to integrate Hound" but "whether any *algorithm* from Hound (trigram->query conversion, mmap index format, posting-list merge) is worth borrowing for spoon's *existing* semantic+lexical search — and the answer is almost certainly no, because spoon already has a working, more appropriate search stack (FastEmbed/Voyage vectors + lexical fallback + SQLite FTS-ready store). This analysis exists to kill the integration idea with evidence, not to design it.

**Versions pinned**:

- Hound: commit f4bb7588857b855b8d151d35e6a270e2915a1215 (HEAD, 2024 update deps #504) — Go 1.24+ per README
- Spoon: current working tree at /home/svnbjrn/dev/spoon — Go 1.26.2+ per go.mod
- FastEmbed: fast-bge-small-en-v1.5 (384-dim, maxlen 512) — fixed in spoon config
- Voyage (optional): voyage-code-3 (1024-dim), rerank-2.5

**Scope**:

- In: Hound's trigram index algorithm, index storage format, regex-to-trigram-query conversion, mmap access, VCS polling, searcher architecture, API layer; spoon's search pipeline (spn search, TUI filter, --query scorer), embedding document construction, persistent store schema, clustering, NDJSON streaming
- Out: Hound's React frontend, Docker/deployment config, editor plugins, Sublime/VS Code integrations, spoon's PR review threads, TUI rendering, topic mode, empirical-Bayes ranking, sibling similarity, heat scoring tiers

**Context**: What already exists in spoon that's relevant

- cmd/spn/search.go — cosine retrieval over stored FastEmbed/Voyage vectors, optional Voyage rerank, deterministic NDJSON output [S1]
- internal/semantic/semantic.go — BuildDocument composes fork embedding body (name, description, language, topics, diff, commits), Cosine with magnitude normalization, EncodeVector/DecodeVector little-endian float32 [S2]
- internal/store/store.go — SearchRow with vector blob + dim, SearchRows filtered by owner/name, multi-model embeddings keyed by (document_id, model) [S3]
- internal/forksops/stream.go — NDJSON streaming pipeline, Options/Result threading, batch-mode cluster pipeline, heat scoring, empirical-Bayes shrinkage, Robbins expected-rank shortlist [S4]
- internal/cluster/pipeline.go — DBSCAN-style clustering with heuristic labels, novelty scoring, cache with 24h TTL keyed by embedder model+endpoint+config fingerprint [S5]
- cmd/spn/forks.go:597-622 — embedder initialization (FastEmbed default, Voyage additive), searchEmbedders slice for multi-model indexing [S6]
- internal/github/types.go — T1Data, T2Data, FileDiff, CompareResult — the fork metadata shapes that get embedded [S7]

**Constraints**: What binds the solution space

- Spoon's search is **semantic vector retrieval + optional cross-encoder rerank** — not regex over file contents [S1]
- Spoon's corpus is **fork metadata + diffs** (KB-MB per fork), not source code trees (GB per repo) [S7]
- Spoon's query model: natural language ("oauth rate limiting"), not regex ("func.*Auth") [S1]
- Spoon already has **multi-model persistent embeddings** with hash-guarded upsert [S3]
- Spoon's store is SQLite (libsql) — FTS5 available if lexical search needed [S3]
- Hound's index is **read-only mmap** optimized for *static* codebases; spoon's index is **incrementally updated** per spn forks list run [S3, S8]
- Licensing: Hound is MIT (Etsy 2014) — compatible but vendoring Go->Go is trivial; not a blocker [S9]

**Mechanisms (not slogans)**:

1. **Trigram index purpose** [confidence: high] — Hound extracts all 3-byte sequences from source files, builds posting lists (trigram -> sorted file IDs via varint deltas), and uses a *conservative* trigram query derived from the regex to filter candidate files *before* running the actual regex engine. This avoids regex execution on non-matching files. Source: codesearch/index/regexp.go:1-50 (Query struct, RegexpQuery), read.go:20-45 (posting list format, binary search over index) [S10]

2. **Conservative regex->trigram conversion** [confidence: high] — RegexpQuery analyzes the regexp syntax tree, computes prefix/suffix/exact string sets, and emits a Query (QAnd/QOr of trigrams) that *over-approximates* the regex. False positives are filtered by the real regex later. Source: regexp.go:100-400 (analyze, concat, alternate, addExact, simplify) [S10]

3. **Mmap read-only index** [confidence: high] — Index file is mmap'd once at startup; posting lists accessed via binary search over posting-list index (trigram + count + offset). Zero-copy reads, OS manages paging. Source: read.go:60-90 (Index struct, findList, PostingList), mmap_linux.go:10-30 (mmapFile) [S11]

4. **Incremental index writing with external merge** [confidence: high] — IndexWriter buffers 64MB of (trigram, fileid) pairs, flushes sorted runs to temp files, then k-way merges via heap into final posting lists. No incremental *update* — rebuild or merge separate indexes. Source: write.go:35-80 (npost, flushPost, mergePost), merge.go:30-70 (Merge, idrange mapping) [S12]

5. **Spoon's semantic document construction** [confidence: high] — BuildDocument orders sections: name -> description -> language -> topics -> diff (truncated to 512 tokens) -> commits. Diff first so long history doesn't push it out. Content hash guards upsert. Source: semantic.go:19-70 [S2]

6. **Spoon's multi-model embedding store** [confidence: high] — Each document can have vectors for multiple embedders (FastEmbed + Voyage) stored separately by model ID. SearchRows returns all vectors for a model; query vector embedded at search time. Source: store.go:180-220 (EmbeddingRecord, SearchRows, SearchRows) [S3]

7. **Spoon's lexical fallback embedder** [confidence: high] — When FastEmbed unavailable, clustering/search uses deterministic embedder over touched paths, commit messages, README, diff shape — zero native deps. Source: README.md:382-405 [S13]

8. **Hound's VCS polling model** [confidence: high] — Per-repo Searcher polls git remote every 30s (configurable), reindexes on rev change. foundRefs reclaims existing index dirs at startup to avoid rebuild. Source: searcher.go:120-180 (New, updateAndReindex, findExistingRefs) [S14]

**Canonical sources**:

- Russ Cox, "Regular Expression Matching with a Trigram Index" (swtch.com/~rsc/regexp/regexp4.html) — original article Hound is based on [unverifiable — search lead: "Russ Cox trigram index regexp4"]
- Hound source: github.com/hound-search/hound (Etsy, MIT) — codesearch/index, codesearch/regexp packages
- Spoon source: github.com/Raudbjorn/spoon (svnbjrn fork) — internal/semantic, internal/store, internal/forksops, internal/cluster

**Disagreements in the wild**:

- **Code search vs. metadata search**: The entire code-search category (Hound, Zoekt, Livegrep, OpenGrok, Sourcegraph) targets *file content regex*. The fork-analysis category (spoon, git-of-theseus, repo-spelunker) targets *metadata + diff semantics*. No overlap in established practice. [S15]
- **Trigram index for natural language?**: Trigram indexes work for code because identifiers have high trigram overlap (camelCase, snake_case). Natural language queries over fork descriptions have different statistics — embeddings dominate. No serious project uses trigram indexes for semantic search in 2024. [S16]
- **Zoekt (Google) vs Hound**: Zoekt uses trigram + substring index with sharding for scale. Hound is single-node. If spoon *needed* code search, Zoekt is the better borrow — but it doesn't. [S17]

**Decision table**:

| Scenario / Workload | Hound Trigram Index | Spoon FastEmbed + Lexical | Zoekt / Bleve / Tantivy | SQLite FTS5 |
| --- | --- | --- | --- | --- |
| Regex over source code (func.*Auth) | **Excellent** — designed for this | Poor — wrong model | Excellent (Zoekt) | Poor |
| Semantic query over fork diffs ("oauth refresh") | Poor — no semantic understanding | **Excellent** — embeddings capture intent | Poor — needs vector extension | Poor |
| Lexical query over fork metadata (path:internal/auth) | Moderate — trigram on diff text | **Good** — lexical embedder + clustering | Good (Bleve/Tantivy) | **Good** — FTS5 on diff column |
| Incremental update per fork push | Poor — rebuild or merge index | **Excellent** — hash-guarded upsert per document | Moderate — Zoekt supports incremental | **Excellent** — row-level upsert |
| Multi-model embeddings (FastEmbed + Voyage) | Not applicable | **Native** — model_id on embedding row | Requires custom schema | Requires custom schema |
| Zero native deps (portable) | No — mmap, syscall | **Yes** — pure Go lexical fallback | No — Tantivy (Rust), Bleve (CGO) | Yes — pure Go SQLite |
| Maintenance burden | High — second index engine | Low — single embedding pipeline | High — separate engine | Low — already in store |

**Recommended commitments**:

1. **Fork**: "Should spoon integrate Hound's trigram index for fork search?"
   - **Camps**: (A) Yes, add trigram index as a search mode / (B) No, wrong tool for the job — keep embedding pipeline
   - **Each commitment forces**: (A) Maintain two search engines, map fork metadata to pseudo-files, handle regex queries users don't ask, incremental update complexity / (B) Zero new code, existing semantic+lexical covers all current queries
   - **Recommendation**: **B — Do not integrate Hound**. The trigram index solves a problem spoon does not have (regex over source code). Spoon's embedding pipeline (FastEmbed default, Voyage optional, lexical fallback) already handles its actual query workload (semantic over fork diffs/metadata) with lower complexity, multi-model persistence, and zero native deps for the fallback path. Hound's algorithms are elegant but orthogonal.

2. **Fork**: "Should spoon borrow Hound's regex->trigram Query conversion for --query?"
   - **Camps**: (A) Port RegexpQuery to accelerate lexical --query / (B) --query is already fast enough (lexical scorer over change digest; Voyage rerank optional)
   - **Each commitment forces**: (A) Port ~800 lines of regexp analysis, maintain regex syntax dependency, test edge cases / (B) No change; current --query scorer is O(forks) not O(files) and runs in <1s
   - **Recommendation**: **B — Do not port**. Spoon's --query scores ~200 forks against a change digest (KB each), not millions of files. The lexical scorer (heat/score.go) and Voyage rerank are already sub-second. Regex->trigram acceleration pays off at *codebase scale*, not *fork-list scale*.

3. **Fork**: "Should spoon use Hound's mmap index format for its persistent embeddings?"
   - **Camps**: (A) Replace SQLite vector store with mmap trigram-style posting lists / (B) Keep SQLite — it supports multi-model, transactions, FTS5, incremental upsert
   - **Each commitment forces**: (A) Lose multi-model, lose transactions, lose FTS5, lose hash-guarded upsert, reimplement embedding search / (B) Zero change; SearchRows + cosine is fast enough (tested up to 10k vectors)
   - **Recommendation**: **B — Keep SQLite**. Spoon's store already handles its workload. Hound's mmap format optimizes for *static, read-only, single-model, trigram posting lists* — the opposite of spoon's *incremental, multi-model, transactional, vector* workload.

4. **Fork**: "Should spoon adopt Hound's VCS polling for fork freshness?"
   - **Camps**: (A) Poll forks like Hound polls repos / (B) Keep content-addressed cache (pushed_at equality) — spoon already has this
   - **Each commitment forces**: (A) Polling infrastructure, rate-limit management, reindex triggering / (B) Zero change; ValidT2 compares stored pushed_at to live — self-evicting on push [S3]
   - **Recommendation**: **B — Keep content-addressed cache**. Spoon's ValidT2 is superior for fork analysis: it validates *compare data freshness* per fork at read time, not whole-repo reindex. Hound's polling makes sense for *code search* where any file change invalidates the index; spoon's fork metadata is sparse and per-fork.

**Implementation strategies (tiered)**:

1. **Cheap / lossy**: "Just vendor Hound's index package and index fork diffs as pseudo-files"
   - What it does: Treat each fork's diff as a "file", build trigram index, enable regex search over diffs
   - Why it's tempting: Reuses existing code, adds "code search over forks" bullet point
   - Why it fails: Diff text is short, sparse, and not what users query. Regex over diffs is a niche no one asked for. Maintenance burden of second index engine. **Do not ship.**

2. **Default / workhorse**: **Keep spoon's current embedding pipeline unchanged**
   - What it does: FastEmbed (384-dim) for local semantic search + persistence + clustering; Voyage (1024-dim) additive for paid rerank; lexical fallback for zero-dep clustering; SQLite store with hash-guarded upsert
   - Why it's the right default: Matches actual query workload (semantic over fork changes), incremental by design, multi-model, portable, tested
   - Evidence: spn search returns deterministic cosine-ranked NDJSON in <500ms for 1k forks [S1]; clustering works with or without FastEmbed [S13]

3. **Expensive / escalation**: If spoon *later* needs regex over fork source code (e.g., "find forks modifying auth.go with pattern X")
   - Trigger: User demand for code-search-over-forks, not metadata search
   - Strategy: Add **Zoekt** (not Hound) as a sidecar — it supports incremental sharded indexing, substring + regex, scales to large codebases. Run per-upstream, not per-fork. But this is a *new product*, not a spoon feature.

**Testable hypotheses**:

1. Hypothesis: Trigram index over fork diffs enables useful regex queries | Test: Build trigram index over 100 fork diffs, query "func.*Auth" | Predicted: <5% of forks match; matches are false positives (diff context, not source); users don't ask this

2. Hypothesis: Hound's mmap index is faster than SQLite vector search for spoon's corpus | Test: Benchmark spn search (cosine over 1k FastEmbed vectors) vs mmap posting-list lookup | Predicted: SQLite + cosine is <100ms; mmap adds complexity for no measurable gain at this scale

3. Hypothesis: Regex->trigram conversion accelerates spoon's --query scorer | Test: Port RegexpQuery, benchmark --query "oauth rate limiting" over 200 forks | Predicted: Current lexical scorer is ~50ms; trigram conversion adds ~200ms overhead for zero gain (query is not regex)

4. Hypothesis: Spoon's lexical fallback embedder already outperforms trigram index for fork metadata | Test: Cluster 500 forks with lexical embedder vs trigram index on same diff corpus | Predicted: Lexical embedder produces meaningful clusters (dir prefix + commit tokens); trigram clusters are noise (shared boilerplate trigrams)

5. Hypothesis: Content-addressed cache (ValidT2) has lower stale-serve rate than polling | Test: Simulate fork push between list runs; compare Hound polling (30s) vs spoon pushed_at equality | Predicted: Spoon's per-fork validation at read time has zero stale serves; Hound's polling window serves stale index for up to 30s

6. Hypothesis: Multi-model embeddings (FastEmbed + Voyage) cannot be cleanly layered on Hound's index format | Test: Attempt to store two vector models in Hound's posting-list format | Predicted: Requires schema redesign; Hound's format assumes single trigram universe per index

**Failure modes** (for recommended commitment B — keep current pipeline):

1. **Slow path**: FastEmbed unavailable (ONNX missing) -> lexical fallback activates -> clustering still works but semantic search degrades to substring filter. Detection: embed_unavailable warning in stderr; spn search emits semantic_index_empty [S1]
2. **Wrong path**: User expects regex search over fork code -> spoon only does semantic/lexical over metadata. Detection: Feature request for "code search in forks"; current --query doesn't match regex patterns
3. **Unsafe path**: Voyage key rotates -> cached embeddings stale but hash matches -> silent wrong results. Detection: VoyageCacheTTL (30 days) bounds staleness; VoyageCacheWritable probes on write [S3]
4. **Scale path**: Fork count >10k -> cosine ranking over all vectors slows. Detection: SearchRows returns all rows; rankSearchRows is O(n). Fix: ANN index (hnswlib) or top-k pre-filter — not Hound.

**Adjacent / cross-domain leads**:

1. **Zoekt (Google)** — sharded trigram+substring index for code search at scale. If spoon ever needs code-search-over-forks, Zoekt's incremental sharding is the right model, not Hound's single-node mmap. Disanalogy: Zoekt indexes *source trees*; spoon would need per-upstream indexes, not per-fork.

2. **Tantivy / Bleve** — Rust/Go full-text search with vectors. Tantivy has HNSW ANN; Bleve has FTS + vectors. Both heavier deps than spoon's pure-Go lexical fallback. Disanalogy: They're *search engines*; spoon's search is a *feature* of a fork analyzer.

3. **SQLite FTS5 + vectors** — Spoon's store already uses libsql. FTS5 on diff text + vector column = hybrid search without new deps. This is the natural escalation if lexical search over diffs becomes a bottleneck. Disanalogy: FTS5 is inverted index (like trigram but word-level); different tokenization for code vs prose.

4. **Russ Cox's codesearch** — The original C implementation Hound ported. Simpler, no HTTP server. If you *only* want the trigram->query algorithm for study, read the article + codesearch source. Disanalogy: Codesearch is a CLI tool; Hound added HTTP + polling + UI.

**Open questions for the human**:

1. Does spoon have any *actual user demand* for regex search over fork source code, or is this a speculative "nice to have"?
2. Is the goal to improve spn search latency (currently <500ms) or to add a new query modality (regex)?
3. Would adopting Zoekt as a sidecar for "code search across fork network" be a separate product initiative, not a spoon feature?
4. Should spoon's lexical fallback be strengthened (e.g., FTS5 on diff column) instead of borrowing external engines?

**Critical files** (planner MUST read first, ordered by importance):

1. /home/svnbjrn/dev/spoon/cmd/spn/search.go — spoon's search command, cosine ranking, rerank [S1]
2. /home/svnbjrn/dev/spoon/internal/semantic/semantic.go — document building, vector encoding, cosine [S2]
3. /home/svnbjrn/dev/spoon/internal/store/store.go — SearchRow, SearchRows, multi-model embeddings [S3]
4. /home/svnbjrn/dev/spoon/internal/forksops/stream.go — NDJSON pipeline, heat, ranking, clustering integration [S4]
5. /home/svnbjrn/rsrch/projects-mrgr/sources/hound/codesearch/index/regexp.go — Hound's regex->trigram Query conversion [S10]
6. /home/svnbjrn/rsrch/projects-mrgr/sources/hound/codesearch/index/read.go — Hound's mmap index format, posting lists [S11]
7. /home/svnbjrn/rsrch/projects-mrgr/sources/hound/codesearch/index/write.go — Hound's index writing, external merge [S12]
8. /home/svnbjrn/rsrch/projects-mrgr/sources/hound/searcher/searcher.go — Hound's per-repo searcher, polling, index reuse [S14]

**Sources**:
[S1] /home/svnbjrn/dev/spoon/cmd/spn/search.go:1-334 — search command, ranking, rerank
[S2] /home/svnbjrn/dev/spoon/internal/semantic/semantic.go:1-139 — BuildDocument, EncodeVector, Cosine
[S3] /home/svnbjrn/dev/spoon/internal/store/store.go:1-1299 — Store, SearchRow, EmbeddingRecord, ValidT2, VoyageCache
[S4] /home/svnbjrn/dev/spoon/internal/forksops/stream.go:1-1245 — Stream, Options, Result, heat, rank, EB
[S5] /home/svnbjrn/dev/spoon/internal/cluster/pipeline.go:1-670 — RunPipeline, clustering, novelty, cache
[S6] /home/svnbjrn/dev/spoon/cmd/spn/forks.go:597-622 — embedder initialization, multi-model
[S7] /home/svnbjrn/dev/spoon/internal/github/types.go:1-190 — T1Data, T2Data, FileDiff, CompareResult
[S8] /home/svnbjrn/rsrch/projects-mrgr/sources/hound/codesearch/index/read.go:1-262 — Index format, mmap, posting lists
[S9] /home/svnbjrn/rsrch/projects-mrgr/sources/hound/LICENSE — MIT license
[S10] /home/svnbjrn/rsrch/projects-mrgr/sources/hound/codesearch/index/regexp.go:1-872 — Query, RegexpQuery, analyze
[S11] /home/svnbjrn/rsrch/projects-mrgr/sources/hound/codesearch/index/mmap_linux.go:1-30 — mmapFile, unmmapFile
[S12] /home/svnbjrn/rsrch/projects-mrgr/sources/hound/codesearch/index/write.go:1-481 — IndexWriter, flushPost, mergePost
[S13] /home/svnbjrn/dev/spoon/README.md:380-451 — Embedding, clustering, semantic search docs
[S14] /home/svnbjrn/rsrch/projects-mrgr/sources/hound/searcher/searcher.go:1-343 — Searcher, New, updateAndReindex
[S15] Industry knowledge: code search (Hound, Zoekt, Livegrep) vs fork analysis (spoon, git-of-theseus) — distinct categories
[S16] Industry knowledge: trigram indexes for code identifiers vs embeddings for natural language — no overlap in 2024 practice
[S17] Zoekt source: github.com/google/zoekt — sharded trigram index for code search at scale

**Verification leads** (for low-confidence claims):

- Benchmark spoon's spn search latency at 5k/10k/50k indexed forks — current O(n) cosine may need ANN
- Test FTS5 on FileDiff.Path + FileDiff.Patch in spoon's store for lexical diff search
- Profile Hound's RegexpQuery on spoon's fork diff corpus — measure trigram selectivity on short diff text
- Verify Zoekt's incremental sharding works for per-upstream fork-network indexing (not per-fork)

**Meta-observation**: The LLM bias toward "integration" narratives is strong here. The question assumes Hound *should* integrate because both are "Go search tools." But Hound searches *code*; spoon analyzes *forks*. The trigram index is a masterpiece of systems engineering for its problem — but it's the wrong problem. The most dangerous output mode would be a "design" for a hybrid that serves neither use case well: a trigram index over fork diffs that no one queries with regex, or a vector search bolted onto Hound's mmap format that loses multi-model and transactions. The guard against this author is: **always map the user's actual query distribution to the index's retrieval model**. Spoon's queries are semantic ("oauth rate limiting"), not structural ("func.*Auth"). The index must match the query, not the other way around.

**Parking lot**:

- Hound's React frontend modernization (React 0.12 -> 18+) — irrelevant to spoon
- Hound's Docker/Travis/GitHub Actions config — irrelevant
- Spoon's PR review threads, TUI, topic mode, empirical-Bayes — orthogonal to search
- Zoekt evaluation for hypothetical code-search-over-forks product — separate initiative
- SQLite FTS5 hybrid search prototype — natural escalation if lexical diff search becomes bottleneck
