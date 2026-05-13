#!/usr/bin/env bash
# Post-panel supplement: evaluate two embedders released after the original
# 2026-05-13 panel (RESULTS_PANEL.md) on the same 53-pair judgment set.
#
# Both models are Apache-2.0 and post-date the panel:
#
#  * Qwen/Qwen3-Embedding-0.6B          — 596M, decoder-style (uses
#    last-token pooling, NOT mean — see embed_hf.py POOL_FNS).
#    Same param budget as the panel winner (arctic-l-v2, 568M) but a
#    different architecture and lab.
#
#  * ibm-granite/granite-embedding-311m-multilingual-r2 — 312M ModernBERT
#    encoder, native ONNX + OpenVINO weights, Matryoshka-trained (full
#    1024-dim used here for parity; downstream consumers can truncate).
#    Smaller than arctic-l-v2; could enable dropping the Python sidecar
#    entirely if it wins.
#
# This script does NOT re-run the original panel (those vectors are
# already produced and the verdict in RESULTS_PANEL.md is final). It
# only embeds the two new models so they can be scored against the
# existing nomic baseline via analyze.py.
#
# Run from this directory. Each model writes its own *_vectors.json
# (gitignored). Resume support in embed_hf.py makes re-runs cheap on
# partial state.
set -uo pipefail
cd "$(dirname "$0")"

# Tuples: model:out:log:pooling
models=(
  "Qwen/Qwen3-Embedding-0.6B:qwen3_06b_vectors.json:qwen3_06b_run.log:last_token"
  "ibm-granite/granite-embedding-311m-multilingual-r2:granite_311m_r2_vectors.json:granite_311m_r2_run.log:mean"
)

for entry in "${models[@]}"; do
  IFS=":" read -r model out log pooling <<< "$entry"
  echo "=== ${model}  (pooling=${pooling}) ==="
  if .venv/bin/python embed_hf.py \
      --model "$model" \
      --features features.json \
      --out "$out" \
      --cache-dir hf_cache \
      --batch-size 4 \
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
