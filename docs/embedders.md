# Spoon's embedders

Spoon runs every model-backed feature **in-process** — no external service, no
model server. There is one embedder, **fastembed**, plus a zero-setup lexical
fallback.

| Feature | What it does | Model (default) | Runtime |
|---|---|---|---|
| Semantic embedder | persistence, `spn search`, clustering, categories | fastembed `fast-bge-small-en-v1.5` (384-dim) | ONNX Runtime |
| Lexical fallback | clustering when fastembed is unavailable / in the TUI; `--query` relevance | built-in (deterministic) | none |

## FastEmbed — the embedder

FastEmbed is the fixed `fast-bge-small-en-v1.5` BGE model (384 dimensions, max
length 512) run in-process through native Go and ONNX Runtime. Its semantic
identity is `fastembed:fast-bge-small-en-v1.5:maxlen=512` (used as the
embeddings' cache/model key). Models cache under
`$XDG_CACHE_HOME/spoon/models/fastembed`.

It is the **default** on every `spn forks list` — opt-out, not opt-in — and
powers:

- **Persistence + semantic index** — after each run, new or changed fork
  documents are embedded and stored in the global libsql store
  (`$XDG_CONFIG_HOME/spoon/spoon.db`; `/var/lib/spoon/spoon.db` on no-home
  hosts).
- **`spn search "<query>"`** — cosine ranking over the stored vectors.
- **Clustering** — when fastembed is active it embeds the modality blobs (see
  below); otherwise the lexical fallback does.
- **Zero-shot categories** — the per-fork `category` facet.

### Setup, opt-out, and graceful degradation

FastEmbed needs ONNX Runtime present:

```sh
export ONNX_PATH=/path/to/libonnxruntime.so   # e.g. /usr/lib/libonnxruntime.so
spoon setup                                    # downloads the model, writes config
```

- If ONNX Runtime is **unavailable**, a run degrades: it emits an
  `embed_unavailable` warning and continues without semantic indexing —
  listing, scoring, and (lexical) clustering are unaffected. A missing native
  runtime never fails a run.
- Pass `--no-embed` (or `SPOON_NO_EMBED=1`) to skip embedding entirely.
- The global store, by contrast, is mandatory: it is the cross-invocation
  cache and the persistence layer in one, and a run that cannot open it fails
  loudly rather than running silently uncached.

### Semantic search

```sh
spn forks list owner/repo          # prospect + index (fastembed default)
spn search "oauth rate limiting" --top 20          # cross-repo semantic search
spn search "wayland support" --repo owner/repo     # scoped to one upstream
```

Each list run embeds only new or changed documents (keyed by a content hash of
`modelID + body`) in batches of 32, so re-runs re-embed nothing unchanged.
`spn search` emits deterministic score-descending NDJSON; an empty index is a
successful empty result with a `semantic_index_empty` warning.

## The lexical fallback

When fastembed is not active (no ONNX Runtime, `--no-embed`, or the interactive
`spoon` TUI, which always clusters lexically), clustering uses a deterministic
built-in lexical embedder — zero setup, no downloads, no "embedder unreachable"
failure mode.

For each fork it builds four modality blobs — touched file paths, commit
messages (merges excluded), a README excerpt, and a normalized diffstat chunk —
and embeds each:

- tokens are lowercase alphanumeric runs plus within-line adjacent bigrams (so
  `internal/auth` path structure and "rate limit" commit phrasing carry
  signal);
- weights are sublinear TF × smoothed IDF, with IDF computed over the run's own
  corpus — boilerplate shared by every fork cancels out, fork-distinctive
  tokens dominate;
- token weights fold into a fixed 512-dim vector via signed feature hashing,
  L2-normalized.

The four vectors are concatenated with weights
`[paths 0.3, commits 0.3, readme 0.2, diff 0.2]`. Because its IDF is per-run,
the lexical embedder is only comparable *within* a run — which is why the
persistent index and `spn search` use fastembed (stable across runs), not the
lexical fallback.

## Clustering

Both embedders feed single-link agglomerative clustering at a cosine-distance
cutoff. The default `--cluster-epsilon` is **0.35** when fastembed is active
(dense-encoder cosines separate at a tighter scale) and **0.55** for the lexical
fallback. Cluster labels are heuristic and deterministic: the dominant directory
prefix (up to two levels) plus the top TF-IDF discriminators from member commit
messages, with a path-segment fallback when commit text is unavailable.

```sh
spoon --cluster-epsilon 0.45 golang/go     # tighter clusters
spoon --cluster-min-size 2 golang/go       # allow pairs
spoon --no-cluster golang/go               # skip clustering entirely
spoon --cluster-top 100 golang/go          # embed more forks
```

## Zero-shot categories

With fastembed active, each clustered fork gets a `category` facet (`feature`,
`bugfix`, `security`, `ci-build`, `docs`, `localization`, `dependencies`,
`port`, `config`) assigned by cosine similarity between its change digest and
anchor descriptions — no extra model, one extra embedding batch. Forks matching
no anchor stay uncategorized.

## Query-driven fork search (`--query`)

```sh
spn forks list owner/repo --query "wayland support"
```

Every enriched fork's change digest (commit subjects + touched paths) is scored
against the query by the built-in lexical scorer and output is sorted by
relevance; each NDJSON record gains `queryScore` (0..1) and `queryMethod`
("lexical"). This is distinct from `spn search`, which is vector-similarity
retrieval over the persistent fastembed index.

## Building

```sh
go build ./cmd/spoon ./cmd/spn   # all features; no build tags
```

FastEmbed links ONNX Runtime; nothing needs an SDK at build time beyond cgo
(required for the libsql store and tree-sitter as well).

## History

Earlier versions delegated embedding to external services (Ollama, a Python
sidecar serving arctic-embed-l-v2.0, or any OpenAI-compatible endpoint), then to
an in-process OpenVINO encoder. Both are gone — as are the later OpenVINO
reranker and label-polisher features: the semantic embedder is now fastembed
(a fixed BGE model over ONNX Runtime), which gives stable, cross-run-comparable
vectors — the prerequisite for the persistent index and `spn search`. The
built-in lexical embedder remains as the zero-setup clustering fallback and
`--query` scorer. The evaluation that informed earlier model choices is
preserved under `experiments/started/behavioral-embeddings/`.
