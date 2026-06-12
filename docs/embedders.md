# Spoon's OpenVINO features

Spoon runs every model-backed feature **in-process** — there is no external
service, no model server. The OpenVINO runtime (linked behind build tags)
powers four features:

| Feature | What it does | Model (default) | Build tag |
|---|---|---|---|
| Semantic embedder | clusters forks by meaning, not just tokens | bge-base-en-v1.5 | `openvino` |
| Query reranker | `spn forks list --query "intent"` relevance | bge-reranker-base | `openvino` |
| Zero-shot categories | per-fork facet: feature/bugfix/ci/security/… | (uses embedder) | `openvino` |
| Label polish | LLM rewrites cluster labels into natural titles | Qwen2.5-1.5B-int4 | `openvino genai` |

**`spoon setup` provisions everything**: it detects the runtime and devices,
downloads the default model for any feature with none configured (pure-Go
HuggingFace download, consent-gated; `--auto-pull` to skip the prompt), and
writes the config. After setup, plain `spoon owner/repo` and
`spn forks list` use the OpenVINO backend automatically.

Without the build tags (or without setup), spoon falls back to the
**builtin** deterministic lexical embedder — zero setup, no downloads, and
`--query` still works via lexical cosine scoring.

## How it works

For each fork, spoon builds four modality blobs: touched file paths, commit
messages (merges excluded), a README excerpt, and a normalized diffstat
chunk. Each blob is embedded with a deterministic lexical embedder:

- tokens are lowercase alphanumeric runs plus within-line adjacent bigrams
  (so `internal/auth` path structure and "rate limit" commit phrasing carry
  signal);
- weights are sublinear TF × smoothed IDF, with IDF computed over the run's
  own corpus — boilerplate shared by every fork cancels out, and
  fork-distinctive tokens dominate;
- token weights are folded into a fixed 512-dim vector via signed feature
  hashing and L2-normalized.

The four modality vectors are concatenated with weights
`[paths 0.3, commits 0.3, readme 0.2, diff 0.2]` and clustered with
single-link agglomerative clustering at a cosine-distance cutoff
(`--cluster-epsilon`, default 0.55, tuned for this embedder).

Cluster labels are heuristic and deterministic: the dominant directory
prefix (up to two levels) plus the top TF-IDF discriminators from member
commit messages — unigrams and bigrams — with a path-segment fallback when
commit text is unavailable.

## Properties

- **Zero setup** — clustering always runs; there is no "embedder
  unreachable" failure mode.
- **Deterministic** — the same fork set always produces the same clusters,
  which makes results reproducible and cache-friendly.
- **Fast** — embedding a 50-fork run is sub-millisecond; the network calls
  to fetch fork data dominate end-to-end time.

## Tuning

```sh
spoon --cluster-epsilon 0.45 golang/go     # tighter clusters
spoon --cluster-min-size 2 golang/go       # allow pairs
spoon --no-cluster golang/go               # skip clustering entirely
spoon --cluster-top 100 golang/go          # embed more forks
```

## Query-driven fork search

```sh
spn forks list owner/repo --query "wayland support"
```

Every enriched fork's change digest (commit subjects + touched paths) is
scored against the query and output is sorted by relevance; each NDJSON
record gains `queryScore` (0..1) and `queryMethod` ("openvino" when the
cross-encoder reranker is configured, "lexical" otherwise). The reranker
reimplements OVMS's `/v3/rerank`: pairs are framed with the tokenizer's own
pair template (parsed from tokenizer.json), scored by the cross-encoder,
and squashed with a sigmoid.

## Zero-shot categories

With the openvino embedder active, each clustered fork gets a `category`
facet (`feature`, `bugfix`, `security`, `ci-build`, `docs`, `localization`,
`dependencies`, `port`, `config`) assigned by cosine similarity between its
change digest and anchor descriptions — no extra model, one extra embedding
batch. Forks matching no anchor stay uncategorized.

## Cluster label polish

With a labeler configured (and a `-tags "openvino genai"` build), each
cluster's heuristic label is rewritten by a small instruct LLM running
in-process via OpenVINO GenAI — greedy decoding, ≤24 new tokens, the
model's own chat template. Errors silently keep the heuristic label.
Example: `markdown/  ·  grammar, upstream, tests` → "Go Tree-sitter
Grammar Updates".

## The OpenVINO embedding backend

The `openvino` backend reimplements OVMS's `/v3/embeddings` computation
in-process: the openvino_tokenizers-converted tokenizer model runs on CPU
to turn each batch of texts into `input_ids`/`attention_mask`, the encoder
runs on the configured device (GPU by default), and pooling (CLS/MEAN/LAST)
plus L2 normalization run in Go. No model server is involved.

### Building

```sh
go build -tags "openvino genai" ./cmd/spoon ./cmd/spn   # all features
go build -tags openvino ./cmd/spoon ./cmd/spn           # no LLM labeler
```

Requires the OpenVINO C runtime at build and run time (`openvino` +
`openvino-intel-gpu-plugin` on Arch) and `libopenvino_tokenizers.so` at run
time (ships with `openvino-genai`; spoon searches the usual locations).
Binaries built **without** the tag still work — the openvino backend just
reports that it is unavailable.

### Getting models

`spoon setup` downloads the defaults (no external tools needed). To use a
different model, any OVMS-style export works (`openvino_model.xml` +
`openvino_tokenizer.xml`): pre-converted models from the OpenVINO Hugging
Face org, an `ovms --pull` export, or
`optimum-cli export openvino --task feature-extraction`. When a
`graph.pbtxt` is present spoon picks up the intended pooling and
normalization from it.

### Running

```sh
spoon --embedder-backend openvino \
      --openvino-model ~/.local/share/spoon/models/OpenVINO/bge-base-en-v1.5-fp16-ov \
      golang/go

# or persistently, via env / config file:
export SPOON_EMBEDDER_BACKEND=openvino
export SPOON_OPENVINO_MODEL=~/.local/share/spoon/models/OpenVINO/bge-base-en-v1.5-fp16-ov
```

Flags: `--openvino-device` (default `GPU`; use `CPU`, `GPU.1`, …),
`--openvino-pooling cls|mean|last` (default: the model's graph.pbtxt, else
CLS — use `mean` for mean-pooled models like nomic-embed-text). The same
settings persist in `~/.config/spoon/config.json` under `embedder`.

The default `--cluster-epsilon` is 0.35 for this backend (dense-encoder
cosines separate at a tighter scale than the lexical embedder's 0.55).
Cluster caches are keyed by model path + pooling, so switching backends or
models never serves stale clusters.

On Intel Arc the first GPU load JIT-compiles kernels; spoon caches them
under `~/.cache/spoon/openvino` so subsequent loads take ~1 s.

## History

Earlier versions delegated embedding to external services (Ollama, a Python
sidecar serving arctic-embed-l-v2.0, or any OpenAI-compatible endpoint) and
optionally polished cluster labels with a local LLM. Those integrations were
removed in favor of in-process embedding: for the data spoon embeds
(path lists, commit subjects, diffstat lines — token-shaped, not prose),
lexical TF-IDF similarity captures most of the clustering signal without the
operational cost of running a model server, and the OpenVINO backend now
covers the semantic-encoder use case without a server either. The
evaluation that informed the earlier model choices is preserved under
`experiments/started/behavioral-embeddings/`.
