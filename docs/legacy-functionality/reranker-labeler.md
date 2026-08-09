# Reranker & Labeler — Removed in `19dd9f5`

**Status:** both features were deleted in commit `19dd9f5` ("refactor: remove
OpenVINO reranker and labeler entirely", 2026-08-08). This document preserves
their full contracts — re-baselined from the pre-removal version of this file
and verified against the parent sources (`git show 19dd9f5^:<path>`) — for
anyone re-implementing them.

Deleted sources (line counts at `19dd9f5^`):

| Feature | Deleted files |
|---|---|
| OpenVINO cross-encoder reranker | `internal/embed/rerank.go` (326), `rerankconfig.go` (72), `pairtemplate.go` (214) |
| Shared OpenVINO runtime/FFI the reranker depended on | `internal/embed/openvino.go` (residual 227: tokenizer/compile/tensor helpers), `ovffi.c`, `ovffi.h`, `ovload.go`, `ovshared.go` |
| GenAI LLM labeler | `internal/genai/genai.go` (351), `labeler.go` (93), `config.go` (57) |
| Model registry | `internal/models/models.go` (257) |
| Pipeline seam | `LabelPolisher` field in `internal/cluster/pipeline.go` `PipelineOptions` |

## What survived at HEAD

The query-scoring seam survived intact; only the OpenVINO implementation is
gone:

- `embed.QueryScorer` interface (`internal/embed/queryscore.go`):
  `Rerank(ctx, query, docs) ([]float64, error)` — one score in `[0,1]` per doc.
- `embed.LexicalQueryScorer` — embeds query + documents in **one
  corpus-consistent batch** via `LocalEmbedder{}.Embed`, scores by cosine
  similarity (dot product of normalized vectors) clamped to `[0,1]`. There is
  **no exponential decay factor**; that was never the behavior.
- `internal/forksops/stream.go:1060` `scoreQuery` — uses `opts.QueryScorer` if
  non-nil, else `LexicalQueryScorer`. Production `cmd/spn/forks.go` leaves the
  seam nil (comment: *"Query relevance uses the built-in lexical scorer
  (opts.QueryScorer nil)."*), so **`--query` is always lexical in production**.
  The seam remains injectable and test-covered (`internal/forksops/stream_test.go`).
- `queryDigest` (`stream.go:1098`) — each fork is reduced to commit subjects
  then touched paths, capped at `queryDigestMaxChars = 2000` runes.

### ⚠ Stale code text at HEAD (flagged, not hidden)

| Location | Stale text | Reality |
|---|---|---|
| `internal/forksops/stream.go:1062` | `method := "openvino"` as the default label for a non-nil injected scorer | No OpenVINO scorer exists; misleading default, not dead code |
| `internal/embed/queryscore.go:5-7` | "Implemented by the OpenVINO cross-encoder Reranker and by LexicalQueryScorer" | Only `LexicalQueryScorer` remains |
| `cmd/spn/main.go:114` | "cross-encoder reranker when configured, lexical fallback otherwise" | Lexical only |
| `cmd/spn/main.go:117` | "a configured labeler polishes" | No labeler exists; `HeuristicLabel` only |

---

## 1. Query reranker (removed contract)

Sources: `19dd9f5^:internal/embed/rerank.go`, `rerankconfig.go`,
`pairtemplate.go`, `ovload.go`, `ovshared.go`; caller
`19dd9f5^:cmd/spn/forks.go:newQueryScorer`.

### Public behavior and caller

`newQueryScorer` checked, in precedence order:

1. `$SPOON_OPENVINO_RERANKER` for the model directory;
2. `config.json`'s `reranker.modelPath`;
3. `$SPOON_OPENVINO_DEVICE`, then `reranker.device` for the device.

With no model configured, it returned no scorer and the stream used
`embed.LexicalQueryScorer`. With a configured model, it constructed
`embed.NewReranker` and returned its `Close` function. A configured but
unloadable reranker was an error rather than a silent quality downgrade.

For `spn forks list --query <text>`, each fork's bounded change digest (commit
subjects plus touched paths) was scored and output received `queryScore` and
`queryMethod` (`"openvino"` or `"lexical"`). This was distinct from
`spn search`, which embeds a query and performs vector retrieval over the
persistent FastEmbed index.

### Model layout and defaults

`RerankConfig` expected an OVMS-style directory:

```text
openvino_model.xml (+ .bin)       # cross-encoder
openvino_tokenizer.xml (+ .bin)   # tokenizer custom ops
config.json                       # optional max_position_embeddings
tokenizer.json                    # pair template
```

Both XML files were validated before runtime initialization. Defaults:

| Field | Default |
|---|---|
| `Device` | `"GPU"` (tokenizer always `"CPU"`) |
| `MaxTokens` | `config.json.max_position_embeddings`, else 512 (`defaultOVMaxTokens`) |
| `MaxBatch` | 8 pairs (`defaultRerankMaxBatch`) |
| `TokenizersLib` | `ResolveTokenizersLib()` |
| `CacheDir` | `$XDG_CACHE_HOME/spoon/openvino` or `~/.cache/spoon/openvino` |

The default provisioned model was `OpenVINO/bge-reranker-base-fp16-ov`
(`internal/models/models.go` at the parent). The repository's
model-evaluation comment identified the default as `bge-reranker-base-fp16`;
the 2026-06-12 comparison retained it over
`Qwen3-Reranker-0.6B-seq-cls-fp16`.

`RerankConfig.RerankerID` was an absolute-path identity
(`"openvino:" + abs(ModelPath)`) for metadata, not a portable model-version
identity; a future persistent cache should use a content/model revision as
well.

### Pair construction

`pairtemplate.go` parsed the converted model's `tokenizer.json` pair template
rather than hard-coding a tokenizer's special-token layout. The reranker:

1. tokenized `[query, document...]` through the converted OpenVINO tokenizer;
2. stripped the single-text wrappers from each token sequence;
3. assembled each row using the model template, conceptually
   `BOS query EOS SEP document EOS` for the BGE model;
4. **rejected an overlong query** (hard error: "shorten the query") and
   truncated assembled rows to max context;
5. padded each batch to a rectangular `[batch, sequence]` tensor.

This was a deliberate compatibility divergence from OVMS `/v3/rerank`: OVMS
chunks long documents and max-aggregates the chunk scores, while Spoon kept a
single truncated row. Fork digests are bounded enough that truncation was
considered acceptable, but a faithful re-implementation must either preserve
this divergence explicitly or implement OVMS-style chunking and update its
quality/evaluation contract.

The converted cross-encoder received `input_ids` and `attention_mask`. If its
compiled input names included `token_type_ids`, the implementation supplied an
all-zero tensor, matching OVMS's behavior for these models. Rows ran in
batches of at most eight.

The output was `logits`; with one class, index 0 was used; with multiple
classes, index 1 is the positive class. Each logit was squashed by a sigmoid,
exactly as OVMS computes it:

```text
score = 1 / (1 + exp(-logit))
```

The result: one score in `[0,1]` per document. Calls were serialized under a
mutex because the implementation reused OpenVINO infer requests; batching
loops honored `ctx.Err()` between batches. `Close` released tokenizer/encoder
requests, compiled models, and the OpenVINO core in order.

### Runtime loading (dlopen)

`ovload.go` and `ovffi.c/.h` made OpenVINO a runtime dependency instead of a
build-time dependency. `libopenvino_c.so` candidates, in order:

1. `$SPOON_OPENVINO_LIB`, if set;
2. `/opt/intel/openvino-genai/lib/libopenvino_c.so` (Arch's `/opt/intel`
   prefix);
3. `libopenvino_c.so` through the dynamic loader.

`libopenvino_tokenizers.so` search path (`ovshared.go`
`tokenizersLibSearchPath`), in order, falling back to the bare soname:

```text
/usr/lib/libopenvino_tokenizers.so
/usr/lib64/libopenvino_tokenizers.so
/opt/intel/openvino-genai/lib/libopenvino_tokenizers.so
/usr/lib/ovms/lib/libopenvino_tokenizers.so
```

`sync.Once` made the probe process-wide. The vendored C ABI wrapper used
`dlopen`/`dlsym` and opaque OpenVINO handles; the Cgo bridge was linked only
with `-ldl`. This kept normal Go builds portable while permitting runtime
fallback when OpenVINO was absent. `OpenVINOAvailable()` was a runtime probe,
not a build-time flag.

The original OpenVINO implementation was introduced under build tags by
`355dcd8`; `806c4f6` ("Load OpenVINO at runtime via dlopen; drop 'openvino
genai' build tags", 2026-06-14) removed the tags and stub files, added lazy
runtime loading, and documented the `libm.so.6` preload needed to avoid Intel
oneAPI IFUNC relocation crashes during `dlopen`.

## 2. GenAI cluster-label polisher (removed contract)

Sources: `19dd9f5^:internal/genai/genai.go`, `labeler.go`, `config.go`;
seam `19dd9f5^:internal/cluster/pipeline.go`, `labels.go`.

### Caller and fallback

`cmd/spn/forks.go:newLabelPolisher` (and the equivalent constructor in
`cmd/spoon/main.go`) resolved:

- `$SPOON_OPENVINO_LABELER`, then `config.json`'s `labeler.modelPath`;
- `labeler.device` (default `GPU`).

No configured model meant no polisher. `cluster.RunPipeline` generated a
heuristic label first, then called the polisher for every non-noise cluster
(pipeline step 8a). Errors retained the heuristic label with a
`[cluster] label polish failed` log line — the heuristic was the correctness
fallback and the LLM was presentation polish.

### Model and generation contract

The default registry entry was `OpenVINO/Qwen2.5-1.5B-Instruct-int4-ov`
(~1.1 GB). The model directory had to contain `openvino_model.xml`.
`genai.Config` defaults: `GPU`, `MaxNewTokens` 24 (labels are short), compile
cache `~/.cache/spoon/openvino` (`XDG_CACHE_HOME` aware) — the same OpenVINO
cache as the reranker.

`labeler.go` supplied:

- system prompt: title GitHub repository-fork clusters, reply with the title
  only — 3 to 7 plain words, no quotes, no trailing punctuation, no
  explanations;
- user prompt: upstream repo, heuristic **keyword hints** (the `dir/  ·  a, b`
  separator flattened — models otherwise copy the heuristic formatting
  verbatim), up to six sample commit subjects and touched paths, then an
  explicit natural-English title request;
- greedy decoding via the chat-history API (the model's own chat template from
  `tokenizer_config.json` applies) with a tight token budget;
- output cleanup (`CleanLabel`): first line only, trim quote/backtick
  wrappers and trailing punctuation, cap at **60 runes** cut at a word
  boundary.

`genai.go` created a chat history with system/user JSON messages, passed it
through the OpenVINO GenAI chat-template pipeline, and serialized concurrent
generation calls. It loaded `libopenvino_genai_c.so` lazily, candidates in
order:

1. `$SPOON_OPENVINO_GENAI_LIB`;
2. `/opt/intel/openvino-genai/lib/libopenvino_genai_c.so`;
3. `libopenvino_genai_c.so`.

`libm.so.6` was preloaded (`RTLD_NOW | RTLD_GLOBAL`) before the GenAI dlopen
to avoid the IFUNC relink crash. The C bridge owned the pipeline, generation
config, chat history, and decoded results. No openvino-genai headers were
vendored.

### Model selection evidence (historical)

`internal/genai/eval_labelers_test.go` (deleted in `19dd9f5`) supported manual
comparisons through `SPOON_EVAL_LABELERS`. The docs recorded the retained
Qwen2.5-1.5B-int4 labeler versus Qwen3-0.6B-int4: three representative labels
were good at ~109 ms warm for the retained model, while the challenger
produced no acceptable labels because thinking output consumed the short
token budget. The sample is small; treat this as a historical default choice,
not a permanent benchmark conclusion.

## 3. Setup and model registry (removed)

`internal/models/models.go` (deleted in `19dd9f5`) maintained public OpenVINO
Hugging Face defaults and pure-Go download/progress/resume logic:

| Feature | Registry repo | Approx. size |
|---|---|---:|
| historical embedder | `OpenVINO/bge-base-en-v1.5-fp16-ov` | 440 MB |
| reranker | `OpenVINO/bge-reranker-base-fp16-ov` | 560 MB |
| labeler | `OpenVINO/Qwen2.5-1.5B-Instruct-int4-ov` | 1,100 MB |

At the pre-removal tip, `cmd/spoon/setup.go` provisioned only the optional
reranker and labeler slots through this registry (FastEmbed has its own ONNX
provisioning), recorded their paths in config, detected OpenVINO
availability/devices, and used consent-gated downloads unless `--auto-pull`
was supplied. The registry and its `SPOON_FETCH_*` download limits are gone
with `19dd9f5`; FastEmbed's dedicated archive provisioning is the only
remaining model download path (see [embedders.md](embedders.md)).

## 4. Reimplementation boundaries

The surviving interface:

```go
type QueryScorer interface {
    Rerank(ctx context.Context, query string, docs []string) ([]float64, error)
}
```

`cluster.LabelPolisher` was **deleted along with the feature** — restoring a
polisher means restoring the interface, `PolishHint`, the `PipelineOptions`
field, and the step-8a wiring in `internal/cluster/pipeline.go` (see
[reimplementation-notes.md](reimplementation-notes.md)):

```go
type LabelPolisher interface {
    PolishLabel(ctx context.Context, hint PolishHint) (string, error)
}
```

Preserve these semantics unless intentionally versioning output:

- same number/order of scores as input docs;
- stable `[0,1]` scores and explicit query-method metadata;
- lexical query fallback when no reranker is configured;
- heuristic label fallback on polish failure;
- explicit model identity/configuration and cleanup;
- no OpenVINO SDK requirement for builds that do not use these features.
