# Architecture and data flow

Re-baselined at HEAD `426049c` (after the `19dd9f5` removal of the OpenVINO
reranker, GenAI labeler, and `internal/models` registry). Sections 2, 3, and 5
are preserved from the prior revision after re-verification at HEAD.

## 1. Subsystem map

### `cmd/spn`

`cmd/spn/forks.go` is the non-interactive integration point. It resolves the
FastEmbed configuration, constructs the semantic embedder when embedding is not
suppressed, passes the embedder and model identity to `cluster.RunPipeline`,
builds semantic documents, and indexes pending documents after relational fork
data is persisted. Relevant functions: `runForks`, `resolveFastEmbedConfig`,
`emitSemanticIndexWarning`.

There is no reranker or label-polisher construction here anymore: the former
`newQueryScorer` and `newLabelPolisher` constructors were deleted in `19dd9f5`.
`--query` relevance flows through `internal/forksops/stream.go:scoreQuery` with
`opts.QueryScorer` left nil, which selects the built-in lexical scorer
(comment in `cmd/spn/forks.go`: *"Query relevance uses the built-in lexical
scorer (opts.QueryScorer nil)."*).

`cmd/spn/search.go` loads the configured FastEmbed model, embeds the query,
loads current rows from `store.SearchRows`, decodes vectors, and ranks them by
magnitude-normalized cosine (`semantic.Cosine`). This is retrieval over the
persistent index, not the `--query` scorer path.

### `cmd/spoon` and TUI

The interactive `spoon` command uses the built-in lexical embedder for
clustering (`cmd/spoon/main.go`). It no longer constructs a label polisher
(the GenAI polisher and its `cmd/spoon/main.go` constructor were deleted in
`19dd9f5`; grep confirms zero labeler/reranker/genai references remain in
`cmd/spoon/`), and it does not use the persistent FastEmbed semantic-search
index. `internal/tui` renders cluster assignments and labels returned by the
stream/pipeline.

### `internal/embed`

This package owns the `Embedder` abstraction, `Vector` type, lexical
`LocalEmbedder` (`local.go`), the FastEmbed/ONNX implementation
(`fastembed.go`, `fastembed_provision.go`), multimodal fork-feature
construction, and query scoring (`queryscore.go`). `backend.go` now exposes
only `fastembed` as the persistent/semantic backend and `builtin` as an
internal lexical implementation. OpenVINO reranking is gone: the reranker, its
config, the pair template, and all shared OpenVINO runtime/FFI support files
were deleted in `19dd9f5` (see [reranker-labeler.md](reranker-labeler.md)).

### `internal/semantic`

`BuildDocument` converts a fork into one deterministic body; `IndexPending`
embeds pending bodies in batches and persists encoded float32 vectors;
`EncodeVector`/`DecodeVector` define the little-endian float32 blob format; and
`Cosine` performs defensive magnitude-normalized similarity.

### `internal/cluster`

`pipeline.go` is the shared orchestration layer. It selects the candidate set,
loads or writes a cluster cache, builds `embed.ForkFeatures`, calls
`embed.MultiModalEmbed`, optionally classifies semantic categories, runs
single-link cosine clustering, and generates heuristic labels via
`labelClusters` → `HeuristicLabel` (`pipeline.go:762,781`). The `LabelPolisher`
seam (`PipelineOptions` field and step-8a wiring) was deleted in `19dd9f5`;
heuristic labels are now the only label path. Failures either fall back to
lexical clustering or return a structured non-fatal skip reason, depending on
which boundary failed.

### `internal/models` — deleted

The package was removed in `19dd9f5`. It had been a pure-Go Hugging Face
repository downloader and model registry that stored models under
`$XDG_DATA_HOME/spoon/models` (or `~/.local/share/spoon/models`) in an
OVMS-compatible directory layout and served the optional reranker and labeler
provisioning slots. Generic model downloading no longer exists; the only
surviving download path is FastEmbed's own pinned-archive provisioning in
`internal/embed/fastembed_provision.go` (see [embedders.md](embedders.md)).

## 2. Fork-to-vector data flow

1. The forge provider returns fork metadata and T2 compare data.
2. `embed.BuildForkFeatures`/`MultiModalEmbed` create modality text from:
   - touched paths;
   - non-merge commit subjects/messages;
   - a bounded README excerpt;
   - normalized diff/diffstat content.
3. The selected embedder encodes the modalities. FastEmbed uses its BGE query
   instruction for query text and passage encoding for documents; lexical mode
   uses corpus-level TF-IDF and signed feature hashing.
4. The modality vectors are concatenated with fixed weights. The current
   documented lexical weights are paths `0.3`, commits `0.3`, README `0.2`, and
   diff `0.2` (`docs/embedders.md`, `internal/embed/multimodal.go`).
5. The cluster pipeline runs cosine-distance clustering and writes cluster ID,
   label, member count, novelty, and optional category facets back to each
   fork's heat/result fields.
6. Independently, `semantic.BuildDocument` creates the persistent single-body
   document. The body is deliberately ordered so the diff section appears
   before commit lists and cannot be pushed out by a long history. Large diffs
   are truncated to the embedding window and expose a truncation warning.
7. The body hash is `SHA-256(body)` and does **not** include the model ID. This
   permits multiple models to share one `documents` row while keeping separate
   `embeddings(document_id, model)` rows. Model identity belongs on the
   embedding row and is part of pending/search selection.
8. `IndexPending` batches at 32 for FastEmbed, validates dimension and finite
   values, encodes float32 values as little-endian bytes, and upserts only when
   the document's current content hash still matches. This prevents a stale
   in-flight embedding from overwriting newer fork content.

## 3. Persistence schema

`internal/store/store.go` creates these relevant tables:

```sql
CREATE TABLE documents (
  document_id TEXT PRIMARY KEY,
  fork_key TEXT NOT NULL UNIQUE REFERENCES forks(fork_key) ON DELETE CASCADE,
  content_hash TEXT NOT NULL,
  body TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE embeddings (
  document_id TEXT NOT NULL REFERENCES documents(document_id) ON DELETE CASCADE,
  model TEXT NOT NULL,
  dim INTEGER NOT NULL,
  vector BLOB NOT NULL,
  content_hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(document_id, model)
);
```

`PendingDocuments` selects rows with no embedding for the requested model or
with a stale embedding hash. `SearchRows` joins only embeddings whose hash still
matches the document, and filters by owner/name when requested. Reimplementation
must preserve this join condition; otherwise semantic search can return vectors
for superseded fork content.

The store is SQLite-compatible and best-effort in the current CLI: a store or
semantic-index failure emits a warning while relational fork listing continues.

### Store and config locations (post-relocation series `2f1560a`, `7d56911`,
`b425566`, `9abb5dc`)

