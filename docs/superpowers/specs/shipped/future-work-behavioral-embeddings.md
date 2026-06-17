> **Status (2026-06-17): PHASE A REJECTED, PHASE B ABANDONED.** See `docs/embedders.md` and commit `355dcd8`. The CodeExecutor sidecar approach described here was rejected at Phase A; Phase B's Snowflake/snowflake-arctic-embed-l-v2.0 panel winner was itself superseded by the convergence to in-process OpenVINO. The Python sidecar that this spec envisioned (`embed/sidecar/server.py`) was removed in commit `355dcd8`. The spec remains as design history; the OpenVINO embedder backend (`--embedder-backend openvino`) is the source of truth.

# Future Work: Behavioral / Execution-Pattern Embeddings for Fork Clustering

**Status:** Phase B in progress with `Snowflake/snowflake-arctic-embed-l-v2.0`. Originally deferred from v1; Phase A rejected the CodeExecutor proposal on 2026-05-11 (Δ = −0.0826); the 4-model panel re-test on 2026-05-13 cleared the +0.05 gate with Snowflake/snowflake-arctic-embed-l-v2.0 (Δ = +0.1196). The SFR-2B escalation prescribed by the decision matrix did not cross the +0.15 acceptance threshold, so the panel winner (a general-purpose long-context encoder, not a code-specific one) takes the Phase B slot. Full write-up: `experiments/started/behavioral-embeddings/RESULTS_PANEL.md`. Phase B sidecar plan: `docs/superpowers/plans/2026-05-13-behavioral-embeddings-sidecar.md`.
**Parent plan:** `plan-integrating-this-research-declarative-puddle.md`
**Estimated effort:** 2–3 weeks engineering + evaluation.

## Context

V1 of spoon's fork clustering uses general-purpose or contrastive-trained text embedders (`nomic-embed-text` by default, `jina-embeddings-v2-base-code` for the code-aware path). Both produce embeddings predominantly from *syntactic* structure: the model maps similar-looking text to nearby vectors. This document describes an upgrade to **behavioral / execution-pattern embeddings**, where the embedder is pre-trained on tasks that require the model to reason about what the code *does* at runtime, not merely how it's spelled.

## Origin of the idea

Two forks can be syntactically similar but functionally divergent (a one-line change introducing a race condition versus the same one-line change adding a bounds check, for example), and vice versa: two functionally identical forks can have wildly different diffs (variable renames, different control-flow structures expressing the same algorithm). V1's clustering will tend to:
- collapse the syntactically-similar-but-functionally-different forks into one cluster (a false merge), and
- spread the syntactically-different-but-functionally-equivalent forks across multiple clusters (a false split).

Behavioral embeddings should help with both: by encoding runtime semantics, two functionally equivalent diffs end up closer in vector space regardless of surface form, and two functionally different diffs end up further apart regardless of surface similarity. The most actionable distinction for fork triage is "did this change *behavior*?" — exactly what behavioral embeddings are pre-trained to capture.

## Research basis

**Perera et al., "CodeBERT-Based Embeddings for Detecting Vulnerable Smart Contracts" (IEEE 2026).** Compared five CodeBERT-family models on smart contract vulnerability classification using the same downstream classifier:

| Model | AUC | Pre-training objective |
| --- | --- | --- |
| CodeBERT | 0.9867 | Masked language modeling on code + natural language |
| GraphCodeBERT | 0.9770 | Adds data-flow graph awareness |
| UniXcoder | 0.9946 | Unified cross-modal (code + NL + AST) |
| CodeReviewer | 0.9582 | Code review comment prediction |
| **CodeExecutor** | **0.9963** | **Predicts code execution outputs** |

The notable result is that CodeExecutor (trained to *simulate* execution) outperforms GraphCodeBERT (trained with *explicit data-flow*). This suggests behavioral pre-training is more discriminative than graph-structural pre-training for the task of separating functional variants — directly applicable to fork clustering.

**Liu et al., "Code Execution with Pre-trained Language Models" (arXiv:2305.05383, 2023).** The original CodeExecutor paper. The model is trained on synthetic execution traces to predict program outputs from source. The training signal aligns with our use case: "does this fork *do* something different?" The model's pre-training is in Python; multi-language support requires fine-tuning or model swap.

**Bui et al., "Self-Supervised Learning for Code Retrieval and Summarization through Semantic-Preserving Program Transformations" (Corder, arXiv:2009.02731v4, 2021).** Complementary evidence: contrastive learning over semantically-equivalent transformed snippets (variable rename, statement permute, dead code insertion) produces embeddings where functionally equivalent code is closer in vector space. Corder is training-time; CodeExecutor is inference-time; they share the underlying goal of encoding behavior rather than syntax.

## Implementation sketch

CodeExecutor is not on Ollama at the time of writing. The path to local serving is a Python sidecar process using HuggingFace transformers.

```
internal/
  embed/
    sidecar.go              NEW — process management for the sidecar
    sidecar_test.go         NEW
embed/
  sidecar/
    server.py               NEW — minimal HTTP server loading the model
    requirements.txt        NEW — torch, transformers, fastapi, uvicorn
    Dockerfile              NEW — for users who prefer container-based deployment
```

