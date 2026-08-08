# OpenVINO query reranker and cluster-label polisher

These features are still present at the inspected tip `07e94ee`; they are not
part of the semantic embedder. If a future cleanup removes them, this document
records the contracts that need to be preserved or deliberately replaced.

## 1. Query reranker

### Public behavior and caller

`cmd/spn/forks.go:newQueryScorer` checks, in precedence order:

1. `$SPOON_OPENVINO_RERANKER` for the model directory;
2. `config.json`'s `reranker.modelPath`;
3. `$SPOON_OPENVINO_DEVICE`, then `reranker.device` for the device.

With no model configured, it returns no scorer and the stream uses
`embed.LexicalQueryScorer`. With a configured model, it constructs
`embed.NewReranker` and returns its `Close` function. A configured but unloadable
reranker is an error rather than a silent quality downgrade.

`internal/forksops/stream.go` passes the scorer through the fork-enrichment
pipeline. For `spn forks list --query <text>`, each fork's bounded change digest
(commit subjects plus touched paths) is scored and output receives `queryScore`
and `queryMethod` (`"openvino"` or `"lexical"`). This is distinct from
`spn search`, which embeds a query and performs vector retrieval over the
persistent FastEmbed index.

### Model layout and defaults

`internal/embed/rerankconfig.go:RerankConfig` expects an OVMS-style directory:

```text
openvino_model.xml (+ .bin)       # cross-encoder
openvino_tokenizer.xml (+ .bin)   # tokenizer custom ops
config.json                       # optional max_position_embeddings
tokenizer.json                    # pair template
```

The default provisioned model is
`OpenVINO/bge-reranker-base-fp16-ov` (`internal/models/models.go`). The
repository's model-evaluation comment identifies the default as
`bge-reranker-base-fp16`; the 2026-06-12 comparison retained it over
`Qwen3-Reranker-0.6B-seq-cls-fp16`.

Defaults:

- device: `GPU`; tokenizer: always `CPU`;
- max tokens: `config.json.max_position_embeddings`, otherwise 512;
- max batch: 8 pairs;
- tokenizer extension: `ResolveTokenizersLib()`;
- compile cache: `$XDG_CACHE_HOME/spoon/openvino` or `~/.cache/spoon/openvino`.

The model path is required and both XML files are validated before runtime
initialization. `RerankConfig.RerankerID` is an absolute-path identity for
metadata, not a portable model-version identity; a future persistent cache
should use a content/model revision as well.

### Pair construction

`internal/embed/pairtemplate.go` parses the converted model's `tokenizer.json`
pair template rather than hard-coding a tokenizer's special-token layout. The
reranker:

1. tokenizes `[query, document...]` through the converted OpenVINO tokenizer;
2. strips the single-text wrappers from each token sequence;
3. assembles each row using the model template, conceptually
   `BOS query EOS SEP document EOS` for the BGE model;
4. rejects an overlong query and truncates assembled rows to max context;
5. pads each batch to a rectangular `[batch, sequence]` tensor.

This is a deliberate compatibility divergence from OVMS `/v3/rerank`: OVMS
chunks long documents and max-aggregates the chunk scores, while Spoon keeps a
single truncated row. Fork digests are currently bounded enough that truncation
was considered acceptable, but a faithful future reimplementation must either
preserve this divergence explicitly or implement OVMS-style chunking and update
its quality/evaluation contract.

The converted cross-encoder receives `input_ids` and `attention_mask`. If its
compiled input names include `token_type_ids`, the implementation supplies an
all-zero tensor, matching OVMS's behavior for these models. Rows run in batches
of at most eight.

The output is `logits`; if there is one class, index 0 is used; if there are
multiple classes, index 1 is the positive class. Each logit is transformed by:

```text
score = 1 / (1 + exp(-logit))
```

The result is one score in `[0,1]` per document. Calls are serialized under a
mutex because the implementation reuses OpenVINO infer requests. `Close`
releases tokenizer/encoder requests, compiled models, and the OpenVINO core.

### Runtime loading

`internal/embed/ovload.go` and `internal/embed/ovffi.c/.h` make OpenVINO a
runtime dependency instead of a build-time dependency:

1. `SPOON_OPENVINO_LIB`, if set;
2. `/opt/intel/openvino-genai/lib/libopenvino_c.so`;
3. `libopenvino_c.so` through the dynamic loader.

`sync.Once` makes the probe process-wide. The vendored C ABI wrapper uses
`dlopen`/`dlsym` and opaque OpenVINO handles; the Cgo bridge is linked only with
`-ldl`. This kept normal Go builds portable while permitting runtime fallback
when OpenVINO is absent. `libopenvino_tokenizers.so` is searched in
`/usr/lib`, `/usr/lib64`, `/opt/intel/openvino-genai/lib`, and
`/usr/lib/ovms/lib`, then the bare soname.