- **Database:** `$XDG_CONFIG_HOME/spoon/spoon.db` (default
  `~/.config/spoon/spoon.db`). Hosts without a resolvable home fall back to
  `/var/lib/spoon/spoon.db` (`internal/store/store.go:173-205`).
- **One-time migration:** `OpenDefault` migrates the pre-relocation
  `$XDG_DATA_HOME/spoon/spoon.db` location once (`DefaultPath`, `legacyPath`,
  `migrateLegacyDB`).
- **Config:** `$XDG_CONFIG_HOME/spoon/config.json` with `/etc/spoon/config.json`
  as the no-home/admin fallback (`internal/config/config.go:74-79`). Old
  `reranker`/`labeler` JSON blocks are ignored: unknown keys are not an error
  (`config.go:28` comment), so pre-removal configs load cleanly.

The live `docs/embedders.md` already documents the post-relocation paths
(`$XDG_CONFIG_HOME/spoon/spoon.db`, `/var/lib/spoon/spoon.db` on no-home
hosts, `docs/embedders.md:25`). Where any documentation and code disagree,
`internal/store.DefaultPath` is the implementation authority.

## 4. Cache identities and invalidation

There are two different caches:

- **Cluster cache:** keyed by provider/upstream, candidate fingerprint, embedder
  identity, epsilon, minimum size, and relevant pipeline configuration. Changing
  pooling, dimension, model, or feature construction must change the identity.
- **Semantic index:** keyed by `(document_id, model)` plus the body hash. A new
  model can coexist with old vectors; a changed body invalidates only its stale
  rows.
- **FastEmbed model cache:** the provisioned ONNX archive lives under
  `$XDG_CACHE_HOME/spoon/models/fastembed` (override `SPOON_FASTEMBED_CACHE`).
  A corrupt cache is discarded and re-provisioned automatically.

Do not reuse the old OpenVINO embedder's cache identity for FastEmbed. The old
identity included an absolute model path and pooling mode
(`OpenVINOConfig.EmbedderID`, deleted in `031cbc8`); FastEmbed uses the stable
semantic ID documented in `internal/embed/fastembed.go`.

## 5. Adjacent vector consumers

The embedding was not used only for ordinary fork clustering:

- `internal/cluster/classify.go` performs zero-shot category assignment by
  embedding the fork digest and category anchor descriptions, then attaching
  the best category and score. This path is gated on a semantic embedder;
  failures leave the fork uncategorized.
- `PipelineOptions.SiblingSimEnabled` activates the P2 distant-relation
  feature. In `SiblingSimModeUpstreamReadme`, the pipeline performs one
  repository search, fetches roughly 50 README files, embeds them in one batch,
  computes one run-wide maximum cosine, and assigns that value to every fork.
  In `SiblingSimModeForkIntent`, it computes per-fork scores against the
  comparison corpus and bypasses the cluster cache. The two modes therefore
  have different cache and score semantics; do not collapse them into a single
  "each fork's maximum" implementation.
- `CentralityBackend` is adjacent but not itself an embedding feature:
  `directory` uses a cheap touched-path proxy; `mdg` builds a Module
  Dependency Graph from a local/shallow clone and falls back unless strict mode
  is requested.
- `internal/eval` contains deterministic HCA/novelty and momentum policies;
  those consume cluster/heat scores rather than model weights, so removing an
  embedder does not automatically remove those evaluation surfaces.

The `--query` scorer deliberately bounds its digest (`queryDigestMaxChars` is
2,000 in `internal/forksops/stream.go`) and uses commits plus paths. The
multimodal embedding path caps modality text at 16,000 characters and the
persistent semantic document uses a 2,000-character default diff window (see
`internal/embed/features.go` and `internal/semantic/semantic.go`). These caps
are part of the model-quality contract and must be frozen during a model
comparison.

## 6. Fallback boundaries

- FastEmbed unavailable: `cmd/spn/forks.go` emits `embed_unavailable`, skips
  semantic indexing, and lets clustering use lexical mode.
- `--no-embed`/`SPOON_NO_EMBED=1`: no semantic embedder is constructed; lexical
  clustering can still run.
- Interactive TUI: lexical clustering is intentional and does not require
  ONNX Runtime.
- `--query` scoring: always lexical in production (`opts.QueryScorer` nil →
  `LexicalQueryScorer`). There is no configured-but-unloadable scorer state
  anymore; the old "configured reranker fails to load → hard error" boundary
  vanished with `19dd9f5`. A custom scorer can still be injected through the
  surviving `embed.QueryScorer` seam (tests exercise this).
- Cluster labels: always heuristic (`HeuristicLabel`). The LLM polish layer and
  its heuristic-fallback-on-error semantics no longer exist.
