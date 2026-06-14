#!/usr/bin/env bash
# Fair n=53 dual-baseline re-run of the post-panel supplement. Where
# run_panel_supplement.sh scores the OVMS candidates against the single
# Ollama/panel nomic baseline (nomic_vectors.json, ~n=35 after skips), this
# variant adds the HF-served nomic baseline (nomic_hf_vectors.json, built
# with embed_hf.py, full n=53) and scores both candidates against *both*
# baselines — the bias-corrected methodology from ab0c8ed. Goal: confirm
# the supplement's verdict (Qwen3 thin pass, granite FAIL) holds on the
# fair n=53 fixture, not just the smaller Ollama-baseline intersection.
#
# Like run_panel_supplement.sh, the candidate embeddings route through
# OpenVINO Model Server's /v3/embeddings on the Arc A770; the only added
# moving part here is the HF-served baseline used for the n=53 scoring row.
#
# Methodology is the bias-corrected one from ab0c8ed:
#   * full n=53 against the *HF-served* nomic baseline
#     (nomic_hf_vectors.json, embed_hf.py with CPU fallback)
#   * Ollama-nomic baseline kept for production-parity framing only
#   * both candidates scored against both baselines
#
# What we DON'T do here: re-test SFR-Embedding-Code-2B_R via OVMS. SFR-2B
# needs ~8 GiB fp32 weights and a custom Gemma2 modeling.py with the
# device_map="auto" sed-patch — out of scope for the OVMS plumbing demo;
# embed_hf.py handles that case adequately in run_panel_supplement.sh.
#
# Run from this directory. Output JSON files are gitignored. embed_ovms.py
# has resume support so re-runs only ship the missing pairs over HTTP.
set -uo pipefail
cd "$(dirname "$0")"

OVMS_ENDPOINT="${OVMS_ENDPOINT:-http://localhost:8978}"
# Strip any trailing slash so path joins below don't produce "//", which some
# strict reverse proxies in front of OVMS reject.
OVMS_ENDPOINT="${OVMS_ENDPOINT%/}"

# --- 1. Slim runtime venv (no torch). requirements.txt is already the slim
# OVMS-client manifest (numpy/scipy/requests/pytest); install straight from it
# so this script and the manifest can't drift. The torch+XPU stack for the
# embed_hf.py baseline lives in requirements-hf.txt and is built separately
# into .venv-hf (see the nomic_hf_vectors.json note below).
if [ ! -d .venv-ovms ]; then
  echo "== creating .venv-ovms (slim, no torch) =="
  uv venv .venv-ovms
  uv pip install --python .venv-ovms/bin/python -r requirements.txt
fi

# --- 2. OVMS health probe + lazy bootstrap.
if ! curl -fsS --max-time 5 "$OVMS_ENDPOINT/v2/health/ready" >/dev/null 2>&1; then
  echo "OVMS not ready at $OVMS_ENDPOINT. systemctl start ovms,"
  echo "then re-run.  /usr/lib/ovms/contrib/setup_embeddings_arc.sh (if pkg)"
  echo "or ./setup_ovms_models.sh handles the model pull + register step."
  exit 2
fi

ovms_has_model() {
  # URL-encode the slash in HF-style model names — OVMS routing returns 400
  # "Invalid request URL" on bare /.
  local encoded="${1//\//%2F}"
  curl -fsS --max-time 5 -o /dev/null -w '%{http_code}' \
    "$OVMS_ENDPOINT/v2/models/$encoded/ready" 2>/dev/null | grep -q '^200$'
}