The original OpenVINO implementation was introduced under build tags by
`355dcd8`; `806c4f6` removed the tags and stub files, added lazy runtime
loading, and documented the `libm.so.6` preload needed to avoid Intel oneAPI
IFUNC relocation crashes during `dlopen`.

## 2. GenAI cluster-label polisher

### Caller and fallback

`cmd/spn/forks.go:newLabelPolisher` and the equivalent constructor in
`cmd/spoon/main.go` resolve:

- `$SPOON_OPENVINO_LABELER`, then `config.json`'s `labeler.modelPath`;
- `labeler.device` (default `GPU`).

No configured model means no polisher. `cluster.RunPipeline` generates a
heuristic label first, then calls the polisher for every non-noise cluster.
Errors retain the heuristic label. The heuristic is therefore the correctness
fallback and the LLM is presentation polish.

### Model and generation contract

The default registry entry is
`OpenVINO/Qwen2.5-1.5B-Instruct-int4-ov`, roughly 1.1 GB. The model directory
must contain `openvino_model.xml`; `genai.Config` defaults to `GPU`, a 24-token
output cap, and the same OpenVINO compile cache as the reranker.

`internal/genai/labeler.go` supplies:

- system prompt: title GitHub repository-fork clusters, return only 3–7 plain
  words, no quotes/punctuation/explanation;
- user prompt: upstream repo, heuristic keywords, up to six sample commit
  subjects and touched paths, then an explicit natural-English title request;
- greedy decoding (`do_sample=false`) and `max_new_tokens=24`;
- output cleanup: first line only, trim quote/backtick wrappers and trailing
  punctuation, cap at 60 runes at a word boundary.

`internal/genai/genai.go` creates a chat history with system/user JSON messages,
passes it through the OpenVINO GenAI chat-template pipeline, and serializes
concurrent generation calls. It loads `libopenvino_genai_c.so` lazily using:

1. `$SPOON_OPENVINO_GENAI_LIB`;
2. `/opt/intel/openvino-genai/lib/libopenvino_genai_c.so`;
3. `libopenvino_genai_c.so`.

The labeler additionally needs `libopenvino_tokenizers.so` through the GenAI
runtime. `internal/genai/genai.go`'s C bridge owns the pipeline, generation
config, chat history, and decoded results.

### Model selection evidence

`internal/genai/eval_labelers_test.go` supports manual comparisons through
`SPOON_EVAL_LABELERS`. The current docs record the retained
Qwen2.5-1.5B-int4 labeler versus Qwen3-0.6B-int4: three representative labels
were good at 109 ms warm for the retained model, while the challenger produced
no acceptable labels because thinking output consumed the short token budget.
The sample is small; treat this as a historical default choice, not a permanent
benchmark conclusion.

## 3. Setup and model registry

`internal/models/models.go` maintains public OpenVINO Hugging Face defaults and
pure-Go download/progress/resume logic:

| Feature | Registry repo | Approx. size |
|---|---|---:|
| historical embedder | `OpenVINO/bge-base-en-v1.5-fp16-ov` | 440 MB |
| reranker | `OpenVINO/bge-reranker-base-fp16-ov` | 560 MB |
| labeler | `OpenVINO/Qwen2.5-1.5B-Instruct-int4-ov` | 1,100 MB |

`cmd/spoon/setup.go` no longer provisions the historical embedder through this
registry: FastEmbed has its own ONNX provisioning. It provisions only the
optional reranker and labeler slots, records their paths in config, detects
OpenVINO availability/devices, and uses consent-gated downloads unless
`--auto-pull` is supplied.

If reranker/labeler removal proceeds, decide explicitly whether to also remove
(or repurpose) the historical `FeatureEmbedder` registry entry. At this tip it
is retained for compatibility/tests, while production setup uses
`setupFastEmbed` instead.

## 4. Reimplementation boundaries

A replacement may preserve the Go interfaces:

```go
type QueryScorer interface {
    Rerank(ctx context.Context, query string, docs []string) ([]float64, error)
}

type LabelPolisher interface {
    PolishLabel(ctx context.Context, hint PolishHint) (string, error)
}
```

Preserve these semantics unless intentionally versioning output:

- same number/order of scores as input docs;
- stable `[0,1]` scores and explicit query method metadata;
- lexical query fallback when no reranker is configured;
- heuristic label fallback on polish failure;
- explicit model identity/configuration and cleanup;
- no OpenVINO SDK requirement for builds that do not use these features.
