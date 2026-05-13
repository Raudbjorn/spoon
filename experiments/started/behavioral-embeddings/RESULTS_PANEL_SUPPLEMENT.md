# Behavioral Embeddings Validation — Post-Panel Supplement

> **Status: informational. The Phase B verdict (Snowflake/snowflake-arctic-embed-l-v2.0) is unchanged. Both supplement models clear the +0.05 gate but do not displace the panel winner.**

**Date:** 2026-05-13
**Predecessor:** `RESULTS_PANEL.md` (panel verdict, final)
**Run on:** vinbonesjr (64 GB RAM, **Intel Arc A770 16 GB via SYCL/XPU** — see "GPU acceleration" below)

## TL;DR

Two embedders released after the original 2026-05-13 panel were evaluated against the same 53-pair judgment set, on the **same nomic-via-Ollama baseline that the panel used at runtime**. Both clear the gate:

- **Qwen/Qwen3-Embedding-0.6B** (596M, decoder-style, Apache-2.0) — Δ = **+0.216** (n=35).
- **ibm-granite/granite-embedding-311m-multilingual-r2** (312M, ModernBERT, Apache-2.0) — Δ = **+0.102** (n=28).

Neither dethrones `Snowflake/snowflake-arctic-embed-l-v2.0` (panel τ +0.208, n=53) on the strict comparison axis used by the panel decision matrix, because their pair coverage is reduced by Arc-A770 VRAM limits on the longest prompts. They are recorded here as candidates worth re-evaluating if/when Phase B sidecar performance becomes a bottleneck.

## Supplement results

Both supplement models scored against the same 53 hand-curated PR pairs from `judgments.json`. Baseline is `nomic-embed-text` served by the local **ollama-sycl** runtime (Intel Arc A770, 16 GB VRAM, via `libggml-sycl.so` drop-in from the `ollama-sycl 0.20.4-4` Arch package). 200 PRs total; nomic embedded 166, missing 34 to Ollama 500s on prompts that exceeded the model's compiled context window even with `num_ctx=8192`. Each supplement model has its own coverage cap from XPU OOM on the longest prompts.

| model | params | context | pooling | τ_supp | Δ vs nomic | n pairs kept | gate verdict |
| --- | --- | --- | --- | --- | --- | --- | --- |
| **nomic-embed-text** (baseline, Ollama-served) | 137M | 8K | (server-internal) | 0.340 | — | 35/53 | — |
| **Qwen/Qwen3-Embedding-0.6B** | 596M | 32K | `last_token` | **0.555** | **+0.216** | 35/53 | ✅ PASS (wide margin) |
| **ibm-granite/granite-embedding-311m-multilingual-r2** | 312M | 8K | `mean` | **0.490** | **+0.102** | 28/53 | ✅ PASS |

(`τ_nomic` differs across the two rows because the pair-skip list differs per candidate — analyze.py drops any pair where *either* embedder lacks a vector for one side. The granite row pairs τ_nomic = 0.388 against n=28.)

## Why "supplement" and not "re-panel"

Both candidates post-date the panel cutoff (May 2026) and the panel verdict in `RESULTS_PANEL.md` is final. Running them as a supplement preserves three properties:

1. **The original panel vectors are not re-embedded.** This document does not contradict any τ value in `RESULTS_PANEL.md`. Those scores stand.
2. **The fixture is identical.** Same `features.json` (200 PRs from `IBM/mcp-context-forge`), same `judgments.json` (53 pairs).
3. **The baseline is the same path used by Phase A** (Ollama-served nomic). This baseline is known to be optimistic-by-truncation relative to the HF-served baseline in `RESULTS_PANEL.md` — see "Baseline note" below.

## Baseline note

