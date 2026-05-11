#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

if [ ! -d .venv ]; then
  uv venv .venv
  uv pip install --python .venv/bin/python -r requirements.txt
fi

if [ ! -f features.json ]; then
  echo "features.json missing; run: go run ./cmd/dump_features -repo IBM/mcp-context-forge -n 200 -out features.json" >&2
  exit 2
fi

if [ ! -f judgments.json ]; then
  echo "judgments.json missing; see README.md for the curation procedure" >&2
  exit 2
fi

.venv/bin/python embed_nomic.py --features features.json --out nomic_vectors.json
.venv/bin/python embed_codeexecutor.py --features features.json --out codeexecutor_vectors.json
.venv/bin/python analyze.py --features features.json --judgments judgments.json \
  --nomic nomic_vectors.json --codeexecutor codeexecutor_vectors.json --out RESULTS.md
echo "wrote RESULTS.md"
