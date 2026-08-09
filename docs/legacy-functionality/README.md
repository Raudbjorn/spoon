# Legacy Functionality — Removed or Displaced Features

Re-baselined against commit `426049c` (`docs`), the tip of
`fix/export-compare-url-and-created-at`, 2026-08-08.

This directory documents functionality that has been **removed or displaced**
from the codebase. It exists for two reasons:

1. **Historical context** — what the project used to do, and why it changed.
2. **Re-implementation reference** — detailed contracts of removed features
   (exact behavior, interfaces, configuration, failure modes) for anyone who
   wants to bring them back, possibly with a different implementation.

The most recent removal landed in commit `19dd9f5` ("refactor: remove OpenVINO
reranker and labeler entirely", 2026-08-08): the in-process OpenVINO
cross-encoder reranker, the GenAI-based labeler, the residual shared OpenVINO
runtime/FFI code that supported the reranker, and the model registry left the
tree. Around it, the zero-config/store relocation series (`2f1560a`, `7d56911`,
`b425566`, `9abb5dc`) moved the store and config into
`$XDG_CONFIG_HOME`/`/etc/spoon`. These docs have been re-baselined to that
state. For the authoritative snapshot of what **currently** exists at HEAD, see
[current-state.md](current-state.md).

## Files

| File | Scope |
|---|---|
| [current-state.md](current-state.md) | **Start here.** What exists at HEAD `426049c`: live embedders, paths, env vars, seams, and known stale code text |
| [removal-log.md](removal-log.md) | Chronological removal/re-baseline history with commit SHAs and what still exists |
| [reranker-labeler.md](reranker-labeler.md) | The removed OpenVINO reranker and GenAI labeler (removed in `19dd9f5`); what survived of the query-scoring seam; re-implementation guidance |
| [embedders.md](embedders.md) | Embedder evolution: external services → OpenVINO embedder → FastEmbed; current provisioning details |
| [architecture-and-data-flow.md](architecture-and-data-flow.md) | Architecture and data-flow diagrams reflecting the post-`19dd9f5` state (historical diagrams kept where useful) |
| [evaluation.md](evaluation.md) | Evaluation harness history; the surviving manual eval gate |
| [reimplementation-notes.md](reimplementation-notes.md) | How to bring removed features back: interfaces, entry points, pitfalls |

## Reading order

For context: `README.md` → `current-state.md` → `removal-log.md` → then the
topic file you care about.

For re-implementing something: the topic file → `reimplementation-notes.md`.

## Provenance note

Several features documented here were deleted in commits that also deleted
their source files. The deleted code has been reconstructed from git history
(`git show <sha>:<path>` at the parent commit). Key parent commits:

- `19dd9f5^` — OpenVINO reranker (`internal/embed/rerank.go`,
  `internal/embed/rerankconfig.go`, `internal/embed/ovload.go`,
  `internal/embed/ovshared.go`, `internal/embed/pairtemplate.go`), GenAI
  labeler (`internal/genai/genai.go`, `internal/genai/labeler.go`,
  `internal/genai/config.go`), and the model registry
  (`internal/models/models.go`). The residual shared OpenVINO runtime/FFI
  support code the reranker depended on (`internal/embed/openvino.go`,
  `ovffi.c`, `ovffi.h`) was also deleted here.
- `031cbc8^` — the full OpenVINO model embedder
  (`internal/embed/openvino.go` at ~311 lines, plus `ovconfig.go` and
  `pooling.go`, reduced or removed by `031cbc8` itself).
- `355dcd8^` — external embedding services (`internal/embed/ollama.go`,
  `internal/embed/openai.go`, `internal/embed/sidecar.go`, plus
  `bootstrap.go`, `detect.go`, `preflight.go`, and the Python sidecar under
  `embed/sidecar/`).

All current-state claims in this directory are anchored to the HEAD commit
listed above; line references may drift as the codebase moves.

## Primary sources (post-removal tree)

### Reranking / query scoring

| Area | Where |
|---|---|
| Current reranker | none — reranker deleted in `19dd9f5`; query scoring is lexical |
| Surviving `QueryScorer` seam | `internal/embed/queryscore.go`; production leaves it nil (`cmd/spn/forks.go`), so `--query` uses `LexicalQueryScorer` |
| Deleted OpenVINO reranker | `internal/embed/rerank.go` (reconstructed from `19dd9f5^`) |

### Labeler

| Area | Where |
|---|---|
| Current labels | deterministic `HeuristicLabel` only (`internal/cluster/labels.go`) |
| Deleted GenAI labeler | `internal/genai/genai.go`, `internal/genai/labeler.go` (reconstructed from `19dd9f5^`) |

### Embedders

| Area | Where |
|---|---|
| FastEmbed backend | `internal/embed/fastembed.go`, `internal/embed/fastembed_provision.go` |
| Built-in lexical backend | `internal/embed/local.go` |
| Backend selection | `internal/embed/backend.go` |
| Historical external services | `internal/embed/{ollama,openai,sidecar}.go` (deleted in `355dcd8`) |
| Historical OpenVINO embedder | `internal/embed/openvino.go` (embedding behavior removed in `031cbc8`; the remaining runtime/FFI code that supported the reranker was deleted in `19dd9f5`) |

### Model registry

| Area | Where |
|---|---|
| Deleted registry | `internal/models/models.go` (deleted in `19dd9f5`) |

### Configuration

| Area | Where |
|---|---|
| Config | `internal/config/config.go` (`reranker`/`labeler` keys no longer exist and are silently ignored) |

## Methodology

- Every claim about removed functionality is grounded in either (a) the
  deleted source reconstructed via `git show <parent-sha>:<path>`, or (b) a
  surviving sibling file at the same commit.
- Every claim about current behavior is grounded in HEAD (`426049c`) source.
- Line references are given for the reconstruction commit; they may drift.
- Where the summary below is brief, the full contract is in the cited source
  file.
- Stale code text that survived removal (comments/help still mentioning the
  OpenVINO reranker or labeler) is flagged explicitly in the relevant files
  rather than papered over.
