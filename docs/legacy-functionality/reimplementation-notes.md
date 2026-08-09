# Reimplementation notes

How to restore the features removed in `19dd9f5` (reranker, GenAI labeler,
model registry) — and, at lower priority, the earlier removals. Every deleted
source is recoverable via `git show <parent-sha>:<path>`; the full behavioral
contracts live in [reranker-labeler.md](reranker-labeler.md) and
[embedders.md](embedders.md).

## 1. Query reranker — seam survives, implementation does not

**Effort: lowest of the removed model features.** The injection point was
deliberately left intact:

1. `embed.QueryScorer` (`internal/embed/queryscore.go`) and the
   `opts.QueryScorer` branch in `internal/forksops/stream.go:scoreQuery` both
   survive and are test-covered (`internal/forksops/stream_test.go`).
2. Restore the implementation from `19dd9f5^`:
   `internal/embed/rerank.go`, `rerankconfig.go`, `pairtemplate.go`, plus the
   shared runtime they need — `openvino.go` (residual runtime/FFI helpers),
   `ovffi.c`, `ovffi.h`, `ovload.go`, `ovshared.go`.
3. Rewire the constructor in `cmd/spn/forks.go`; the deleted
   `newQueryScorer` (`19dd9f5^:cmd/spn/forks.go:1232`) is the reference:
   `$SPOON_OPENVINO_RERANKER` → `reranker.modelPath` config, device coalesce,
   hard error on a configured-but-unloadable model.
4. Provision a cross-encoder model in OVMS layout
   (`openvino_model.xml` + `openvino_tokenizer.xml` + `config.json` +
   `tokenizer.json`). The registry that used to do this is gone (§3), so
   either restore it or place the model directory by hand and point config at
   it.

**Decisions to make explicitly** (contracts from
[reranker-labeler.md](reranker-labeler.md) §1):

- keep the single-truncated-row divergence from OVMS `/v3/rerank` chunking,
  or implement chunking and re-run the evaluation gate;
- keep `"openvino"` as the `queryMethod` label or rename it — if renamed, fix
  the stale default at `stream.go:1062` in the same change;
- preserve `[0,1]` sigmoid scores and per-doc score order so downstream
  sorting and tests stay valid.

## 2. GenAI labeler — seam was deleted with the feature

**Effort: higher.** Unlike the reranker, the labeler's extension point did not
survive `19dd9f5`. Restoration requires rebuilding, in order:

1. **Runtime package:** `internal/genai/genai.go` (chat-history pipeline over
   `libopenvino_genai_c.so`, dlopen search `$SPOON_OPENVINO_GENAI_LIB` →
   `/opt/intel/openvino-genai/lib/…` → bare soname, `libm.so.6` preload),
   `config.go` (defaults: `GPU`, 24 max tokens, OV compile cache), `labeler.go`
   (system/user prompts, `CleanLabel` 60-rune cap). All from `19dd9f5^`.
2. **Cluster interface:** the deleted `cluster.LabelPolisher` interface and
   `PolishHint` type, plus the `PipelineOptions` field and step-8a wiring in
   `internal/cluster/pipeline.go` (reference:
   `git show 19dd9f5^:internal/cluster/pipeline.go`).
3. **Config surface:** a `labeler.modelPath` / `labeler.device` block and the
   `SPOON_OPENVINO_LABELER` env var. Note: `internal/config/config.go` already
   tolerates unknown JSON keys, so pre-removal configs with a `labeler` block
   load today — they are merely inert until the block is parsed again.
4. **Constructor wiring** in `cmd/spn/forks.go` and `cmd/spoon/main.go`
   (both constructors deleted; references at
   `19dd9f5^:cmd/spn/forks.go:1252` and `19dd9f5^:cmd/spoon/main.go`).

**Non-negotiable baseline:** `HeuristicLabel` (`pipeline.go:762,781`) is the
working label path. A restored polisher must coexist exactly as before —
heuristic first, polish per non-noise cluster, heuristic retained on any
polish error. Do not make the LLM a hard dependency of the pipeline.

## 3. Model-download registry — restore only if §1/§2 do

`internal/models/models.go` (`19dd9f5^`, 257 lines) was a pure-Go HF
downloader with progress/resume, consent gating, `SPOON_FETCH_*` limits, and
the defaults table (`OpenVINO/bge-reranker-base-fp16-ov` 560 MB;
`OpenVINO/Qwen2.5-1.5B-Instruct-int4-ov` 1.1 GB; historical embedder
`OpenVINO/bge-base-en-v1.5-fp16-ov` 440 MB). Restore it only when two or
more features need provisioned models again; otherwise hand-placed model
directories suffice. FastEmbed's pinned-archive provisioning
(`internal/embed/fastembed_provision.go`) is the template for a simpler
single-model alternative and must not be folded back into the registry.

## 4. Lower-priority restorations (earlier removals)

- **OpenVINO model embedder** (deleted `031cbc8`, sources at `031cbc8^`:
  `openvino.go`, `ovconfig.go`, `pooling.go`): it was already 100%
  unreachable when removed (`validBackends` rejected `openvino`). Restoring it
  is a product decision, not a bugfix — and must clear the
  [evaluation.md](evaluation.md) §5 revalidation checklist against the
  FastEmbed baseline before persistence is re-enabled (new model identity,
  dimension, pooling = full index rebuild).
- **External services** (deleted `355dcd8`, sources at `355dcd8^`:
  `internal/embed/{ollama,openai,sidecar}.go`, bootstrap/detect/preflight,
  Python sidecar under `embed/sidecar/`): three historical integration
  styles. Restore only with a concrete need (e.g., a model FastEmbed cannot
  serve), and expect to rebuild the backend-selection surface that `45601e4`
  deliberately narrowed.

## 5. Ground rules for any restoration

- Preserve the surviving contracts in [reranker-labeler.md](reranker-labeler.md) §4:
  score count/order, `[0,1]` ranges, lexical and heuristic fallbacks, cleanup,
  no build-time SDK dependency.
- The `documents`/`embeddings` schema and hash-join semantics
  ([architecture-and-data-flow.md](architecture-and-data-flow.md) §3) must not
  change for a scorer-only restoration; they *must* be revisited if the
  semantic embedder itself is swapped.
- Fix the stale code text listed in [current-state.md](current-state.md) §4 as
  part of the same change that reintroduces the feature it describes.
