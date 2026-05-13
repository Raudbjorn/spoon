# Behavioral Embeddings Validation — Panel Re-Test Results

> **TEMPLATE — pipeline running on vinbonesjr as of 2026-05-13. `<FILL>` markers are placeholders for measured numbers that the closing step of the plan substitutes in.**

**Date:** 2026-05-13
**Spec:** `docs/superpowers/specs/2026-05-12-behavioral-embeddings-panel-design.md`
**Plan:** `docs/superpowers/plans/2026-05-12-behavioral-embeddings-panel.md`
**Phase A reference:** `experiments/started/behavioral-embeddings/RESULTS.md`
**Run on:** vinbonesjr (58 GB RAM, CPU-only; AMD Raphael + Intel Arc A770 hardware, Ollama running CPU-build)

## TL;DR

<FILL: 2-3 sentences. State the best candidate, its delta vs nomic baseline, and the verdict from the decision matrix.>

## Panel results

All panel members and the baseline scored against the same 53 hand-curated PR pairs (27 same-intent, 26 different-intent) using Kendall's tau-c (binary-label variant).

| model | params | context | τ_panel | Δ vs nomic | n pairs kept | gate verdict |
| --- | --- | --- | --- | --- | --- | --- |
| **nomic-embed-text-v1** (baseline) | 137M | 8K | <FILL> | — | <FILL>/53 | — |
| **nomic-ai/CodeRankEmbed** | 137M | 8K | <FILL> | <FILL> | <FILL>/53 | <PASS/FAIL/BORDERLINE> |
| **jinaai/jina-embeddings-v2-base-code** | 161M | 8K | <FILL> | <FILL> | <FILL>/53 | <PASS/FAIL/BORDERLINE> |
| **Salesforce/SFR-Embedding-Code-400M_R** | 400M | 32K | <FILL> | <FILL> | <FILL>/53 | <PASS/FAIL/BORDERLINE> |
| **Snowflake/snowflake-arctic-embed-l-v2.0** | 568M | 8K | <FILL> | <FILL> | <FILL>/53 | <PASS/FAIL/BORDERLINE> |
| **jazzcort/nomic-embed-code-Q6_K** | 7B (Q6_K) | 8K | <FILL> | <FILL> | <FILL>/53 | <PASS/FAIL/BORDERLINE> |

## Decision-matrix outcome

Best delta: **<FILL>**. Decision-matrix row: **<FILL: one of "≥+0.15", "+0.05 to +0.15", "-0.02 to +0.05", "<-0.02">**.

**Verdict: <FILL: "PASS at acceptance" / "PASS at gate" / "BORDERLINE — escalating" / "FAIL — second-pass rejection">**

Action:
- <FILL: one of the four branch actions from plan Task 10>

## Substitutions from the spec

The original spec listed 4 panel candidates. Two substitutions were necessary mid-execution:

1. **`nomic-ai/nomic-embed-code` (originally specced as 137M)** → replaced with **`nomic-ai/CodeRankEmbed`** (137M encoder, 8K context, MIT, same lab). The originally-specced name resolves to a 7B-parameter decoder model on HF (~27 GB on disk in fp16), which OOM'd the 14 GB local box. CodeRankEmbed preserves the hypothesis we wanted to test ("recent code-fine-tuned encoder from the same lab as our baseline beats nomic-embed-text") at a tractable scale.

2. **Added a 5th panel slot: `jazzcort/nomic-embed-code-Q6_K`** (a community Ollama port of the original 7B nomic-embed-code, Q6_K quantization → 5.8 GB on disk, ~6 GB RAM resident). This recovers the originally-specced architecture (decoder-as-embedder) for testing without requiring fp16 weights. Q6_K is well-known to preserve embedding quality within ~1% of fp16 on benchmarks.

## Plan deviations

Mid-execution adaptations beyond the substitutions above:

1. **Migrated to vinbonesjr** from the 14 GB local box. Local hit OOM on jina-embeddings-v2-base-code (batch-size=4) and on the original 7B nomic-embed-code. vinbonesjr's 58 GB RAM accommodated batch-size=16 across all panel members.

