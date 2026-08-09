# Removal Log

Chronological log of features removed or displaced from the codebase, most
recent last. Re-baselined at HEAD `426049c` (`fix/export-compare-url-and-created-at`),
2026-08-08.

## 2026-08-08 — OpenVINO reranker and labeler removed entirely (`19dd9f5`)

Commit `19dd9f5` — "refactor: remove OpenVINO reranker and labeler entirely".
The commit message's own summary: *"Embedding is fastembed + lexical, full
stop."*

### Removed

| Feature | Files deleted | Contract |
|---|---|---|
| In-process OpenVINO cross-encoder reranker | `internal/embed/rerank.go`, `rerankconfig.go`, `pairtemplate.go` (+ test), `rerank_integration_test.go` | [reranker-labeler.md](reranker-labeler.md) |
| Shared OpenVINO runtime/FFI support (reranker dependency) | `internal/embed/openvino.go` (residual 227 lines), `ovffi.c`, `ovffi.h`, `ovload.go`, `ovshared.go` | [reranker-labeler.md](reranker-labeler.md) |
| GenAI LLM labeler (labels, categories, summaries) | `internal/genai/genai.go`, `internal/genai/labeler.go`, `internal/genai/config.go` (+ tests) | [reranker-labeler.md](reranker-labeler.md) |
| Model registry + downloader | `internal/models/models.go` (+ tests) | [reimplementation-notes.md](reimplementation-notes.md) |
| `LabelPolisher` pipeline seam | field removed from `internal/cluster/pipeline.go` `PipelineOptions` | [reranker-labeler.md](reranker-labeler.md) |
| `Reranker` / `Labeler` / `ModelConfig` config blocks | `internal/config/config.go` | [current-state.md](current-state.md) |
| Reranker/labeler eval legs | `internal/embed/eval_models_test.go` changed +5/−96; `internal/genai/eval_labelers_test.go` deleted | [evaluation.md](evaluation.md) |

### Retired environment variables

`SPOON_OPENVINO_*` (model dir / URL / SHA256 / timeout / cache),
`SPOON_EVAL_RERANKERS`, `SPOON_EVAL_LABELERS`, `SPOON_FETCH_*` (downloader
limits). Verified absent from non-test sources at HEAD.

### Survived the removal (still in tree at HEAD)

- `embed.QueryScorer` interface and `LexicalQueryScorer`
  (`internal/embed/queryscore.go`) — the seam stays injectable; production
  `cmd/spn` leaves it nil, so `--query` scoring is lexical.
- The `method` column on search results — but see the stale `"openvino"`
  default label flagged in [reranker-labeler.md](reranker-labeler.md).
- `HeuristicLabel` deterministic cluster labeling (`internal/cluster/labels.go`).

### Stale text left behind by the removal

Flagged here rather than hidden:

- `cmd/spn/main.go:114` — help still says *"cross-encoder reranker when
  configured, lexical fallback otherwise"*.
- `cmd/spn/main.go:117` — help still says *"a configured labeler polishes"*.
- `internal/embed/queryscore.go:5-7` — package comment still names the OpenVINO
  reranker as a `QueryScorer` implementation.
- `internal/forksops/stream.go` — when an injected `QueryScorer` is non-nil the
  result `method` is labeled `"openvino"`, which no longer corresponds to any
  implementation.

## 2026-08-08 — Zero-config + store/config relocation series

`2f1560a`, `7d56911`, `b425566`, `9abb5dc` (2026-08-08). Not a feature removal:
configuration and the persistent store moved so that `spn` works with zero
setup. Current layout (verified at HEAD, `internal/store/store.go:173-205`,
`internal/config/config.go`):

| What | Where now | Notes |
|---|---|---|
| Persistent store | `$XDG_CONFIG_HOME/spoon/spoon.db` (default `~/.config/spoon/spoon.db`) | one-time migration moves a legacy `$XDG_DATA_HOME/spoon/spoon.db` if present |
| Store, no-home hosts | `/var/lib/spoon/spoon.db` | fallback when `$HOME` is empty |
| Config file | `$XDG_CONFIG_HOME/spoon/config.json`, `/etc/spoon` fallback | unknown JSON keys are ignored — old `reranker`/`labeler` blocks are silently inert |
| FastEmbed model cache | `$XDG_CACHE_HOME/spoon/models/fastembed` | overridable via `SPOON_FASTEMBED_CACHE` |

## Earlier removals (historical, preserved)

| Commit | Date | Removed / displaced | Successor | Contract doc |
|---|---|---|---|---|
| `355dcd8` | 2026-06-12 | External ML services: OpenAI/Ollama API clients, Python embedding sidecar (`embed/sidecar/`) | in-process OpenVINO | [embedders.md](embedders.md) |
| `ac0dcf0` | 2026-06-14 | OpenVINO as the only embedder (introduced the builtin/lexical embedder) | builtin default, openvino opt-in | [embedders.md](embedders.md) |
| `45601e4` | 2026-07-12 | FastEmbed as sole embedder (default, opt-out); backend selection narrowed | `fastembed` | [embedders.md](embedders.md) |
| `031cbc8` | 2026-07-26 | OpenVINO model embedder (kept fastembed + lexical) | FastEmbed | [embedders.md](embedders.md) |

## Current state (HEAD `426049c`)

Full detail in [current-state.md](current-state.md). Summary:

- **Embedding:** FastEmbed is the sole user-selectable persistent / semantic /
  model-backed backend (`fastembed:fast-bge-small-en-v1.5:maxlen=512:prompts=bge`,
  384 dims). The `builtin`/`lexical` `LocalEmbedder` remains for internal
  callers (query scoring, clustering fallback, `SPOON_NO_EMBED`).
- **Query scoring (`--query`):** lexical only in production —
  `LexicalQueryScorer` embeds query + documents in one batch and uses cosine
  similarity clamped to `[0,1]`. No decay factor.
- **Labels:** deterministic `HeuristicLabel` only.
- **Model downloads:** the generic `internal/models` registry/downloader
  is gone; FastEmbed's dedicated provisioning (pinned archive download with
  optional SHA-256 verification) remains.
- **Store:** libsql at `$XDG_CONFIG_HOME/spoon/spoon.db` with legacy-data
  migration.
