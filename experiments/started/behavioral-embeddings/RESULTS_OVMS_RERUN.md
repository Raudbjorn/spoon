# Behavioral Embeddings — OVMS Serving-Backend Re-run

> **Status: informational. The Phase B verdict (Snowflake/snowflake-arctic-embed-l-v2.0) is unchanged. This document does not re-open the supplement decision** — it answers a narrower, orthogonal question: *does the bias-corrected supplement verdict survive a change of serving backend, from in-process torch+XPU to OpenVINO Model Server?*

**Date:** 2026-06-06
**Branch:** `claude/ovms-arc-a770-supplement-rerun-n53` (forks the bias-corrected `claude/huggingface-connector-integration-jqQs1` at `88e80b6`)
**Predecessors:** `RESULTS_PANEL_SUPPLEMENT.md` (bias-corrected supplement, load-bearing), `RESULTS_PANEL.md` (panel verdict, final)
**Serving:** OpenVINO Model Server `2026.0.0` (backend `2026.0.0.0rc3`) over OpenAI-compat `/v3/embeddings`, Intel Arc A770 (`target_device=GPU`)

## Why this re-run exists

The first OVMS pass (`RESULTS_PANEL_SUPPLEMENT.md` predecessor draft / the `…-embeddings-supplement` branch) reported both candidates **PASS** — but it forked *before* the supplement's two bias corrections landed and silently reproduced the inflated picture: n≤34 coverage against an Ollama-only baseline. Measured that way, any candidate looks strong.

This branch closes both gaps using the **same methodology the bias-corrected supplement established**, then routes embeddings through OVMS instead of torch+XPU. The comparison is now apples-to-apples: same 53-pair fixture, same HF-served nomic baseline, full coverage.

## Headline (fair n=53, HF-served baseline)

| candidate | params | pooling | Δ vs Ollama-nomic (n=35) | Δ vs **HF-nomic (n=53)** | gate (HF, n=53) |
| --- | --- | --- | --- | --- | --- |
| `Qwen/Qwen3-Embedding-0.6B` (int8 IR) | 596M | `last_token` | +0.183 ✅ | **+0.023** | ❌ FAIL (thin) |
| `ibm-granite/granite-embedding-311m-multilingual-r2` | 312M | `mean` | +0.157 ✅ | **−0.094** | ❌ FAIL |

Baselines: τ_Ollama-nomic = 0.340 (n=35); τ_HF-nomic = 0.179 (n=53). Gate threshold Δ ≥ +0.05.

Coverage: **all four vector sets reach 200/200** (qwen3, granite, nomic-HF baseline). Full coverage came from patching `truncate: true` into each model's `graph.pbtxt` — OVMS's `EmbeddingsCalculatorOV` defaults to `truncate: false` and returns HTTP 400 on any prompt exceeding the model's context window (the corpus has a 332 K-char outlier, `pr-4264`). With truncation enabled the Arc A770 handles every pair on-GPU — **no CPU fallback needed**, unlike the torch+XPU path.

## The actual finding: the verdict is robust to serving backend

Cross-tabulated against the torch+XPU numbers from the bias-corrected supplement (`RESULTS_PANEL_SUPPLEMENT.md`), at fair n=53 / HF-baseline:

| candidate | torch+XPU (fp16/fp32) | **OVMS (int8 IR)** | agree? |
| --- | --- | --- | --- |
| Qwen3-0.6B | Δ=+0.054 ✅ thin pass | Δ=+0.023 ❌ thin fail | both marginal, straddle the gate |
| granite-r2 | Δ=−0.077 ❌ | Δ=−0.094 ❌ | **both FAIL** |

- **granite-r2 fails on both backends.** The bias-corrected supplement's verdict ("granite's PASS was an artifact") is confirmed independently of the serving path.
- **Qwen3-0.6B is genuinely marginal.** torch+XPU placed it a hair *above* the gate (+0.054); OVMS places it a hair *below* (+0.023). The ~0.03 gap is the load-bearing nuance: the OVMS run uses the **int8-quantized** `OpenVINO/Qwen3-Embedding-0.6B-int8-ov` IR, while the torch path runs the model at fp16/fp32. A ~0.03 τ drop from int8 quantization is plausible and pushes a borderline model below the line. This is a concrete, actionable datapoint for sidecar architecture: **int8 OVMS serving can flip a marginal embedder from thin-pass to thin-fail.** If Qwen3 were ever a Phase-B candidate, it would need fp16 IR (`OpenVINO/Qwen3-Embedding-0.6B-fp16-ov`) to preserve the torch-measured margin.

The earlier OVMS branch's PASS/PASS table was not measuring a stronger result — it was measuring an easier subset against a weaker baseline. Re-run on equal footing, OVMS *agrees* with the bias-corrected verdict.

## Cross-doc τ caveat (carried forward)

