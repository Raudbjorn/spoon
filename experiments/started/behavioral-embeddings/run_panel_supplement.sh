#!/usr/bin/env bash
# Post-panel supplement: evaluate embedders released after the original
# 2026-05-13 panel (RESULTS_PANEL.md), and re-test the panel's most
# notable negative datapoint with corrected pooling. All on the same
# 53-pair judgment set.
#
# Models:
#
#  * Qwen/Qwen3-Embedding-0.6B          — 596M, decoder-style (uses
#    last-token pooling, NOT mean — see embed_hf.py POOL_FNS).
#    Same param budget as the panel winner (arctic-l-v2, 568M) but a
#    different architecture and lab. Apache-2.0, post-dates the panel.
#
#  * ibm-granite/granite-embedding-311m-multilingual-r2 — 312M ModernBERT
#    encoder, native ONNX + OpenVINO weights, Matryoshka-trained (full
#    1024-dim used here for parity; downstream consumers can truncate).
#    Smaller than arctic-l-v2; could enable dropping the Python sidecar
#    entirely if it wins. Apache-2.0, post-dates the panel.
#
#  * Salesforce/SFR-Embedding-Code-2B_R — 2B Gemma2 decoder. Originally
#    panel-tested at --pooling mean and lost by Δ=−0.057 (worse than its
#    400M sibling, which was a clue the pooling was wrong). Re-tested
#    here with --pooling last_token, the architecturally correct choice
#    for a decoder-as-embedder (same gotcha SFR-2B revealed first).
#    SFR-Embedding-Code-2B is the panel artifact this row contradicts
#    or confirms — see RESULTS_PANEL.md "Additional finding".
#
# This script does NOT re-run the original panel (those vectors are
# already produced and the verdict in RESULTS_PANEL.md is final). It
# only embeds the supplement models so they can be scored against the
# existing nomic baseline via analyze.py.
#
# Run from this directory. Each model writes its own *_vectors.json
# (gitignored). Resume support in embed_hf.py makes re-runs cheap on
# partial state. The CPU fallback in embed_hf.py (since 2026-05-14)
# closes the n_pairs gap for prompts that OOM the Arc A770's 16 GiB.
set -uo pipefail
cd "$(dirname "$0")"

# The torch==2.9.0+xpu wheel bundles libsycl.so.8 + libur_loader.so.0.12.0
# under .venv/lib/ — but ldconfig's cache points libur_loader.so.0 at the
# system /opt/intel/oneapi/2025.3/lib/libur_loader.so.0, which is missing
# urEnqueueCooperativeKernelLaunchExp. Without this prefix, `import torch`
# crashes with: undefined symbol: urEnqueueCooperativeKernelLaunchExp,
# version LIBUR_LOADER_0.12. Prepend the wheel libs so its bundled UR
# loader wins for THIS process only (still keep the system libs visible
# so anything else in the venv that wants them resolves correctly).
export LD_LIBRARY_PATH="$PWD/.venv/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

# Tuples: model:out:log:pooling:batch_size
# SFR-2B uses batch_size=1 — at 2B params the model itself is ~8 GiB
# in fp32 and the attention working set quickly exceeds the Arc's 16 GiB.
# CPU fallback in embed_hf.py will catch what XPU can't.
models=(
  "Qwen/Qwen3-Embedding-0.6B:qwen3_06b_vectors.json:qwen3_06b_run.log:last_token:4"
  "ibm-granite/granite-embedding-311m-multilingual-r2:granite_311m_r2_vectors.json:granite_311m_r2_run.log:mean:4"
  "Salesforce/SFR-Embedding-Code-2B_R:sfr_code_2b_lasttoken_vectors.json:sfr_code_2b_lasttoken_run.log:last_token:1"
)

for entry in "${models[@]}"; do
  IFS=":" read -r model out log pooling bs <<< "$entry"
  echo "=== ${model}  (pooling=${pooling}, batch_size=${bs}) ==="
  if .venv/bin/python embed_hf.py \
      --model "$model" \
      --features features.json \
      --out "$out" \
      --cache-dir hf_cache \
      --batch-size "$bs" \
      --pooling "$pooling" 2>"$log" ; then
    echo "  -> $(jq 'length' "$out") vectors in $out"
  else
    echo "  -> FAILED (see $log); continuing"
  fi
done

echo
echo "supplement embeddings complete. Score each candidate with:"
echo "  .venv/bin/python analyze.py \\"
echo "    --nomic nomic_vectors.json \\"
echo "    --codeexecutor qwen3_06b_vectors.json \\"
echo "    --out qwen3_06b_results.md"
echo "  .venv/bin/python analyze.py \\"
echo "    --nomic nomic_vectors.json \\"
echo "    --codeexecutor granite_311m_r2_vectors.json \\"
echo "    --out granite_311m_r2_results.md"
echo "  .venv/bin/python analyze.py \\"
echo "    --nomic nomic_vectors.json \\"
echo "    --codeexecutor sfr_code_2b_lasttoken_vectors.json \\"
echo "    --out sfr_code_2b_lasttoken_results.md"
