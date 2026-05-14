#!/usr/bin/env bash
# Post-panel supplement: evaluate embedders released after the original
# 2026-05-13 panel (RESULTS_PANEL.md), and re-test the panel's most
# notable negative datapoint with corrected pooling. All on the same
# 53-pair judgment set.
#
# Inference path: OpenVINO Model Server (ovms) on the Intel Arc A770. The
# models are pulled and registered once by setup_ovms_models.sh; this
# script just hits OVMS /v3/embeddings (OpenAI-compat) for each.
#
# Models (all Apache-2.0, post-date the panel):
#  * Qwen/Qwen3-Embedding-0.6B          — 596M decoder, LAST-token pooling.
#  * ibm-granite/granite-embedding-311m-multilingual-r2 — 312M ModernBERT,
#    native ONNX + OpenVINO weights, CLS pooling per model card.
#
# nomic_vectors.json is the panel baseline. If absent it's regenerated here:
# preferred path is OVMS (nomic-ai/nomic-embed-text-v1.5 with patched MEAN
# pooling); fall back to Ollama (http://localhost:11434) if OVMS doesn't
# serve nomic. Either reproduces the baseline closely enough that the
# Kendall tau delta stays interpretable.
#
# Run from this directory. Each model writes its own *_vectors.json
# (gitignored). Resume support in embed_ovms.py makes re-runs cheap.
set -uo pipefail
cd "$(dirname "$0")"

OVMS_ENDPOINT="${OVMS_ENDPOINT:-http://localhost:8978}"

if [ ! -d .venv ]; then
  uv venv .venv
  uv pip install --python .venv/bin/python -r requirements.txt
fi

# Sanity-check the OVMS endpoint before launching three slow GPU compiles.
if ! curl -fsS --max-time 5 "$OVMS_ENDPOINT/v2/health/ready" >/dev/null 2>&1; then
  echo "OVMS not ready at $OVMS_ENDPOINT. Start it (systemctl start ovms),"
  echo "then register models with:  ./setup_ovms_models.sh"
  exit 2
fi

ovms_has_model() {
  # Probes the v2 model-ready endpoint. The OVMS routing layer rejects raw
  # `/` in the model name segment (returns 400 "Invalid request URL"), so
  # encode any slashes to %2F before issuing the request — model names
  # like `Qwen/Qwen3-Embedding-0.6B` need this. HTTP 200 means loaded; any
  # other status (404 missing, 503 mid-compile, etc.) is treated as "not".
  local encoded="${1//\//%2F}"
  curl -fsS --max-time 5 -o /dev/null -w '%{http_code}' \
    "$OVMS_ENDPOINT/v2/models/$encoded/ready" 2>/dev/null | grep -q '^200$'
}

# Lazy bootstrap: if neither Qwen3 nor granite is loaded yet, run the pull
# helper. Idempotent — already-pulled models are skipped. Set
# OVMS_SKIP_PULL=1 to opt out (e.g. you maintain the repo by hand).
if [ "${OVMS_SKIP_PULL:-0}" != "1" ]; then
  if ! ovms_has_model "Qwen/Qwen3-Embedding-0.6B" \
     || ! ovms_has_model "ibm-granite/granite-embedding-311m-multilingual-r2"; then
    echo "== bootstrapping OVMS model repository =="
    ./setup_ovms_models.sh || {
      echo "setup_ovms_models.sh failed — fix and re-run, or set OVMS_SKIP_PULL=1" >&2
      exit 2
    }
    # Give OVMS a generous window to discover + compile each model on GPU.
    # First-load GPU JIT on Arc A770 is 60-180 s per embedder.
    echo "waiting for models to become ready..."
    deadline=$(( $(date +%s) + 600 ))
    ready=0
    while [ "$(date +%s)" -lt "$deadline" ]; do
      if ovms_has_model "Qwen/Qwen3-Embedding-0.6B" \
         && ovms_has_model "ibm-granite/granite-embedding-311m-multilingual-r2"; then
        ready=1
        break
      fi
      sleep 5
    done
    if [ "$ready" -ne 1 ]; then
      echo "ERROR: models failed to become ready in OVMS within 10 minutes." >&2
      exit 3
    fi
  fi
fi

# nomic baseline (re)generation if needed.
if [ ! -f nomic_vectors.json ]; then
  echo "== nomic_vectors.json missing — regenerating baseline =="
  if ovms_has_model "nomic-ai/nomic-embed-text-v1.5"; then
    .venv/bin/python embed_ovms.py \
      --model nomic-ai/nomic-embed-text-v1.5 \
      --features features.json \
      --out nomic_vectors.json \
      --endpoint "$OVMS_ENDPOINT" 2>nomic_run.log \
      && echo "  -> $(jq 'length' nomic_vectors.json) vectors (via ovms)" \
      || echo "  -> FAILED (see nomic_run.log)"
  elif curl -fsS --max-time 3 http://localhost:11434/api/tags >/dev/null 2>&1; then
    echo "  (OVMS has no nomic model — falling back to Ollama)"
    .venv/bin/python embed_nomic.py \
      --features features.json \
      --out nomic_vectors.json 2>nomic_run.log \
      && echo "  -> $(jq 'length' nomic_vectors.json) vectors (via ollama)" \
      || echo "  -> FAILED (see nomic_run.log)"
  else
    echo "  -> no nomic backend available (neither OVMS nor Ollama)"
    exit 3
  fi
fi

# (model_name, output_file, log_file)
models=(
  "Qwen/Qwen3-Embedding-0.6B:qwen3_06b_vectors.json:qwen3_06b_run.log"
  "ibm-granite/granite-embedding-311m-multilingual-r2:granite_311m_r2_vectors.json:granite_311m_r2_run.log"
)

for entry in "${models[@]}"; do
  IFS=":" read -r model out log <<< "$entry"
  echo "== $model =="
  if ! ovms_has_model "$model"; then
    echo "  -> not loaded in OVMS — run ./setup_ovms_models.sh first"
    continue
  fi
  if .venv/bin/python embed_ovms.py \
      --model "$model" \
      --features features.json \
      --out "$out" \
      --endpoint "$OVMS_ENDPOINT" \
      --batch-size 4 2>"$log" ; then
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
