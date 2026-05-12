#!/usr/bin/env bash
# Sequential panel runner for Task 8 of the behavioral-embeddings panel plan.
# Each model runs with --batch-size 4 to fit comfortably in 15 GiB RAM with
# CPU inference on long-context inputs (the default 32 OOM'd on nomic at
# full prompt sizes).
#
# Run from this directory. The four output files are gitignored. Resume
# support in embed_hf.py means re-running is cheap on partial state.
set -euo pipefail
cd "$(dirname "$0")"

models=(
  "nomic-ai/nomic-embed-code:nomic_embed_code_vectors.json:nomic_embed_code_run.log"
  "jinaai/jina-embeddings-v2-base-code:jina_v2_code_vectors.json:jina_v2_code_run.log"
  "Salesforce/SFR-Embedding-Code-400M_R:sfr_code_400m_vectors.json:sfr_code_400m_run.log"
  "Snowflake/snowflake-arctic-embed-l-v2.0:arctic_l_v2_vectors.json:arctic_l_v2_run.log"
)

for entry in "${models[@]}"; do
  IFS=":" read -r model out log <<< "$entry"
  echo "=== ${model} ==="
  .venv/bin/python embed_hf.py \
    --model "$model" \
    --features features.json \
    --out "$out" \
    --cache-dir hf_cache \
    --batch-size 4 2>"$log"
  echo "  -> $(jq 'length' "$out") vectors in $out"
done

echo "panel complete"