`internal/embed/sidecar.go` exposes:

```go
type SidecarEmbedder struct {
    Endpoint string  // http://localhost:<port>
    Cmd      *exec.Cmd
}

// NewSidecarEmbedder launches the sidecar (if not already running) and waits
// for the health endpoint to become available. Returns an Embedder.
func NewSidecarEmbedder(ctx context.Context, model string) (embed.Embedder, error)

// Embed POSTs to the sidecar's /embed endpoint. Matches the Embedder interface
// so the rest of the pipeline is unchanged.
```

The sidecar exposes:
- `POST /embed` → `{"texts":[...]}` → `{"vectors":[[...]]}`
- `GET /health` → 200 when model is loaded.

Users wire this via `--embedder http://localhost:8765 --embedder-model code-executor` or with a convenience flag `--embedder-backend sidecar`.

## Cost analysis

| Resource | Estimate |
| --- | --- |
| Model download | ~500 MB (CodeExecutor base) |
| Process RAM | 2.5–3.5 GB resident |
| Embedding latency (CPU) | 200–800 ms per fork at ~4 KB input |
| Embedding latency (GPU) | 30–80 ms per fork |
| Clustering pass on 50 forks (CPU) | ~25–40 s |
| Python startup overhead | ~3 s cold, then warm |

Token cost: none (local). Network cost: none.

## Acceptance criteria

1. End-to-end run on IBM/mcp-context-forge completes successfully with the sidecar embedder.
2. Hand-curated validation: pick 20 fork pairs from IBM/mcp-context-forge that are *known* functionally similar (same plugin re-implemented) and 20 *known* functionally different (one adds OAuth, another adds metrics). Both embedders rank each pair. CodeExecutor should achieve higher rank correlation with the hand judgment than nomic-embed-text — specifically, a Kendall's tau improvement of ≥ 0.15.
3. Intra-cluster cosine distance (mean over all v1 clusters) decreases by at least 10% when switching from nomic-embed-text to the CodeExecutor sidecar on the same dataset.
4. Graceful degradation: if the sidecar process dies mid-run, the pipeline reverts to the configured fallback embedder within 5 seconds, never crashes.

## Risks and open questions

- **Adoption friction.** Python dependency conflicts with spoon's single-static-binary culture. Mitigate by making the sidecar opt-in and well-documented (`docker run spoon-codeexecutor-sidecar` as the easy install path).
- **Untested for fork diffs.** CodeExecutor was pre-trained on synthetic execution traces of complete programs, not diffs. May underperform on partial-program inputs. Initial experiments should verify.
- **Language coverage.** Pre-training is Python-heavy. Forks of Go/Rust/JS repos may produce poor embeddings. Fall back to general-purpose embedder for unsupported languages, gated by `T2Data.PrimaryLanguage`.
- **License.** Confirm CodeExecutor weights are redistributable (MIT? Apache? Microsoft Research? — check on the original release).

## How to complete

1. **Validation experiment first.** Before any code, hand-curate ~40 fork pairs from a real spoon analysis (IBM/mcp-context-forge is a good source given prior field work). Run each pair through both Ollama-served nomic-embed-text and a local-HF-script CodeExecutor. Compute pair-wise cosine similarity. Compare against hand-judged similarity (1 = same intent, 0 = different intent). Reject the entire feature if Kendall's tau improvement is < 0.05.
2. **Build the minimal sidecar.** `embed/sidecar/server.py` should be ≤ 100 lines: load model, expose `/embed` and `/health`, batch up to 32 texts per request.
3. **Build `internal/embed/sidecar.go`.** Process management (start, health-poll, graceful shutdown on context cancel). Tests with a `httptest.Server` mocking the sidecar.
4. **Wire into bootstrap.** When `--embedder-backend sidecar` or `SPOON_EMBEDDER_BACKEND=sidecar`, route through `SidecarEmbedder` rather than `OllamaClient`. Falls back to Ollama if sidecar fails to start.
5. **Document.** README section: "Using CodeExecutor for higher-quality fork clustering" with Docker instructions, GPU setup notes, and known-language caveats.
6. **Performance-test.** Measure throughput on a large fork set (≥ 200 forks) and document the cost/benefit tradeoff so users can choose informedly.

## References

- Perera, A., Pillai, B., Tharani, J. S., Rao, A. S., & Muthukkumarasamy, V. (2026). *CodeBERT-Based Embeddings for Detecting Vulnerable Smart Contracts.* IEEE.
- Liu, C., Lu, S., Chen, W., Jiang, D., Svyatkovskiy, A., Fu, S., Sundaresan, N., & Duan, N. (2023). *Code Execution with Pre-trained Language Models.* arXiv:2305.05383.
- Bui, N. D. Q., Yu, Y., & Jiang, L. (2021). *Self-Supervised Learning for Code Retrieval and Summarization through Semantic-Preserving Program Transformations.* arXiv:2009.02731v4.
