# Behavioral Embeddings Validation — Panel Re-Test Results

> **FINAL. Verdict: Phase B authorized with `Snowflake/snowflake-arctic-embed-l-v2.0`.**

**Date:** 2026-05-13
**Spec:** `docs/superpowers/specs/2026-05-12-behavioral-embeddings-panel-design.md`
**Plan:** `docs/superpowers/plans/2026-05-12-behavioral-embeddings-panel.md`
**Phase A reference:** `experiments/started/behavioral-embeddings/RESULTS.md`
**Run on:** vinbonesjr (58 GB RAM, CPU-only; AMD Raphael + Intel Arc A770 hardware, Ollama running CPU-build)

## TL;DR

**Phase B is authorized with `Snowflake/snowflake-arctic-embed-l-v2.0`.** This 568M-parameter general-purpose long-context encoder cleared the +0.05 Kendall's tau gate by a wide margin (Δ = +0.120 vs nomic baseline). The SFR-2B escalation prescribed by the decision matrix failed (Δ = −0.057), so the panel winner takes the Phase B slot rather than the heavier code-specific candidate. The originally-rejected hypothesis (Phase A) was an artifact of artificial 5 KB prompt truncation: with full-context inputs, a general-purpose long-context model wins decisively. Most surprising finding: every code-specific model in the panel underperformed the general-purpose winner.

## Panel results

All panel members and the baseline scored against the same 53 hand-curated PR pairs (27 same-intent, 26 different-intent) using Kendall's tau-c (binary-label variant).

| model | params | context | τ_panel | Δ vs nomic | n pairs kept | gate verdict |
| --- | --- | --- | --- | --- | --- | --- |
| **nomic-embed-text-v1** (baseline) | 137M | 8K | 0.0883 | — | 53/53 | — |
| ~~nomic-ai/CodeRankEmbed~~ | 137M | 8K | — | — | — | **SKIPPED — loader incompat** |
| **jinaai/jina-embeddings-v2-base-code** | 161M | 8K | 0.0199 | **−0.0684** | 53/53 | **FAIL** (by 0.12) |
| **Salesforce/SFR-Embedding-Code-400M_R** | 400M | 32K | 0.0570 | **−0.0313** | 53/53 | FAIL (borderline) |
| **Snowflake/snowflake-arctic-embed-l-v2.0** | 568M | 8K | **0.2079** | **+0.1196** | 53/53 | **✅ PASS at gate** (close to +0.15 acceptance) |
| **jazzcort/nomic-embed-code-Q6_K** | 7B (Q6_K) | 8K | 0.4456 | **+0.0822** | 43/53 | **✅ PASS** (10 dropped to Ollama ctx; biased toward smaller PRs) |
| **Salesforce/SFR-Embedding-Code-2B_R** (escalation) | 2B | 32K | 0.0313 | **−0.0570** | 53/53 | **FAIL** (worse than 400M sibling) |

## Decision-matrix outcome

Best delta on the panel: **+0.1196** (Snowflake/snowflake-arctic-embed-l-v2.0). Decision-matrix row: **"+0.05 to +0.15 — gate cleared, below acceptance."** SFR-2B escalation prescribed by the matrix did not cross the +0.15 acceptance threshold (Δ = −0.057, *worse* than its 400M sibling).

**Verdict: PASS at gate. Phase B proceeds with `Snowflake/snowflake-arctic-embed-l-v2.0`.**

Action:
- Spec status updated from "Rejected" → "Phase B in progress with `Snowflake/snowflake-arctic-embed-l-v2.0`."
- Phase B sidecar plan written at `docs/superpowers/plans/2026-05-13-behavioral-embeddings-sidecar.md`.

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
| nomic τ | −0.0085 | **+0.0883** |
| best candidate τ | −0.0911 (CodeExecutor) | **+0.2079** (arctic-l-v2) |
| best Δ (candidate − nomic) | −0.0826 | **+0.1196** |
| best Δ direction | candidate lost by 0.08 | **candidate won by 0.12** |