τ_HF-nomic here is 0.179 (n=53); the supplement reported 0.159; the panel reported 0.088. Same weights, same fixture, same `mean` pooling — the deltas are `transformers`-version drift on `nomic-embed-text-v1`'s `trust_remote_code` path (panel 4.47.1 → supplement/here 4.57.6) plus minor pair-survival differences. **Within this document all candidates are scored against the same 0.179 baseline, so the deltas are internally fair.** Cross-doc comparison against arctic-l-v2's panel τ remains not strictly apples-to-apples — see `RESULTS_PANEL_SUPPLEMENT.md` "Cross-doc τ caveat".

## What OVMS contributes (operational, not scientific)

The science verdict is the supplement's. What this branch adds is a **production-shaped serving path** and the plumbing to make it work on the `python_off` OVMS build:

- `embed_ovms.py` — OpenAI-compat `/v3/embeddings` client, batching + resume, identical on-disk vector format to `embed_hf.py` (so `analyze.py` consumes either). 7 unit tests in `embed_ovms_test.py`.
- `setup_ovms_models.sh` — `ovms --pull` + `--add_to_config` + pooling-patch, delegates to the packaged `/usr/lib/ovms/contrib/setup_embeddings_arc.sh` when present.
- `convert_tokenizers.py` / `convert_models.py` — generate the `openvino_tokenizer.{xml,bin}` + `openvino_model.{xml,bin}` artifacts the `python_off` build can't (it lacks `optimum-cli`); pull pre-converted IR from the `OpenVINO/` HF org where it exists (Qwen3).
- `run_panel_supplement_ovms.sh` — the OVMS-served analogue of `run_panel_supplement.sh`, wired to the n=53 / dual-baseline analysis loop.

### Pitfalls surfaced (any future OVMS-served sidecar must solve these)

1. **`truncate: false` is the default** → HTTP 400 on long prompts. Patch `graph.pbtxt` to `truncate: true`, or the long-PR pairs silently drop and re-introduce selection bias.
2. **Slash in model names** (`Qwen/Qwen3-Embedding-0.6B`) → OVMS routing returns 400 "Invalid request URL". URL-encode `/` to `%2F` for `/v2/models/<name>/ready`.
3. **Batch cross-padding** — a 332 K-char outlier batched at `--batch-size 4` pads the whole batch to its token length and can 400 even with truncation. Fill the residue at `--batch-size 1`.
4. **`python_off` build has no `optimum-cli`** → `--weight-format int8` fails instantly; tokenizer/model IR must be generated client-side (`convert_*.py`).
5. **`config.json` ownership** — `ovms --add_to_config` writes the file; ship it `0664 root:ovms` (see the `ovms` package's `ovms.tmpfiles`).

## OpenVINO version note (2026-06-06)

This run used OVMS **2026.0.0** with a **release-candidate backend** (`2026.0.0.0rc3`). Two stable releases have shipped since: **2026.1** (pre-converted models under `hf.co/OpenVINO`, HF download retry/resume) and **2026.2** (explicit Arc A770 / Xe-GPU support, fixed `/v2/health/ready` semantics, HF download checkpoint resume). Three of this branch's workarounds — hand-rolled readiness probing, no pull-resume after the disk-full incident, and Arc not being a first-class target — are upstream-fixed in 2026.2. Bumping the `ovms` package to 2026.2 is recommended (asset name changed to `ovms_ubuntu24_2026.2.0_python_off.tar.gz`); it does **not** eliminate the `convert_*.py` helpers, which remain necessary on any `python_off` build until nomic/granite land in the `OpenVINO/` org.

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
# 1. OVMS must serve the three embedding models (see setup_ovms_models.sh).
#    Patch truncate:true into each model's graph.pbtxt, then restart ovms.
# 2. Embed candidates via OVMS (creates the slim .venv-ovms from requirements.txt):
OVMS_SKIP_PULL=1 ./run_panel_supplement_ovms.sh
# 3. Build the HF-served nomic baseline in a torch+XPU venv (requirements-hf.txt):
uv venv .venv-hf && uv pip install --python .venv-hf/bin/python -r requirements-hf.txt
LD_LIBRARY_PATH="$PWD/.venv-hf/lib:$LD_LIBRARY_PATH" .venv-hf/bin/python embed_hf.py \
  --model nomic-ai/nomic-embed-text-v1 --features features.json \
  --out nomic_hf_vectors.json --cache-dir hf_cache --batch-size 4 --pooling mean
# 4. Score both candidates against both baselines (analyze.py needs only .venv-ovms):
for cand in qwen3_06b granite_311m_r2 ; do
  for base in nomic nomic_hf ; do
    .venv-ovms/bin/python analyze.py --nomic ${base}_vectors.json \
      --codeexecutor ${cand}_vectors.json --out ${cand}_vs_${base}_ovms.md
  done
done
```
