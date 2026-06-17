#!/usr/bin/env bash
# Full pipeline runner for vinbonesjr: nomic baseline + 4-model panel + jazzcort.
# Designed for 58 GB RAM box; uses batch-size=16 (vs 4 on the 14 GB local box).
# Each step logs to its own *_run.log; orchestrator progress to stdout.
set -uo pipefail
cd "$(dirname "$0")"

echo "=== START $(date) ==="

# Step 0: kick off jazzcort pull in background; will be awaited later.
echo "[bg] pulling jazzcort/nomic-embed-code-Q6_K (5.8 GB)..."
( ollama pull jazzcort/nomic-embed-code-Q6_K ) > jazzcort_pull.log 2>&1 &
JAZZCORT_PID=$!

# Step 1: nomic-embed-text-v1 baseline (HF, not Ollama — Ollama's nomic
# doesn't honor num_ctx for embeddings and 500s on long prompts).
echo "=== [1/6] nomic-embed-text-v1 (baseline) === $(date)"
if .venv/bin/python embed_hf.py \
    --model nomic-ai/nomic-embed-text-v1 \
    --features features.json \
    --out nomic_vectors.json \
    --cache-dir hf_cache \
    --batch-size 16 2>nomic_run.log ; then
  echo "  -> $(jq length nomic_vectors.json) vectors"
else
  echo "  -> FAILED (see nomic_run.log)"
fi

# Step 2-5: HF panel (4 models)
declare -a panel=(
  "nomic-ai/CodeRankEmbed:nomic_embed_code_vectors.json:nomic_embed_code_run.log"
  "jinaai/jina-embeddings-v2-base-code:jina_v2_code_vectors.json:jina_v2_code_run.log"
  "Salesforce/SFR-Embedding-Code-400M_R:sfr_code_400m_vectors.json:sfr_code_400m_run.log"
  "Snowflake/snowflake-arctic-embed-l-v2.0:arctic_l_v2_vectors.json:arctic_l_v2_run.log"
)
step=2
for entry in "${panel[@]}"; do
  IFS=":" read -r M O L <<< "$entry"
  echo "=== [$step/6] $M === $(date)"
  if .venv/bin/python embed_hf.py \
      --model "$M" \
      --features features.json \
      --out "$O" \
      --cache-dir hf_cache \
      --batch-size 16 2>"$L" ; then
    echo "  -> $(jq length "$O") vectors"
  else
    echo "  -> FAILED (see $L)"
  fi
  step=$((step + 1))
done

# Step 6: wait for jazzcort pull, then embed via Ollama (uses embed_nomic.py
# with a different --model flag).
echo "=== [6/6] waiting for jazzcort pull ==="
wait $JAZZCORT_PID
echo "  pull done; status:"
tail -1 jazzcort_pull.log
echo ""
echo "=== jazzcort/nomic-embed-code-Q6_K embed === $(date)"
if .venv/bin/python embed_nomic.py \
    --model jazzcort/nomic-embed-code-Q6_K \
    --features features.json \
    --out jazzcort_vectors.json 2>jazzcort_run.log ; then
  echo "  -> $(jq length jazzcort_vectors.json) vectors"
else
  echo "  -> FAILED (see jazzcort_run.log)"
fi

echo ""
echo "=== summary ==="
for f in nomic_vectors.json nomic_embed_code_vectors.json jina_v2_code_vectors.json \
         sfr_code_400m_vectors.json arctic_l_v2_vectors.json jazzcort_vectors.json ; do
  if [ -f "$f" ]; then
    echo "  $f: $(jq length "$f") vectors"
  else
    echo "  $f: missing"
  fi
done

echo "=== DONE $(date) ==="
