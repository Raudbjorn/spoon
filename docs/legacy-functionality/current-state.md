# Current state at HEAD `426049c`

> **Superseded in part (2026-08-12).** Voyage AI added an optional external
> embedder and reranker alongside fastembed; see the top entry in
> [removal-log.md](removal-log.md) and [../embedders.md](../embedders.md).
> Everything below still describes the default, no-API-key configuration, and the
> claims about `QueryScorer` being unimplemented and `queryMethod` defaulting to
> `"openvino"` no longer hold.

**Authoritative snapshot** of the embedding/scoring/labeling surface after the
`19dd9f5` removal (2026-08-08) and the store/config relocation series. Every
claim below was verified at HEAD; see the sibling docs for contracts of the
removed features.

## 1. Removed vs surviving capability

| Capability | Status at HEAD | Authority |
|---|---|---|
| OpenVINO cross-encoder `--query` reranker | **Removed** (`19dd9f5`) | [reranker-labeler.md](reranker-labeler.md) |
| GenAI LLM cluster-label polisher | **Removed** (`19dd9f5`) | [reranker-labeler.md](reranker-labeler.md) |
| `internal/models` HF registry/downloader | **Removed** (`19dd9f5`) | [removal-log.md](removal-log.md) |
| OpenVINO model embedder | **Removed** (`031cbc8`) | [embedders.md](embedders.md) |
| External Ollama/OpenAI/sidecar embedders | **Removed** (`355dcd8`) | [embedders.md](embedders.md) |
| `embed.QueryScorer` seam | **Survives** — injectable, test-covered | `internal/embed/queryscore.go` |
| `LexicalQueryScorer` | **Survives** — the only scorer in production | `internal/embed/queryscore.go` |
| FastEmbed semantic embedder | **Survives** — sole persistent/semantic backend | `internal/embed/fastembed.go` |
| Built-in lexical embedder | **Survives** — internal clustering/degradation | `internal/embed/local.go` |
| Multimodal fork-feature weighting | **Survives** | `internal/embed/multimodal.go` |
| Heuristic cluster labels | **Survives** — now the *only* label path | `internal/cluster/pipeline.go:762,781` |

## 2. Production behavior

### `--query` scoring (always lexical)

`internal/forksops/stream.go:1060` `scoreQuery`: when `opts.QueryScorer` is
nil (the production default — `cmd/spn` never sets it), scoring uses
`embed.LexicalQueryScorer{}` and reports `queryMethod = "lexical"`. Semantics
(verified in `queryscore.go`):

- query + all fork digests are embedded in **one corpus-consistent batch**
  through `LocalEmbedder`;
- each score is the dot product of the L2-normalized query/doc vectors
  (cosine similarity), **clamped to `[0,1]`**;
- there is **no exponential decay** — never was;
- each fork's digest (`queryDigest`, commits then paths) is capped at
  `queryDigestMaxChars = 2000` runes (`stream.go:1098`).

Custom scorers can still be injected through `opts.QueryScorer`; tests do.
⚠ `stream.go:1062` labels any injected scorer's method as `"openvino"` by
default — stale and misleading, not dead code.

### Semantic indexing and search

- FastEmbed identity:
  `fastembed:fast-bge-small-en-v1.5:maxlen=512:prompts=bge`
  (`internal/embed/fastembed.go:23`). **384 dims, max length 512**, BGE v1.5
  passage/query prompt convention, ONNX Runtime backend.
- Cache: `$XDG_CACHE_HOME/spoon/models/fastembed`; override
  `SPOON_FASTEMBED_CACHE`. Corrupt cache is discarded and re-provisioned.
- `spn search`: embeds the query, loads `store.SearchRows`, decodes
  little-endian float32 vectors, ranks by `semantic.Cosine`.
- Failures degrade: `embed_unavailable` warning → lexical clustering only;
  `--no-embed`/`SPOON_NO_EMBED=1` intentionally takes the same path.

### Cluster labels

Heuristic only (`HeuristicLabel`). No LLM polish exists; restoring one
requires rebuilding the deleted pipeline seam — see
[reimplementation-notes.md](reimplementation-notes.md).

## 3. Store, config, cache

| Thing | Location | Notes |
|---|---|---|
| Database | `$XDG_CONFIG_HOME/spoon/spoon.db` (default `~/.config/spoon/spoon.db`) | `internal/store/store.go:173-205` |
| No-home DB fallback | `/var/lib/spoon/spoon.db` | mandatory store; fails loudly if unwritable |
| Legacy DB migration | from `$XDG_DATA_HOME/spoon/spoon.db` | one-time move by `OpenDefault` (`migrateLegacyDB`) |
| Config | `$XDG_CONFIG_HOME/spoon/config.json` | `internal/config/config.go:74-79` |
| No-home/admin config | `/etc/spoon/config.json` | old `reranker`/`labeler` JSON blocks are ignored (unknown keys tolerated) |
| FastEmbed model cache | `$XDG_CACHE_HOME/spoon/models/fastembed` | `SPOON_FASTEMBED_CACHE` override |

Relocation series: `2f1560a`, `7d56911`, `b425566`, `9abb5dc`.

## 4. Stale code text at HEAD (flagged for cleanup)

| Location | Text | Reality |
|---|---|---|
| `internal/forksops/stream.go:1062` | `method := "openvino"` default | no OpenVINO scorer exists |
| `internal/embed/queryscore.go:5-7` | "Implemented by the OpenVINO cross-encoder Reranker and by LexicalQueryScorer" | only `LexicalQueryScorer` remains |
| `cmd/spn/main.go:114` | "cross-encoder reranker when configured, lexical fallback otherwise" | lexical only |
| `cmd/spn/main.go:117` | "a configured labeler polishes cluster labels with an in-process LLM" | no labeler; heuristic only |
| `docs/embedders.md:16` | short model ID without `:prompts=bge` | code ID is the full form |

## 5. Environment variable census

Active embedding-relevant: `SPOON_FASTEMBED_MODEL`, `SPOON_FASTEMBED_CACHE`,
`SPOON_FASTEMBED_SHA256`, `SPOON_NO_EMBED`, `ONNX_PATH`, plus
`SPOON_EVAL_EMBEDDERS` (manual comparison gate).

Dead (consumed only by deleted code): `SPOON_OPENVINO_RERANKER`,
`SPOON_OPENVINO_LABELER`, `SPOON_OPENVINO_DEVICE`, `SPOON_OPENVINO_LIB`,
`SPOON_OPENVINO_GENAI_LIB`, `SPOON_EVAL_RERANKERS`, `SPOON_EVAL_LABELERS`,
`SPOON_FETCH_*`, and the pre-`355dcd8` external-service vars
(`SPOON_EMBEDDER_URL`, `SPOON_EMBEDDER_MODEL`, `SPOON_OPENAI_BASE_URL`,
`OPENAI_API_KEY`, `SPOON_SIDECAR_MODEL`, `SPOON_SIDECAR_DEVICE`).

See [embedders.md](embedders.md) §4b for the full census with line references.
