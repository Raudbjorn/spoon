#!/usr/bin/env bash
# Pull the three behavioral-embeddings models into the OVMS repository,
# converting weights to OpenVINO IR on the fly (optimum-cli is invoked by
# the C++ pull tool — no Python venv on the OVMS side), then register each
# model in /etc/ovms/config.json and reload the server.
#
# Target device: GPU. On this host that's the Intel Arc A770 — verify with
# `clinfo -l | grep -i arc` if the load logs say "Compiling Models" but
# never finish (means GPU plugin fell back to CPU).
#
# If the ovms package is installed locally
# (/usr/lib/ovms/contrib/setup_embeddings_arc.sh present), this script
# just delegates to it — the package helper takes the same defaults and
# stays in sync with whatever OVMS version is installed. Standalone path
# (Ollama-only hosts, Docker, etc.) follows below.
if [ -x /usr/lib/ovms/contrib/setup_embeddings_arc.sh ]; then
  exec /usr/lib/ovms/contrib/setup_embeddings_arc.sh "$@"
fi
#
# Pooling notes:
#   * nomic-embed-text-v1.5 is sentence-transformers-style (mean pooling).
#     OVMS 2026.0 only accepts CLS or LAST at pull time — we pull with CLS
#     and patch graph.pbtxt to MEAN, which the EmbeddingsCalculatorOV runtime
#     does accept. If the patched server logs "Pooling type is not supported",
#     fall back to CLS (different vectors than the Ollama baseline, but the
#     downstream tau comparison still runs).
#   * Qwen3-Embedding-0.6B is a decoder — LAST token pooling, the only
#     correct choice on this architecture (see RESULTS_PANEL.md SFR-2B
#     gotcha).
#   * granite-embedding-311m-multilingual-r2 is a ModernBERT encoder whose
#     model card specifies CLS pooling.
#
# Idempotent: re-runs skip already-downloaded models unless --force is
# passed. Requires root or membership in the ovms group (writes
# /var/lib/ovms and /etc/ovms/config.json).
set -uo pipefail

REPO="${OVMS_MODEL_REPOSITORY_PATH:-/var/lib/ovms}"
CONFIG="${OVMS_CONFIG_PATH:-/etc/ovms/config.json}"
TARGET_DEVICE="${OVMS_TARGET_DEVICE:-GPU}"
# Empty by default — Intel's ovms_python_off package has no optimum-intel, so
# any non-empty --weight-format makes `ovms --pull` fail instantly with
# "missing optimum-intel". Without the flag, --pull does a git-LFS fetch and
# leaves quantization to whatever the upstream HF repo already ships.
WEIGHT_FORMAT="${OVMS_WEIGHT_FORMAT:-}"
FORCE=0
for arg in "$@"; do
  case "$arg" in
    --force) FORCE=1 ;;
    --cpu)   TARGET_DEVICE=CPU ;;
    -h|--help)
      sed -n '2,30p' "$0"
      exit 0 ;;
  esac
done

# model:pooling   (pooling values are the OVMS --pull CLI vocabulary)
models=(
  "nomic-ai/nomic-embed-text-v1.5:CLS:MEAN"
  "Qwen/Qwen3-Embedding-0.6B:LAST:LAST"
  "ibm-granite/granite-embedding-311m-multilingual-r2:CLS:CLS"
)

# `sudo -u ovms` if not already running as that user — the server-owned
# repository is 0750 ovms:ovms, and ovms.service refuses to start models
# it can't open.
if [ "$(id -un)" = "ovms" ]; then
  AS_OVMS=()
else
  AS_OVMS=(sudo -u ovms)
fi
# /etc/ovms/config.json may be owned by root, ovms, or the operator depending
# on packaging. `test -w` already accounts for owner/group/other write bits
# for the current user (and for root), so trust it directly. The old
# group-membership probe was wrong: being *in* the file's group doesn't
# imply the group has the write bit, so it could pick the no-sudo context
# for a file we can't actually write, making `ovms --add_to_config` fail.
if [ -w "$CONFIG" ] || [ "$(id -un)" = "root" ]; then
  AS_CFG=()
else
  AS_CFG=("${AS_OVMS[@]}")
fi

for entry in "${models[@]}"; do
  IFS=":" read -r model pull_pooling runtime_pooling <<< "$entry"
  dest="$REPO/$model"
  if [ -d "$dest" ] && [ "$FORCE" -eq 0 ]; then
    echo "==  $model  (already present in $dest — skip; pass --force to redo)"
  else
    echo "==  $model  (pull, pooling=$pull_pooling, device=$TARGET_DEVICE)"
    extra=()
    [ "$FORCE" -eq 1 ] && extra+=(--overwrite_models)
    wf_args=()
    [ -n "$WEIGHT_FORMAT" ] && wf_args+=(--weight-format "$WEIGHT_FORMAT")
    "${AS_OVMS[@]}" ovms --pull \
        --source_model "$model" \
        --task embeddings \
        --target_device "$TARGET_DEVICE" \
        --pooling "$pull_pooling" \
        --model_repository_path "$REPO" \
        "${wf_args[@]}" \
        "${extra[@]}" || {
      echo "!! pull failed for $model; continuing"
      continue
    }
  fi

  # Patch graph.pbtxt if the runtime pooling differs from what the pull
  # tool would let us set. This is the workaround for OVMS rejecting
  # MEAN at --pull time but accepting it in the calculator.
  graph="$dest/graph.pbtxt"
  if [ "$pull_pooling" != "$runtime_pooling" ] && [ -f "$graph" ]; then
    if grep -q "pooling: $pull_pooling" "$graph"; then
      echo "    patching pooling -> $runtime_pooling"
      "${AS_OVMS[@]}" sed -i "s/pooling: $pull_pooling/pooling: $runtime_pooling/" "$graph"
    fi
  fi

  echo "    register in $CONFIG"
  "${AS_CFG[@]}" ovms --add_to_config \
      --config_path "$CONFIG" \
      --model_repository_path "$REPO" \
      --model_name "$model" >/dev/null || echo "    (already in config or add failed; check $CONFIG)"
done

echo
echo "models in repository:"
"${AS_OVMS[@]}" ovms --list_models --model_repository_path "$REPO" 2>&1 | sed 's/^/  /'

echo
echo "config snapshot ($CONFIG):"
cat "$CONFIG" | python3 -m json.tool 2>/dev/null | sed 's/^/  /'

echo
echo "OVMS polls config.json every --file_system_poll_wait_seconds (default 1s),"
echo "so newly registered models load automatically. Watch progress with:"
echo "  journalctl -u ovms.service -f"
echo
echo "First GPU compile per model is slow (60-180s on Arc A770). Cache writes"
echo "to OVMS_CACHE_DIR (/var/cache/ovms by default) — subsequent loads are fast."