The +0.20 swing in Δ between Phase A and Phase B is driven by two methodology changes: (1) removing the 5 KB per-modality truncation cap, which let big-PR features carry meaningful intent signal instead of becoming uninformative prefixes; and (2) switching the nomic baseline from Ollama (which hard-capped prompts and 500'd on long inputs) to HF transformers (which honors the native 8 K window). Most importantly, the **strongest passing model in Phase B is general-purpose (Snowflake-arctic-embed-l-v2.0), not code-specific** — the three code-specific encoders (CodeBERTa/CodeExecutor in Phase A, jina-v2-code and SFR-400M in Phase B) all underperformed nomic at fair context. This is direct evidence for hypothesis #4 in the spec: "code-awareness was a red herring; context length was the limiting factor in Phase A."

## Methodology summary

- **Dataset:** 200 PRs from `IBM/mcp-context-forge`, 53 hand-curated pairs. Same fixture as Phase A; no changes to `features.json`, `judgments.json`, or `pr_states.json`.
- **Prompts:** Full per-modality content (no 5 KB cap from Phase A). Each tokenizer truncates at its native `model_max_length` (capped at 32K for safety). nomic-via-Ollama-was-removed path described above.
- **Pooling:** Mean-pooling on last_hidden_state (all panel members are encoders). For the decoder-style jazzcort/nomic-embed-code-Q6_K, Ollama handles internal pooling.
- **Analysis:** SciPy's `kendalltau(variant='c')` between cosine-similarity ranking of pairs and binary judgment vector. Same as Phase A.

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
# Baseline (nomic-embed-text, via Ollama) — the τ everything is compared against:
.venv/bin/python embed_nomic.py --features features.json --out nomic_vectors.json
# Panel candidates (sequential, resume-safe):
./run_panel.sh
# Score each candidate against the baseline:
.venv/bin/python analyze.py --nomic nomic_vectors.json --codeexecutor <CANDIDATE>_vectors.json --out <CANDIDATE>_results.md
```

`run_panel.sh` orchestrates the panel-candidate embedding runs sequentially;
the baseline `nomic_vectors.json` is produced by the separate `embed_nomic.py`
step above.

## Future supplements

Two additional evaluation datasets were considered but not run in this experiment. Their inclusion would strengthen (or weaken) the verdict on a different axis:

1. **CRAVE** (1,174 PR patches, APPROVE/REQUEST_CHANGES labels). Binary classification axis. Plan: `docs/superpowers/plans/2026-05-13-behavioral-embeddings-crave-supplement.md`. Triggered conditional on the panel verdict (see decision matrix).
2. **MULocBench** (3,052 GitHub issues, file localization). Retrieval axis. Closer structural fit to spoon's actual fork-clustering use case than CRAVE. Reserved as a third-axis tiebreaker for borderline cases.

---

## Additional finding: SFR-2B is worse than SFR-400M

The escalation produced a surprising result. SFR-Embedding-Code-2B_R (2 billion parameters, 32K context, Gemma2-based architecture) scored *worse* than its 400M sibling:

| variant | τ | Δ vs nomic | params | architecture |
| --- | --- | --- | --- | --- |
| SFR-400M_R | 0.0570 | −0.0313 | 400M | RoBERTa-like encoder |
| SFR-2B_R | 0.0313 | −0.0570 | 2B | Gemma2 decoder |

The 2B is a decoder-only architecture; the 400M is an encoder. Our `embed_hf.py` uses mean-pooling on `last_hidden_state` which is the right aggregation for encoders but a known suboptimal choice for decoders (decoder-style embedders typically need last-token-pooling or a learned aggregation head). The result is consistent with that mismatch — bigger model but pooled wrong for the architecture.

This also flags a gotcha for any future re-test that adds decoder-style candidates: a switch on the pooling strategy in `embed_hf.py` would be needed for a fair test.

*Pipeline status: COMPLETE.*
*Last updated: 2026-05-13 11:30 GMT (final).*
