# Embedders: removed and surviving implementations

## Executive summary

Spoon has had four embedding families:

1. external Ollama REST;
2. an external Python FastAPI sidecar serving Snowflake Arctic;
3. generic OpenAI-compatible HTTP endpoints (including OVMS);
4. in-process OpenVINO, followed by in-process FastEmbed/ONNX.

The first three old integration styles and the OpenVINO encoder are no longer
part of the production path. The current semantic embedder is fixed FastEmbed;
the deterministic lexical embedder remains for clustering and degradation.
The source-level history is `355dcd8` (external services → in-process
OpenVINO, 2026-06-12), `45601e4` (FastEmbed sole persistent backend,
2026-07-12), `031cbc8` (OpenVINO embedder deleted, 2026-07-26), and
`19dd9f5` (reranker, GenAI labeler, and `internal/models` registry deleted,
2026-08-08 — generic model downloading is gone).

## 1. Historical external services

### Ollama embeddings

The original `internal/embed/ollama.go` client posted one request per text to:

```text
POST {endpoint}/api/embeddings
{"model":"<model>","prompt":"<text>"}
```

It expected `{"embedding":[...]}`, learned the dimension from the first
successful result, reused an HTTP client with a 30-second timeout, capped each
response at 10 MiB, and handled context-window failures by halving the text
and retrying before returning an error so callers could degrade. The default
endpoint was `http://localhost:11434` (`SPOON_EMBEDDER_URL` override); the
default model was `nomic-embed-text` (`SPOON_EMBEDDER_MODEL` override).

The old ranked model registry (`git show 355dcd8^:internal/embed/embed.go`)
listed:

| Model | Approx. dimension | Role |
|---|---:|---|
| `nomic-embed-text` | 768 | Ollama default |
| `snowflake-arctic-embed2` | 1024 | preferred long/context alternative |
| `snowflake-arctic-embed-l-v2.0` | 1024 | Arctic alias/large model |
| `snowflake-arctic-embed` | 1024 | earlier Arctic model |
| `jina-embeddings-v2-base-code` | 768 | code-aware candidate |

`internal/embed/bootstrap.go` detected Ollama with `GET /api/tags`, matched
installed tags by base model name, optionally prompted before `POST /api/pull`,
and returned structured skip reasons when the endpoint/model was unavailable.
The old `--auto-pull` path bypassed consent.

### OpenAI-compatible endpoint

`git show 355dcd8^:internal/embed/openai.go` implemented an OpenAI embeddings
client for `POST {base}/embeddings`, with `Input: []string` and response data
placed by returned index. It accepted a base such as
`http://localhost:8978`/`https://api.openai.com/v1`, used
`SPOON_OPENAI_BASE_URL` and `OPENAI_API_KEY`, and defaulted the model to
`nomic-ai/nomic-embed-text-v1.5`. A base without `/v1` or `/v3` received an
appended `/v3` for OVMS compatibility, and its default HTTP client timeout was
five minutes. Its health probe used `GET {base}/models` and required the
configured model to be listed.

This path was deliberately generic: it could target a hosted OpenAI API, vLLM,
or OVMS's OpenAI-compatible embeddings surface. It was still an external
network and authentication dependency, not the later in-process OpenVINO path.

### Python sidecar

The removed `embed/sidecar/server.py` loaded
`Snowflake/snowflake-arctic-embed-l-v2.0` through
`sentence_transformers.SentenceTransformer`, with
`trust_remote_code=True`, and used the model's CLS-pooling plus L2-normalization convention. The service
was FastAPI on port `8766` by default:

```text
GET  /health       -> {status, model, device, dim}
POST /embed        -> {texts:[...]} -> {vectors:[[...]], dim}
```

`SPOON_SIDECAR_MODEL` selected the Hugging Face model and
`SPOON_SIDECAR_DEVICE` selected `cpu` or `cuda`. The Go client used
`--embedder-backend sidecar --sidecar-endpoint http://localhost:8766`.
The repository included Docker/systemd installation assets, Python dependency
pins, and a benchmark harness. The sidecar was selected because the Arctic
model's long context was useful for fork/PR change text, but it added a second
process, Python dependencies, service lifecycle, and an HTTP boundary.

### External Ollama labeler

Before the in-process GenAI labeler, `git show 355dcd8^:internal/cluster/llm_labeler.go`
used Ollama `/api/chat`. The default model was `llama3.2:3b`; the labeler could
also use models such as `qwen2.5-coder:7b`. It sent a system prompt requiring a
short factual one-line cluster title, included heuristic/upstream/member
context, capped README/member content, and returned the heuristic label on
errors. Its endpoint and model were configured separately from the embedder.

## 2. Removed in-process OpenVINO embedder

### Model and files

The OpenVINO embedding backend was introduced by `355dcd8` and verified with
`OpenVINO/bge-base-en-v1.5-fp16-ov` (roughly 440 MB, 768 dimensions). It
accepted an OVMS-style model directory containing at least:

```text
openvino_model.xml (+ .bin)
openvino_tokenizer.xml (+ .bin)
config.json                 # optional max_position_embeddings
 graph.pbtxt                 # optional OVMS pooling/normalization metadata
```

The implementation is reconstructable from:

```text
git show 031cbc8^:internal/embed/openvino.go
git show 031cbc8^:internal/embed/ovconfig.go
git show 031cbc8^:internal/embed/pooling.go
```

### Execution pipeline

1. `NewOpenVINOEmbedder` lazily loaded `libopenvino_c.so`, created an OpenVINO
   core, added `libopenvino_tokenizers.so`, and compiled the tokenizer on CPU.
2. It compiled the encoder on `GPU` by default (or `CPU`/`GPU.1`) and used the
   OpenVINO compiled-kernel cache at `~/.cache/spoon/openvino`.
3. The tokenizer returned `input_ids` and `attention_mask`; unsupported or
   missing `token_type_ids` were detected from compiled input names, and
   zero-filled when required.
4. Sequences were truncated to `config.json`'s
   `max_position_embeddings`, otherwise 512; batches defaulted to 16.
5. The encoder's rank-3 output `[batch, seq, hidden]` was selected as the last
   hidden state. The backend rejected models without a matching rank-3 output
   or with non-f32 hidden states.
6. Pooling was selected from `graph.pbtxt` or defaulted to CLS:
   - CLS: first token;
   - MEAN: attention-mask weighted mean;
   - LAST: last attended token.
7. Vectors were L2-normalized by default, matching OVMS's
   `NormalizeL2(axis=1, eps=1e-12, MAX)` behavior. Pooling was part of the
   `EmbedderID` because it changes vector geometry.

`OpenVINOConfig` also accepted explicit `Normalize`, `MaxTokens`, `MaxBatch`,
`TokenizersLib`, and `CacheDir`. Tokenizer and encoder inference requests were
serialized under a mutex and released in `Close`.

### Why it was removed

`45601e4` made FastEmbed the only selectable persistent/semantic backend and
rewrote configuration validation to accept only `""`/`"fastembed"`; legacy
external/builtin values were normalized away. `031cbc8` then deleted the
OpenVINO encoder, its configuration, pooling implementation, tests, and
integration test. The removal commit states that the backend was **100%
unreachable**: `validBackends` rejected `openvino`, no reachable production
selection path remained, and no production caller constructed
`NewOpenVINOEmbedder`. This was a dead-code removal with no intended runtime
behavior change.

The shared OpenVINO tokenizer/compiler/tensor helpers survived `031cbc8`
because the reranker still used them at that point; `19dd9f5` then deleted
them along with the reranker. `L2NormalizeAll` moved to `normalize.go` for
the FastEmbed path and survives at HEAD.

## 3. Surviving FastEmbed implementation

`internal/embed/fastembed.go` fixes the model identity to:

```text
fastembed:fast-bge-small-en-v1.5:maxlen=512:prompts=bge
```

> **⚠ Documentation divergence (flagged, not hidden):** the live
> `docs/embedders.md:16` quotes the short form
> `fastembed:fast-bge-small-en-v1.5:maxlen=512` — missing the trailing
> `:prompts=bge` segment. The code (`internal/embed/fastembed.go:23`,
> `FastEmbedModelID`) is authoritative; the persisted `model` column and all
> cache-identity logic carry the full form with prompts.

The model is 384-dimensional, uses BGE v1.5's passage/query convention, and
runs through native Go `fastembed` backed by ONNX Runtime. Model files are
cached under `$XDG_CACHE_HOME/spoon/models/fastembed` (or the equivalent user
cache fallback). `ONNX_PATH` points at `libonnxruntime.so`.

### Safe provisioning

`fastembed_provision.go` downloads the pinned archive
`https://storage.googleapis.com/qdrant-fastembed/fast-bge-small-en-v1.5.tar.gz`
and enforces hard bounds (`fastembed_provision.go:31-38`):

| Guard | Value |
|---|---|
| archive size cap | `fastEmbedMaxArchiveBytes = 1 << 30` (1 GiB) |
| entry size cap | `fastEmbedMaxEntryBytes = 1 << 30` |
| entry count cap | `fastEmbedMaxEntries = 4096` |
| download timeout | `fastEmbedDownloadTimeout = 15 * time.Minute` |

SHA-256 verification is **opt-in**: set `SPOON_FASTEMBED_SHA256` and the
downloaded archive is checked before extraction
(`fastembed_provision.go:43-46`). Path traversal entries are rejected,
extraction is atomic (temp dir → rename), and a bad/incomplete cache is
discarded and re-provisioned. This is now the *only* model-download path in
Spoon: the generic `internal/models` HF downloader was deleted in `19dd9f5`.