2. **Switched nomic baseline from Ollama-served to HF transformers.** Ollama's `/api/embeddings` endpoint does not honor `num_ctx` for `nomic-embed-text` — it 500s on prompts above its compiled-in context limit regardless of the option. Verified empirically across `num_ctx ∈ {2048, 8192, 16384}`. Loading `nomic-ai/nomic-embed-text-v1` via HF transformers lets the experiment use the model's native 8192-token window deterministically. Same weights, same architecture; just a different serving path.

3. **`vishalraj/nomic-embed-code` is NOT a code embedder.** Probed early in execution. GGUF metadata showed it's `nomic-bert` (the standard nomic-embed-text encoder), `Q2_K` quantized to 49 MB, with BERT-base-uncased vocab — i.e., a brutally-quantized version of nomic-embed-text with a misleading name. Cosine vs the f16 baseline on the same input is 0.12–0.18 (random would be ~0; identical-model would be ~1.0). Not included in the panel.

4. **Intel Arc A770 GPU on vinbonesjr is not utilized.** The system's Ollama is the stock CPU build (no oneAPI/SYCL/Vulkan backend linked). The Arc A770 is present hardware-wise (lspci shows it; `intel_gpu_top` is installed) but enabling it would require a custom Ollama build with `OLLAMA_LLM=vulkan` or `=sycl`. CPU on 58 GB RAM was sufficient for this experiment; GPU acceleration was deferred as future work.

## Comparison to Phase A

| metric | Phase A (5KB cap, CodeBERTa) | Phase B panel |
| --- | --- | --- |
| nomic τ | −0.0085 | <FILL> |
| best candidate τ | −0.0911 (CodeExecutor) | <FILL> (<FILL: model name>) |
| best Δ (candidate − nomic) | −0.0826 | <FILL> |
| best Δ direction | candidate lost by 0.08 | <FILL: candidate won/lost by FILL> |

<FILL: 1-2 sentence interpretation of what changed between Phase A and Phase B.>

## Methodology summary

- **Dataset:** 200 PRs from `IBM/mcp-context-forge`, 53 hand-curated pairs. Same fixture as Phase A; no changes to `features.json`, `judgments.json`, or `pr_states.json`.
- **Prompts:** Full per-modality content (no 5 KB cap from Phase A). Each tokenizer truncates at its native `model_max_length` (capped at 32K for safety). nomic-via-Ollama-was-removed path described above.
- **Pooling:** Mean-pooling on last_hidden_state (all panel members are encoders). For the decoder-style jazzcort/nomic-embed-code-Q6_K, Ollama handles internal pooling.
- **Analysis:** SciPy's `kendalltau(variant='c')` between cosine-similarity ranking of pairs and binary judgment vector. Same as Phase A.

## Reproduce

```sh
ssh vinbonesjr
cd ~/projects/spoon/spoon-2/experiments/started/behavioral-embeddings
./run_remote_panel.sh
.venv/bin/python analyze.py --nomic nomic_vectors.json --codeexecutor <CANDIDATE>_vectors.json --out <CANDIDATE>_results.md
```

`run_remote_panel.sh` orchestrates all 6 embedding runs (baseline + 5 panel candidates) sequentially.

## Future supplements

Two additional evaluation datasets were considered but not run in this experiment. Their inclusion would strengthen (or weaken) the verdict on a different axis:

1. **CRAVE** (1,174 PR patches, APPROVE/REQUEST_CHANGES labels). Binary classification axis. Plan: `docs/superpowers/plans/2026-05-13-behavioral-embeddings-crave-supplement.md`. Triggered conditional on the panel verdict (see decision matrix).
2. **MULocBench** (3,052 GitHub issues, file localization). Retrieval axis. Closer structural fit to spoon's actual fork-clustering use case than CRAVE. Reserved as a third-axis tiebreaker for borderline cases.

---

*Pipeline status: <FILL: in progress / complete>*
*Last updated: <FILL: timestamp>*
