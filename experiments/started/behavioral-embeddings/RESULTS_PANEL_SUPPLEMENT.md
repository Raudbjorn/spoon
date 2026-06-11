# Behavioral Embeddings Validation — Post-Panel Supplement

> **Status: informational. The Phase B verdict (Snowflake/snowflake-arctic-embed-l-v2.0) is unchanged.**
>
> At fair n=53 against the panel's HF-served baseline:
> - **Qwen/Qwen3-Embedding-0.6B** clears the gate by a thin margin (Δ=+0.054).
> - **ibm-granite/granite-embedding-311m-multilingual-r2** *fails* the gate (Δ=−0.077).
> - **Salesforce/SFR-Embedding-Code-2B_R** at correct `last_token` pooling fails (Δ=−0.028) but quadruples the panel's mean-pool τ (0.031 → 0.131), confirming the panel's pooling-mismatch hypothesis.
>
> An earlier draft of this document showed all candidates passing with Ollama-served baseline at n=35 — that picture was inflated by selection bias on the dropped pairs *and* a weaker baseline. Both effects are corrected below.

**Date:** 2026-05-14 (first draft 2026-05-13; revised after code review surfaced two caveats)
**Predecessor:** `RESULTS_PANEL.md` (panel verdict, final)
**Run on:** vinbonesjr (64 GB RAM, **Intel Arc A770 16 GB via SYCL/XPU** — see "GPU acceleration" below)

## TL;DR

Three embedders are scored against the same 53-pair judgment set, each against **two** nomic baselines — the Ollama-served one the host actually ships at runtime, and the HF-served one the panel used in `RESULTS_PANEL.md`. Higher coverage and the panel-equivalent baseline give the load-bearing numbers; the Ollama row is kept for production-parity framing only.

| candidate | params | pooling | τ vs Ollama-nomic (n=35) | Δ | τ vs HF-nomic (n=53) | Δ | gate (HF, n=53) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `Qwen/Qwen3-Embedding-0.6B` | 596M | `last_token` | 0.555 | +0.216 | 0.214 | **+0.054** | ✅ PASS (thin) |
| `ibm-granite/granite-embedding-311m-multilingual-r2` | 312M | `mean` | 0.438 | +0.098 | 0.083 | **−0.077** | ❌ FAIL |
| `Salesforce/SFR-Embedding-Code-2B_R` (panel re-test, corrected pooling) | 2B | `last_token` | 0.340 | 0.000 | 0.131 | **−0.028** | ❌ FAIL |

Baselines for reference: τ_Ollama-nomic = 0.340 (n=35); τ_HF-nomic (this supplement) = 0.159 (n=53). The supplement's HF-nomic τ does **not** match the panel's reported τ_nomic = 0.088 — same model weights, same fixture, but `transformers` was bumped from 4.47.1 (panel) to 4.57.6 (supplement, required by Qwen3 / granite-r2), and `nomic-embed-text-v1` uses `trust_remote_code`. See "Cross-doc τ caveat" below — direct comparison against `Snowflake/snowflake-arctic-embed-l-v2.0` from `RESULTS_PANEL.md` is still not strictly apples-to-apples.

**Panel verdict (arctic-l-v2) stands.** Qwen3-0.6B is the only supplement model that clears the gate at n=53, and only barely — well below the panel winner's +0.120 margin under the panel's own framework version.

## Headline findings

### 1. Selection bias was real and substantial

