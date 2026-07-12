# Spoon's embedders

Spoon runs every model-backed feature **in-process** — no external service, no
model server. There is one embedder, **fastembed**, plus a zero-setup lexical
fallback and two optional OpenVINO-backed features (a `--query` reranker and an
LLM label polisher).

| Feature | What it does | Model (default) | Runtime |
|---|---|---|---|
| Semantic embedder | persistence, `spn search`, clustering, categories | fastembed `fast-bge-small-en-v1.5` (384-dim) | ONNX Runtime |
| Lexical fallback | clustering when fastembed is unavailable / in the TUI | built-in (deterministic) | none |
| Query reranker | `spn forks list --query "intent"` relevance | bge-reranker-base | OpenVINO (`libopenvino_c`) |
| Label polish | LLM rewrites cluster labels into natural titles | Qwen2.5-1.5B-int4 | OpenVINO GenAI (`libopenvino_genai_c`) |

## FastEmbed — the embedder

FastEmbed is the fixed `fast-bge-small-en-v1.5` BGE model (384 dimensions, max
length 512) run in-process through native Go and ONNX Runtime. Its semantic
identity is `fastembed:fast-bge-small-en-v1.5:maxlen=512` (used as the
embeddings' cache/model key). Models cache under
`$XDG_CACHE_HOME/spoon/models/fastembed`.

It is the **default** on every `spn forks list` — opt-out, not opt-in — and
powers:

- **Persistence + semantic index** — after each run, new or changed fork
  documents are embedded and stored in the durable SQLite store
  (`$XDG_DATA_HOME/spoon/spoon.db`).
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
- The durable store is likewise best-effort: a locked DB, full disk, or
  read-only data dir yields a `store_unavailable` warning, not a failed run.

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
against the query and output is sorted by relevance; each NDJSON record gains
`queryScore` (0..1) and `queryMethod` ("openvino" when the cross-encoder
reranker is configured, "lexical" otherwise). The reranker reimplements OVMS's
`/v3/rerank`: pairs are framed with the tokenizer's own pair template (parsed
from tokenizer.json), scored by the cross-encoder, and squashed with a sigmoid.
This is distinct from `spn search`, which is vector-similarity retrieval over
the persistent fastembed index.

## Cluster label polish

With a labeler configured (and the openvino-genai runtime loadable), each
cluster's heuristic label is rewritten by a small instruct LLM running
in-process via OpenVINO GenAI — greedy decoding, ≤24 new tokens, the model's own
chat template. Errors silently keep the heuristic label. Example:
`markdown/  ·  grammar, upstream, tests` → "Go Tree-sitter Grammar Updates".

## OpenVINO runtime (reranker + labeler)

The `--query` reranker and the label polisher load the OpenVINO runtime at
**run time** via `dlopen` (no build tags), so a default build needs no OpenVINO
SDK and stays portable — those features simply report unavailable (and `--query`
falls back to lexical scoring) when the libraries are absent. cgo is still
required, as elsewhere in spoon.

To run them, install the runtime (`openvino` + `openvino-intel-gpu-plugin`, and
`openvino-genai` + `libopenvino_tokenizers.so` for the labeler). spoon loads
`libopenvino_c.so` / `libopenvino_genai_c.so` from the openvino-genai prefix
(`/opt/intel/...` on Arch) and then the ldconfig path; override with
`SPOON_OPENVINO_LIB` / `SPOON_OPENVINO_GENAI_LIB`. `spoon setup` downloads the
default reranker/labeler models (pure-Go HuggingFace download, consent-gated;
`--auto-pull` to skip the prompt). On Intel Arc the first GPU load JIT-compiles
kernels; spoon caches them under `~/.cache/spoon/openvino` so subsequent loads
take ~1 s.

## Building

```sh
go build ./cmd/spoon ./cmd/spn   # all features; no build tags
```

FastEmbed links ONNX Runtime; the reranker/labeler `dlopen` OpenVINO at run
time. Neither needs an SDK at build time beyond cgo.

## Choosing models

The reranker and labeler defaults were re-validated on 2026-06-12 against
pre-converted alternatives from the OpenVINO HF org, on the hand-labeled
behavioral-embeddings dataset (53 same/different-intent pairs over real PR
feature sets; harnesses: `TestEval*_Manual` in internal/embed and
internal/genai):

| Feature | Default (kept) | Challenger | Result |
|---|---|---|---|
| Reranker | bge-reranker-base-fp16 | Qwen3-Reranker-0.6B-seq-cls-fp16 | acc@1 0.63 / MRR 0.77 in 1.7 s |
| Labeler | Qwen2.5-1.5B-Instruct-int4 | Qwen3-0.6B-int4 | 3/3 good labels @109 ms warm vs 0/3 (thinking mode eats the token budget) |

The dataset is small (CI ≈ ±0.16 on AUC), so only clear wins justify a default
switch; none of the challengers produced one. The semantic embedder is fixed at
fastembed `fast-bge-small-en-v1.5` and is not user-selectable.

## History

Earlier versions delegated embedding to external services (Ollama, a Python
sidecar serving arctic-embed-l-v2.0, or any OpenAI-compatible endpoint), then to
an in-process OpenVINO encoder. Both are gone: the semantic embedder is now
fastembed (a fixed BGE model over ONNX Runtime), which gives stable,
cross-run-comparable vectors — the prerequisite for the persistent SQLite index
and `spn search`. The built-in lexical embedder remains as the zero-setup
clustering fallback. The evaluation that informed earlier model choices is
preserved under `experiments/started/behavioral-embeddings/`.