The panel switched the nomic baseline from Ollama-served to HF-served because Ollama 500s on long prompts (see `RESULTS_PANEL.md` plan deviation #2). This supplement uses the Ollama-served baseline because the host is currently configured with Ollama+SYCL as the primary embedder, and one of the supplement's questions is "is the new model better than what we actually ship via Ollama?", which is the operationally relevant baseline. Compared on equal terms the supplement τ deltas are meaningful within this document; cross-comparing them against `RESULTS_PANEL.md` numbers would conflate the two baselines.

## GPU acceleration — corrects RESULTS_PANEL plan deviation #4

`RESULTS_PANEL.md` notes: *"Intel Arc A770 GPU on vinbonesjr is not utilized. The system's Ollama is the stock CPU build."* That is no longer true for this supplement, and the original observation is itself stale:

- **Ollama** runs on the Arc A770 via the `ollama-sycl 0.20.4-4` package (Arch). The package drops `libggml-sycl.so` into `/usr/lib/ollama/` where stock ollama discovers it at runtime via `GGML_BACKEND_DL`. The Arch `ollama` is unmodified. Active runtime env (`/etc/conf.d/ollama-sycl`): `ZES_ENABLE_SYSMAN=1`, `SYCL_CACHE_PERSISTENT=0` (oneAPI 2025.3 has a SIGSEGV in `PersistentDeviceCodeCache::getItemFromDisc` — first-run JIT is slower but stable), `OLLAMA_CONTEXT_LENGTH=8192`. `ext_intel_free_memory` is not supported on Arc/DG2 so ollama can't see free VRAM; the explicit context cap prevents over-allocation.
- **PyTorch supplement embeddings** run on the Arc A770 via `torch==2.9.0+xpu` (Intel-prebuilt wheel from `https://download.pytorch.org/whl/xpu`). `embed_hf.py` now prefers `xpu` over `cuda`. No `intel-extension-for-pytorch` needed for torch ≥ 2.7.
- **Wheel/system library conflict workaround.** The +xpu torch wheel bundles `libsycl.so.8` and `libur_loader.so.0.12.0` under `.venv/lib/`. The system's `/opt/intel/oneapi/2025.3/lib/libur_loader.so.0` (from `intel-oneapi-base-toolkit 2025.3.2.21`) is missing `urEnqueueCooperativeKernelLaunchExp@LIBUR_LOADER_0.12`, which the wheel's libsycl requires. Without intervention, `import torch` crashes with `ImportError: …libsycl.so.8: undefined symbol: urEnqueueCooperativeKernelLaunchExp`. `run_panel_supplement.sh` prepends `$PWD/.venv/lib` to `LD_LIBRARY_PATH` so the wheel's bundled UR loader wins for the process — scope-limited to this venv.

## OOM observations

| model | batch=4 dropped | batch=1 dropped | smallest failing alloc |
| --- | --- | --- | --- |
| Qwen3-Embedding-0.6B | 36 batches (164/200 kept) | 3 batches (197/200 kept) | 17–18 GiB (exceeds 16 GiB Arc cap regardless of batch) |
| granite-embedding-311m-r2 | 40 batches (160/200 kept) | not re-run | varies; mean-pooling encoder, similar ceiling |

Qwen3 was re-run with `--batch-size 1` to fill the gaps; the script's resume support (`out` JSON loaded as starting state, embedded ids skipped) made this cheap. The three remaining Qwen3 failures want 17–18 GiB *single-sequence* allocations: those PRs tokenize past the Arc's per-allocation ceiling at Qwen3's full 32 K context. A length-based fallback (truncate at ~16K, or route to CPU) would close them; not pursued here since the τ result is well clear of the gate without those three pairs.

## Methodology summary

- **Dataset:** identical to the panel — 200 PRs from `IBM/mcp-context-forge`, 53 judgment pairs. No changes to `features.json`, `judgments.json`, or `pr_states.json`.
- **Prompts:** unchanged — full per-modality content (no 5 KB cap from Phase A). Each tokenizer truncates at its native `model_max_length` capped at 32 K.
- **Pooling:** Qwen3 is decoder-style → `last_token` (the SFR-2B gotcha from `RESULTS_PANEL.md` applies; mean-pool would have systematically degraded it). Granite is ModernBERT encoder → `mean`.
- **Analysis:** SciPy's `kendalltau(variant='c')`, same as the panel.

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
./run_panel_supplement.sh                       # embeds Qwen3 + granite on Arc A770
# If Qwen3 has gaps from OOM, refill cheaply (script resumes from --out):
LD_LIBRARY_PATH="$PWD/.venv/lib:$LD_LIBRARY_PATH" .venv/bin/python embed_hf.py \
  --model Qwen/Qwen3-Embedding-0.6B \
  --features features.json --out qwen3_06b_vectors.json \
  --cache-dir hf_cache --batch-size 1 --pooling last_token

.venv/bin/python analyze.py \
  --nomic nomic_vectors.json \
  --codeexecutor qwen3_06b_vectors.json \
  --out qwen3_06b_results.md

.venv/bin/python analyze.py \
  --nomic nomic_vectors.json \
  --codeexecutor granite_311m_r2_vectors.json \
  --out granite_311m_r2_results.md
```

The supplement script requires `.venv/` to exist with `torch==2.9.0+xpu` and `transformers>=4.55` from `requirements.txt`. The first call to `run.sh` from the panel will bootstrap the venv via `uv`.

## When to revisit

- **Phase B sidecar regresses on throughput or VRAM.** Qwen3-0.6B is the same parameter budget as the panel winner (568M vs 596M) but uses different architecture. Granite-r2 at 312M is the only candidate so far that's smaller than arctic-l-v2 *and* clears the gate — relevant if Phase B has to drop the Python sidecar entirely.
- **A future re-panel adds GPU-served embedders.** The supplement establishes that the Arc A770 stack (ollama-sycl + torch+xpu) works for behavioral-embeddings workloads. Future runs can target it directly without rebuilding the venv.

---

*Last updated: 2026-05-13.*