The model is embedded in batches of 32 by default, with bounded concurrent
ONNX sessions; the configured max length is fixed at 512. Config resolution
(`cmd/spn/forks.go:resolveFastEmbedConfig`, lines 1190-1196) coalesces CLI
flag → `SPOON_FASTEMBED_MODEL` / `SPOON_FASTEMBED_CACHE` → `config.json`
`fastembed.model` / `fastembed.cacheDir`.

FastEmbed powers:

- durable semantic documents and `spn search`;
- semantic clustering when available;
- zero-shot category anchors.

If ONNX Runtime/model initialization fails, `cmd/spn/forks.go` emits
`embed_unavailable`, skips semantic indexing, and lets lexical clustering
continue. `--no-embed` or `SPOON_NO_EMBED=1` intentionally takes the same
no-semantic path.

## 4. Surviving lexical embedder

`internal/embed/local.go` implements `BuiltinModelName =
`builtin-lexical-v1` with 512 signed-hash buckets. It tokenizes lower-case
alphanumeric runs, adds within-line adjacent bigrams, computes sublinear TF and
smoothed IDF across the current embedding call's corpus, folds tokens into
signed feature buckets, and L2-normalizes each vector.

The lexical engine is corpus-relative and therefore not suitable for a
persistent cross-run semantic index. It remains the intentional zero-setup
engine for:

- interactive TUI clustering;
- `--query` relevance scoring — now unconditional in production (`cmd/spn`
  leaves `opts.QueryScorer` nil, so `LexicalQueryScorer` is the only scorer;
  the reranker and its configuration no longer exist, see
  [current-state.md](current-state.md));
- cluster fallback when FastEmbed is absent;
- tests and deterministic offline operation.

The four multimodal lexical blobs are weighted paths `0.3`, commits `0.3`,
README `0.2`, and diff `0.2`. Current cluster epsilon defaults are `0.35` for
FastEmbed and `0.55` for lexical vectors (`docs/embedders.md` and
`internal/cluster/pipeline.go`).

## 4a. Multimodal fork-feature contract (surviving)

`internal/embed/multimodal.go` defines the fork-feature embedding contract
that both FastEmbed and the lexical backend feed:

- four modalities in fixed order: **paths, commits, readme, diff**;
- weights `modalityWeights = [0.3, 0.3, 0.2, 0.2]`
  (`multimodal.go:9`);
- each modality blob is rune-truncated at `maxEmbedModalityChars = 16000`
  (`multimodal.go:21`);
- missing (empty) modalities contribute a zero block;
- the concatenated weighted vector is **L2-normalized** before use.

These weights and caps are part of the model-quality contract and must be
frozen during any model comparison (see
[architecture-and-data-flow.md](architecture-and-data-flow.md) §5).

## 4b. Environment variable census

Surviving embedding-relevant variables at HEAD:

| Variable | Effect |
|---|---|
| `SPOON_FASTEMBED_MODEL` | override the pinned FastEmbed model name |
| `SPOON_FASTEMBED_CACHE` | override the FastEmbed cache dir (default `$XDG_CACHE_HOME/spoon/models/fastembed`) |
| `SPOON_FASTEMBED_SHA256` | opt-in SHA-256 verification of the provisioned archive |
| `SPOON_NO_EMBED` | `=1` disables the semantic embedder; lexical clustering continues |
| `ONNX_PATH` | path to `libonnxruntime.so` for the FastEmbed backend |

Retired by the removal series: `SPOON_OPENVINO_RERANKER`,
`SPOON_OPENVINO_LABELER`, `SPOON_OPENVINO_DEVICE`, `SPOON_OPENVINO_LIB`,
`SPOON_OPENVINO_GENAI_LIB`, `SPOON_EVAL_RERANKERS`, `SPOON_EVAL_LABELERS`,
and the `SPOON_FETCH_*` download limits — all consumed only by code deleted
in `19dd9f5`. (`SPOON_EVAL_EMBEDDERS` survives: manual embedder comparison,
see [evaluation.md](evaluation.md). Historical external-service variables —
`SPOON_EMBEDDER_URL`, `SPOON_EMBEDDER_MODEL`, `SPOON_OPENAI_BASE_URL`,
`OPENAI_API_KEY`, `SPOON_SIDECAR_MODEL`, `SPOON_SIDECAR_DEVICE` — died with
`355dcd8`.)

## 5. Models considered but not blindly restoreable

The preserved panel shows that model choice alone was not the dominant factor.
Snowflake Arctic long-context embeddings passed the gate when full context was
available, while multiple code-specialized encoders underperformed the baseline.
The Phase-A failure was caused partly by artificial 5 KB modality truncation and
Ollama context failures. Any reimplementation should repeat the benchmark with
its actual input window, pooling rule, and serving runtime rather than infer
quality from model names or parameter count.
