# Legacy AI/vector/embedding/reranking functionality

This directory preserves the implementation record for spoon's model-backed
features that were removed, narrowed, or may be removed next. It is intended
as a re-implementation map, not a recommendation to restore the old design
unchanged.

## Scope and repository state

The archaeology was performed in:

- checkout: `/home/svnbjrn/dev/projects/revw/spoon`
- branch: `fix/export-compare-url-and-created-at`
- inspected tip: `07e94ee` (`feat(spn): store-backed compare reuse and mandatory store for forks list`)
- working tree at inspection: clean; no untracked or uncommitted removal was present

The OpenVINO **embedder** is already removed. The OpenVINO **query reranker**,
cluster-label **GenAI polisher**, their runtime loader, and the supporting model
registry remain at this tip. They are documented here because they are the
likely targets of the pending removal the user described.

## Read in this order

1. [architecture-and-data-flow.md](architecture-and-data-flow.md) — subsystem map,
   call graph, vector/document persistence, and fallback boundaries.
2. [embedders.md](embedders.md) — historical Ollama/OpenAI/sidecar/OpenVINO
   embedders, then the surviving FastEmbed and lexical implementations.
3. [reranker-labeler.md](reranker-labeler.md) — current OpenVINO cross-encoder
   and GenAI labeler, including the interfaces a future replacement must honor.
4. [evaluation.md](evaluation.md) — the preserved 53-pair behavioral dataset,
   panel methodology, model results, and re-validation procedure.
5. [removal-log.md](removal-log.md) — commit-by-commit provenance and a
   restoration checklist.

## Primary source index

| Subject | Source |
|---|---|
| Current model/config overview | `docs/embedders.md` |
| Current embedder selection | `internal/embed/backend.go`, `internal/embed/fastembed.go`, `cmd/spn/forks.go` |
| Current lexical fallback | `internal/embed/local.go` |
| Current document construction/indexing | `internal/semantic/semantic.go`, `internal/store/store.go` |
| Current clustering | `internal/cluster/pipeline.go`, `internal/cluster/classify.go` |
| Current reranker | `internal/embed/rerank.go`, `rerankconfig.go`, `pairtemplate.go` |
| Current GenAI labeler | `internal/genai/config.go`, `genai.go`, `labeler.go` |
| Current model downloader/registry | `internal/models/models.go`, `cmd/spoon/setup.go` |
| Removed OpenVINO embedder | `git show 031cbc8^:internal/embed/openvino.go`, `ovconfig.go`, `pooling.go` |
| Removed external clients | `git show 355dcd8^:internal/embed/ollama.go`, `openai.go`, `sidecar.go`, `bootstrap.go`, `detect.go` |
| Removed Python sidecar | `git show 355dcd8^:embed/sidecar/README.md`, `server.py` |
| Preserved model evaluation | `experiments/started/behavioral-embeddings/` |

## Historical timeline

| Date | Commit | Result |
|---|---|---|
| 2026-06-12 | `355dcd8` | Replaced external Ollama/OpenAI/sidecar embedding and Ollama labeler paths with in-process OpenVINO embedding/reranking and GenAI label polishing; the lexical engine followed in `ac0dcf0`. |
| 2026-06-14 | `ac0dcf0` | Added the deterministic builtin lexical embedder for zero-setup clustering/fallback. |
| 2026-06-14 | `806c4f6` | Replaced hard native linkage/build tags with lazy `dlopen`; default builds contained the full feature code and degraded when runtimes were absent. |
| 2026-07-12 | `45601e4` | Made FastEmbed the only user-selectable persistent/semantic embedder; retained lexical internally for clustering and fallback. |
| 2026-07-26 | `031cbc8` | Deleted the unreachable OpenVINO embedder and its pooling/config code; retained the reranker, labeler, and shared OpenVINO runtime helpers. |

## Re-implementation principles

- Re-establish a stable model identity and document hash before writing vectors.
- Keep vector dimensions, pooling, normalization, tokenizer templates, and
  truncation policy explicit; changing any of them invalidates persisted data.
- Preserve graceful degradation: lexical clustering is the no-runtime path;
  semantic indexing must not make fork listing fail.
- Re-run the behavioral evaluation before changing a default model. The old
  experiment found that context handling and pooling choices mattered more than
  simply selecting a larger or more code-specific model.
- Treat OpenVINO model directories as a compatibility contract: tokenizer IR,
  encoder/decoder IR, `config.json`, tokenizer metadata, and compiled-kernel
  cache must agree.
