# Removal and restoration log

This table separates historical deletion from current retention. Use the commit
SHA with `git show` to recover exact source when a future implementation needs
behavior that prose cannot capture.

The pre-removal provenance includes the sidecar introduction sequence
`dcb7ff0`, `44f2c5a`, `01f588b`, `80da595` (2026-05-13) and the
OpenAI-compatible backend addition `4937fbc` (2026-06-08). Those commits are
useful when recovering the original CLI semantics, but the deleted blobs at
`355dcd8^` are the authoritative implementation snapshot used below.

| Commit | Removed/changed | Why | Restoration source |
|---|---|---|---|
| `355dcd8` (2026-06-12) | Ollama/OpenAI/sidecar embedders; Ollama chat labeler; endpoint config, detection, pull prompts, sidecar command/service/assets | Replaced external services with in-process OpenVINO embedding/reranking and GenAI label-polishing features; removed process/network/lifecycle dependencies | `git show 355dcd8^:internal/embed/{ollama.go,openai.go,sidecar.go,bootstrap.go,detect.go}`; `git show 355dcd8^:internal/cluster/llm_labeler.go`; `git show 355dcd8^:embed/sidecar/{README.md,server.py}` |
| `ac0dcf0` (2026-06-14) | Added the deterministic builtin lexical embedder and wired it as the zero-setup clustering/fallback engine | Portable offline clustering and graceful degradation | `git show ac0dcf0`; current `internal/embed/local.go` |
| `806c4f6` (2026-06-14) | Deleted OpenVINO/GenAI build-tag stubs and removed `openvino`/`genai` build-tag requirement | Lazy runtime `dlopen` made the default build portable while preserving optional features | `git show 806c4f6^:internal/embed/openvino_stub.go`, `rerank_stub.go`, `internal/genai/genai_stub.go`; current `ovffi.*`, `ovload.go` |
| `45601e4` (2026-07-12) | Removed user-selectable `openvino`, `builtin`, and external backend selection; removed backend flags; made FastEmbed default/opt-out | FastEmbed became the stable persistent semantic model; lexical remained internal fallback/TUI engine | `git show 45601e4^:internal/embed/backend.go`, old `cmd/spn/forks.go`, old `internal/config/config.go`; current `backend.go`, `fastembed.go`, `local.go` |
| `031cbc8` (2026-07-26) | Deleted OpenVINO encoder integration tests, `ovconfig.go`, pooling files; trimmed `openvino.go`; moved normalization | OpenVINO embedder was unreachable after `45601e4`: validation accepted only empty/fastembed, no production caller constructed it, and no reachable selection path remained | `git show 031cbc8^:internal/embed/openvino.go`, `ovconfig.go`, `pooling.go`; commit body gives the reachability proof |
| current `07e94ee` | Reranker, GenAI labeler, OpenVINO C ABI loader, tokenizer/compiler helpers, FastEmbed, lexical, semantic index remain | The inspected branch has no pending/untracked removal | `internal/embed/rerank.go`, `internal/genai/`, `internal/embed/ovffi.{c,h}`, `internal/embed/ovload.go` |

## File-level consequences

### After `355dcd8`

Deleted paths included:

- `cmd/spn/embed.go`, `cmd/spn/sidecar.go` and their tests;
- `cmd/spoon/embed.go`, `cmd/spoon/sidecar.go` and their tests;
- `internal/embed/ollama.go`, `openai.go`, `sidecar.go`, `bootstrap.go`,
  `detect.go`, `preflight.go`, `prompter.go` and tests;
- `internal/sidecar/sidecar.go` and tests;
- `embed/sidecar/` Docker, Python, systemd, benchmark, and README assets;
- `internal/cluster/llm_labeler.go` and its tests.

The surviving replacement was not a drop-in protocol swap: it moved model
loading, tokenization, pooling, runtime errors, and cleanup into the Go process.
A reimplementation should decide first whether that operational complexity is
still justified.

### After `45601e4`

This was a behavior/configuration consolidation rather than a large deletion.
It changed `internal/embed/backend.go`, CLI parsers, setup, config validation,
semantic indexing, and docs. Legacy values (`ollama`, `sidecar`, `openai`,
`builtin`, `lexical`) were normalized to the empty backend, which means
FastEmbed. `validBackends` accepted only empty/`fastembed`.

Important compatibility result: old JSON config files remain parseable because
unknown removed fields are ignored, but old backend selection no longer revives
old services.

### After `031cbc8`

The OpenVINO encoder's pooling and model config were removed, but the shared
runtime helpers were deliberately preserved for reranking. Do not restore the
whole old `openvino.go` blindly: that would reintroduce an encoder whose cache
identity, pooling, dimension, and persistence semantics no longer match the
current FastEmbed index.

## Future removal checklist for reranker/labeler

If the pending cleanup removes the reranker and labeler, audit all of these
surfaces together:

- `internal/embed/rerank.go`, `rerankconfig.go`, `pairtemplate.go`,
  `rerank_integration_test.go`, and reranker paths in `eval_models_test.go`;
- `internal/genai/` runtime, config, labeler, integration/evaluation tests;
- `internal/embed/ovffi.{c,h}`, `ovload.go`, `ovshared.go` — retain only if
  another feature still needs OpenVINO;
- `internal/models/models.go` feature registry and model-download tests;
- `cmd/spn/forks.go` query scorer and label-polisher constructors;
- `cmd/spoon/main.go` label-polisher constructor and help text;
- `cmd/spoon/setup.go` OpenVINO checks, model slots, config persistence;
- `internal/config/config.go` `Reranker`/`Labeler` fields and env/config docs;
- `internal/forksops/stream.go` `QueryScorer` plumbing and `queryMethod` output;
- `internal/cluster/pipeline.go` `LabelPolisher` and polish fallback;
- `docs/embedders.md`, README, CLI contract tests, setup tests, and integration
  tests.

Remove the feature as one contract change, not merely by deleting the model
constructor. Otherwise stale config keys, setup downloads, output metadata,
runtime loaders, or test fixtures will imply a capability that no longer works.