# Bootstrap models if any are missing. Skip with OVMS_SKIP_PULL=1.
if [ "${OVMS_SKIP_PULL:-0}" != "1" ]; then
  if ! ovms_has_model "Qwen/Qwen3-Embedding-0.6B" \
     || ! ovms_has_model "ibm-granite/granite-embedding-311m-multilingual-r2" \
     || ! ovms_has_model "nomic-ai/nomic-embed-text-v1.5"; then
    echo "== bootstrapping OVMS embedding models =="
    ./setup_ovms_models.sh || {
      echo "setup_ovms_models.sh failed — fix and re-run, or set OVMS_SKIP_PULL=1"
      exit 2
    }
    echo "waiting for model readiness (first GPU compile is 60-180 s per model)..."
    deadline=$(( $(date +%s) + 600 ))
    ready=0
    while [ "$(date +%s)" -lt "$deadline" ]; do
      # Wait on all three models the bootstrap gate above requires, including
      # nomic-v1.5 — it's loaded for the downstream within-OVMS sanity check,
      # so returning before it finishes compiling would race that consumer.
      if ovms_has_model "Qwen/Qwen3-Embedding-0.6B" \
         && ovms_has_model "ibm-granite/granite-embedding-311m-multilingual-r2" \
         && ovms_has_model "nomic-ai/nomic-embed-text-v1.5"; then
        ready=1
        break
      fi
      sleep 5
    done
    if [ "$ready" -ne 1 ]; then
      echo "ERROR: timeout waiting for OVMS models to become ready" >&2
      exit 1
    fi
  fi
fi

# --- 3. Embed Qwen3 + granite via OVMS. nomic-v1.5 is loaded too but
# only consumed downstream by the within-OVMS sanity check; the
# load-bearing baseline (nomic_hf_vectors.json, nomic-embed-text-v1)
# comes from run_panel_supplement.sh's embed_hf.py path.
models=(
  "Qwen/Qwen3-Embedding-0.6B:qwen3_06b_vectors.json:qwen3_06b_run.log"
  "ibm-granite/granite-embedding-311m-multilingual-r2:granite_311m_r2_vectors.json:granite_311m_r2_run.log"
)

for entry in "${models[@]}"; do
  IFS=":" read -r model out log <<< "$entry"
  echo "== $model =="
  if ! ovms_has_model "$model"; then
    echo "  -> not loaded in OVMS — bootstrap with ./setup_ovms_models.sh"
    continue
  fi
  if .venv-ovms/bin/python embed_ovms.py \
      --model "$model" \
      --features features.json \
      --out "$out" \
      --endpoint "$OVMS_ENDPOINT" \
      --batch-size 4 2>"$log" ; then
    echo "  -> $(.venv-ovms/bin/python -c 'import json,sys; print(len(json.load(open(sys.argv[1]))))' "$out") vectors in $out"
  else
    echo "  -> FAILED (see $log); continuing"
  fi
done

# --- 4. Score against both baselines, mirroring ab0c8ed's analysis loop.
# nomic_vectors.json (Ollama, n=35) is the production-parity row.
# nomic_hf_vectors.json (HF-served, n=53) is the load-bearing one — produce
# it first via run_panel_supplement.sh's manual snippet if missing.
if [ ! -f nomic_hf_vectors.json ]; then
  cat <<EOF >&2
warning: nomic_hf_vectors.json missing. Build it for the fair n=53 score with:

  uv venv .venv-hf && uv pip install --python .venv-hf/bin/python -r requirements-hf.txt
  LD_LIBRARY_PATH="\$PWD/.venv-hf/lib:\$LD_LIBRARY_PATH" .venv-hf/bin/python embed_hf.py \\
    --model nomic-ai/nomic-embed-text-v1 \\
    --features features.json --out nomic_hf_vectors.json \\
    --cache-dir hf_cache --batch-size 4 --pooling mean

(embed_hf.py needs the torch+XPU stack from requirements-hf.txt, built into a
separate .venv-hf kept alongside the slim .venv-ovms used for the OVMS path.)
EOF
fi

echo
echo "supplement embeddings complete. Score each OVMS candidate with:"
echo "  for base in nomic nomic_hf ; do"
echo "    for cand in qwen3_06b granite_311m_r2 ; do"
echo "      .venv-ovms/bin/python analyze.py \\"
echo "        --nomic \${base}_vectors.json \\"
echo "        --codeexecutor \${cand}_vectors.json \\"
echo "        --out \${cand}_vs_\${base}_ovms.md"
echo "    done"
echo "  done"