The earlier draft of this document reported Qwen3 Δ=+0.216 and granite Δ=+0.098 against Ollama-nomic at n=35. Those were upper bounds: the dropped 18 pairs were precisely the longest-prompt PRs (17–27 GiB single-sequence VRAM allocations on the Arc A770's 16 GiB ceiling), and those pairs are harder to rank — they carry more files and more low-signal tokens per intent. Adding a length-based CPU fallback (this revision) closes the n=35 → n=53 gap and pulls both candidates' deltas in the conservative direction:

| candidate | Δ at n=35 (Ollama-baseline, batch=4) | Δ at n=53 (Ollama-baseline) | Δ at n=53 (HF-baseline) |
| --- | --- | --- | --- |
| Qwen3-0.6B | +0.216 | (n_max=35; Ollama baseline 500s on 18 long PRs) | **+0.054** |
| granite-311m-r2 | +0.098 | (same cap) | **−0.077** |

For both candidates: each step of the methodology fix (CPU fallback for candidates, HF baseline for fair n=53 comparison) shaves Δ further. Granite crosses below zero. The selection-bias caveat the reviewer raised was precisely correct — and quantitatively much stronger than initially framed.

### 2. SFR-2B pooling correction confirmed

`RESULTS_PANEL.md` flagged SFR-2B as a likely pooling-strategy mismatch: a 2B Gemma2 decoder was scored at `--pooling mean` and lost (Δ=−0.057), worse than its 400M encoder sibling. The supplement re-tests SFR-2B with `--pooling last_token` (architecturally correct for a decoder-as-embedder):

| run | pooling | τ | Δ vs nomic | n |
| --- | --- | --- | --- | --- |
| panel | `mean` | 0.031 | −0.057 | 53 |
| **supplement** | **`last_token`** | **0.131** | −0.028 | 53 |

τ improves ≈4× from the pooling fix alone. SFR-2B still fails the gate, but the failure is now structural (the model isn't competitive against nomic-text-v1 on this corpus), not methodological. The "additional finding" in `RESULTS_PANEL.md` was correct.

### 3. Granite-r2's "passes the gate" framing was an artifact

The earlier draft reported granite Δ=+0.098 (Ollama-nomic, n=35). At HF-nomic, n=53, granite scores τ=0.083 — *below* the baseline. The earlier positive Δ was the conjunction of:
- a weaker Ollama-served baseline (which 500s on long PRs and so under-scores on the corpus subset where it works);
- selection bias against precisely the pairs both granite and the Ollama-baseline drop.

This is a stronger version of the reviewer's caveat than originally acknowledged: granite-r2 is not a near-miss. It loses at fair measurement.

## Cross-doc τ caveat

`RESULTS_PANEL.md` reports τ_nomic_HF = 0.088 at n=53. This supplement reports τ_nomic_HF = 0.159 at n=53. Same model weights (`nomic-ai/nomic-embed-text-v1`, same revision in cache: `3ac47f12…`). Same fixture. Same pooling (`mean`).

The most likely cause is `transformers` version drift. The panel ran on `transformers==4.47.1`; Qwen3 needs ≥4.52 and granite-r2 (ModernBERT) needs ≥4.48, so the supplement venv pins `transformers>=4.55,<5` and resolved to `4.57.6`. `nomic-embed-text-v1` is loaded with `trust_remote_code=True` against a custom modeling file that interacts with transformers internals — changes to `AutoModel` / `PreTrainedModel` / tokenizer behavior between 4.47 and 4.57 can produce different vectors even from the same weights.

This means: **τ differences across documents are not directly comparable.** Within this supplement, all three candidates were scored against the same supplement-version HF-nomic baseline, so the deltas in the headline table are internally fair. Cross-comparing supplement candidates against the panel's arctic-l-v2 τ requires re-embedding arctic on transformers 4.57.6 — left as future work; the supplement is not in a position to dethrone arctic without that step.

## GPU acceleration — corrects RESULTS_PANEL plan deviation #4

`RESULTS_PANEL.md` notes: *"Intel Arc A770 GPU on vinbonesjr is not utilized. The system's Ollama is the stock CPU build."* That is no longer true for this supplement, and the original observation is itself stale:

- **Ollama** runs on the Arc A770 via the `ollama-sycl 0.20.4-4` package (Arch). The package drops `libggml-sycl.so` into `/usr/lib/ollama/` where stock ollama discovers it at runtime via `GGML_BACKEND_DL`. The Arch `ollama` is unmodified. Active runtime env (`/etc/conf.d/ollama-sycl`): `ZES_ENABLE_SYSMAN=1`, `SYCL_CACHE_PERSISTENT=0` (oneAPI 2025.3 has a SIGSEGV in `PersistentDeviceCodeCache::getItemFromDisc` — first-run JIT is slower but stable), `OLLAMA_CONTEXT_LENGTH=8192`. `ext_intel_free_memory` is not supported on Arc/DG2 so ollama can't see free VRAM; the explicit context cap prevents over-allocation.
- **PyTorch supplement embeddings** run on the Arc A770 via `torch==2.9.0+xpu` (Intel-prebuilt wheel from `https://download.pytorch.org/whl/xpu`). `embed_hf.py` now prefers `xpu` over `cuda`. No `intel-extension-for-pytorch` needed for torch ≥ 2.7.
- **Wheel/system library conflict workaround.** The +xpu torch wheel bundles `libsycl.so.8` and `libur_loader.so.0.12.0` under `.venv/lib/`. The system's `/opt/intel/oneapi/2025.3/lib/libur_loader.so.0` (from `intel-oneapi-base-toolkit 2025.3.2.21`) is missing `urEnqueueCooperativeKernelLaunchExp@LIBUR_LOADER_0.12`, which the wheel's libsycl requires. Without intervention, `import torch` crashes with `ImportError: …libsycl.so.8: undefined symbol: urEnqueueCooperativeKernelLaunchExp`. `run_panel_supplement.sh` prepends `$PWD/.venv/lib` to `LD_LIBRARY_PATH` so the wheel's bundled UR loader wins for the process — scope-limited to this venv.

## CPU fallback in `embed_hf.py`

Added in this revision. When a batch fails on the primary device (XPU/CUDA) with a resource-exhaustion signal — `torch.OutOfMemoryError`, an "out of memory" message, or an Intel UR-loader error (`UR_RESULT_ERROR_OUT_OF_RESOURCES`, `UR backend failed`) — the runner lazily loads a CPU-resident copy of the model and retries the batch there. The retry path is opt-out via `--no-cpu-fallback`.

Empirical impact on this supplement's runs (all on Arc A770 first, with CPU fallback closing gaps):

| model | XPU OOM batches | CPU-recovered ids | Final coverage |
| --- | --- | --- | --- |
| `nomic-embed-text-v1` (baseline, HF-served) | ~10 (long prompts) | **40** | 200/200 |
| `Qwen/Qwen3-Embedding-0.6B` (last_token) | 3 (after batch=1 retry) | 3 | 200/200 |
| `granite-embedding-311m-multilingual-r2` (mean) | 4 (after batch=1 retry) | 4 | 200/200 |
| `SFR-Embedding-Code-2B_R` (last_token) | several at b=1 first pass; recovered on resume | 3 | 200/200 |

The `nomic-embed-text-v1` HF-baseline result is the headline beneficiary: it would not have reached n=53 without the fallback, and **the whole HF-baseline analysis depends on having a 200/200 baseline.** Without CPU fallback, the Ollama-baseline n=35 ceiling would have stood and the bias-corrected picture would not be visible.

## OOM observations

| model | batch=4 dropped | batch=1 dropped (CPU-recovered) | smallest XPU-failing alloc |
| --- | --- | --- | --- |
| Qwen3-Embedding-0.6B | 36 batches (164/200 kept) | 3 (all 3 recovered on CPU; 200/200) | 17–18 GiB single-seq at 32K context |
| granite-embedding-311m-r2 | 40 batches (160/200 kept) | 4 (all 4 recovered on CPU; 200/200) | 25–27 GiB single-seq at 8K context |
| SFR-Embedding-Code-2B_R | n/a (started at batch=1) | 80 in first pass (0 recovered before patch); 3 on resume after broadening OOM-detection to UR errors | UR-loader exhaustion under concurrent Arc load |
| nomic-embed-text-v1 (HF baseline) | n/a (batch=4) | n/a | dozens of long-prompt batches, all CPU-recovered |

SFR-2B in particular surfaced that the XPU's UR-loader can fail with `UR_RESULT_ERROR_OUT_OF_RESOURCES` rather than a pytorch OOM — distinct error class, but same operational meaning. The CPU-fallback exception filter was widened to catch both signatures.

## SFR-2B custom modeling workaround

`Salesforce/SFR-Embedding-Code-2B_R` ships a custom `modeling_gemma2.py` (loaded via `trust_remote_code=True`) whose `__init__` hardcodes:

```python
self.model = Gemma2Model.from_pretrained(config._name_or_path, trust_remote_code=True, is_causal=False, device_map="auto")
```

The `device_map="auto"` triggers `accelerate.dispatch_model` which calls `model.to(device)` on still-meta tensors → `NotImplementedError: Cannot copy out of meta tensor`. `embed_hf.py` couldn't intervene because the error fires before our outer load returns. **Workaround applied:** the downloaded custom modeling file (under `~/.cache/huggingface/modules/transformers_modules/Salesforce/...`) was edited to drop the `device_map="auto"` kwarg from the inner `Gemma2Model.from_pretrained` call. With that, SFR-2B loads cleanly on Arc XPU.

**This workaround is not durable across cache refresh.** Anyone reproducing the SFR-2B row will need to re-apply the same `sed`:

```sh
for f in ~/.cache/huggingface/modules/transformers_modules/Salesforce/SFR*/*/modeling_gemma2.py; do
  sed -i 's/, device_map="auto"//g' "$f"
done
```

A more robust fix would live upstream in the model's modeling code; until then, the patch is a known limitation.

## Why "supplement" and not "re-panel"

1. **The original panel vectors are not re-embedded.** This document does not contradict any τ value in `RESULTS_PANEL.md`. The panel scored its candidates against panel-version (`transformers==4.47.1`) nomic-HF. This supplement scores against supplement-version (`transformers==4.57.6`) nomic-HF. The two baselines differ; the supplement's verdicts about its own candidates are internally consistent but cross-document Δ vs arctic-l-v2 is not strictly apples-to-apples (see "Cross-doc τ caveat").
2. **The fixture is identical.** Same `features.json` (200 PRs from `IBM/mcp-context-forge`), same `judgments.json` (53 pairs).

## Methodology summary

- **Dataset:** identical to the panel — 200 PRs from `IBM/mcp-context-forge`, 53 judgment pairs. No changes to `features.json`, `judgments.json`, or `pr_states.json`.
- **Prompts:** unchanged — full per-modality content (no 5 KB cap from Phase A). Each tokenizer truncates at its native `model_max_length` capped at 32 K.
- **Pooling:** Qwen3 → `last_token`; granite → `mean` (ModernBERT encoder); SFR-2B → `last_token` (decoder, pooling-correction re-test); HF-nomic baseline → `mean`.
- **Coverage:** all four models above complete the 200/200 corpus thanks to the CPU fallback in `embed_hf.py`. Ollama-served nomic baseline still caps at 166/200 (34 PRs return 500 from the model's compiled context limit) and so is reported only at n=35.
- **Analysis:** SciPy's `kendalltau(variant='c')`, same as the panel.

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
./run_panel_supplement.sh                       # embeds Qwen3 + granite + SFR-2B on Arc A770

# Build the HF-served baseline (panel-equivalent path) for fair n=53 scoring:
LD_LIBRARY_PATH="$PWD/.venv/lib:$LD_LIBRARY_PATH" .venv/bin/python embed_hf.py \
  --model nomic-ai/nomic-embed-text-v1 \
  --features features.json --out nomic_hf_vectors.json \
  --cache-dir hf_cache --batch-size 4 --pooling mean

# Then score every candidate against both baselines:
for cand in qwen3_06b granite_311m_r2 sfr_code_2b_lasttoken ; do
  for base in nomic nomic_hf ; do
    .venv/bin/python analyze.py \
      --nomic ${base}_vectors.json \
      --codeexecutor ${cand}_vectors.json \
      --out ${cand}_vs_${base}.md
  done
done
```

The supplement script requires `.venv/` to exist with `torch==2.9.0+xpu` and `transformers>=4.55` from `requirements.txt`. The first call to `run.sh` from the panel will bootstrap the venv via `uv`. SFR-2B additionally needs the modeling-file `sed` workaround above.

## When to revisit

- **Phase B sidecar regresses on throughput or VRAM.** Qwen3-0.6B is the closest supplement candidate to a serious alternative — barely passes the gate at fair measurement, but is the same parameter budget as the panel winner with a different architecture/lab. Worth a fresh head-to-head if the panel winner becomes operationally unviable.
- **A future re-panel adds GPU-served embedders.** This supplement establishes that the Arc A770 stack (`ollama-sycl` + `torch==2.9.0+xpu` + CPU fallback) is sufficient for behavioral-embeddings workloads at this corpus size. Future runs can target it directly.
- **The transformers-version-drift hypothesis is testable.** If a future investigator pins `transformers==4.47.1` (panel version) for the nomic-baseline embed only, the supplement's τ_nomic_HF should converge toward the panel's 0.088, enabling strict cross-doc Δ comparisons against arctic-l-v2.

---

*Last updated: 2026-05-14.*
