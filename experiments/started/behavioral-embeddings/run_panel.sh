#!/usr/bin/env bash
# Sequential panel runner for Task 8 of the behavioral-embeddings panel plan.
# Each model runs with --batch-size 4 to fit comfortably in 15 GiB RAM with
# CPU inference on long-context inputs (the default 32 OOM'd on nomic at
# full prompt sizes).
#
# Note: `nomic-ai/nomic-embed-code` turned out to be a 7B decoder model
# (6 shards, ~6 GB on disk) and OOM'd on this 15 GiB box. Substituted
# `nomic-ai/CodeRankEmbed` (137M encoder, 8K context, same lab) which
# fits the original hypothesis ("a recent code-fine-tuned model from a
# reputable lab beats nomic-embed-text") at a tractable size.
#
# `set -e` is deliberately NOT used: a single-model OOM should not abort
# the whole panel — we want as many data points as we can get.
#
# Run from this directory. Output JSON files are gitignored. Resume
# support in embed_hf.py means re-running is cheap on partial state.
set -uo pipefail
cd "$(dirname "$0")"

models=(
  "nomic-ai/CodeRankEmbed:nomic_embed_code_vectors.json:nomic_embed_code_run.log"
  "jinaai/jina-embeddings-v2-base-code:jina_v2_code_vectors.json:jina_v2_code_run.log"
  "Salesforce/SFR-Embedding-Code-400M_R:sfr_code_400m_vectors.json:sfr_code_400m_run.log"
  "Snowflake/snowflake-arctic-embed-l-v2.0:arctic_l_v2_vectors.json:arctic_l_v2_run.log"
)

for entry in "${models[@]}"; do
  IFS=":" read -r model out log <<< "$entry"
  echo "=== ${model} ==="
  if .venv/bin/python embed_hf.py \
      --model "$model" \
      --features features.json \
      --out "$out" \
      --cache-dir hf_cache \
      --batch-size 4 2>"$log" ; then
    echo "  -> $(jq 'length' "$out") vectors in $out"
  else
    echo "  -> FAILED (see $log); continuing to next model"
  fi
done

echo "panel complete"
