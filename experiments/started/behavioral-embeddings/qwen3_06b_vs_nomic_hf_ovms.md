# Behavioral Embeddings Validation — Results

| metric | value |
| --- | --- |
| Kendall's tau (nomic-embed-text) | 0.1794 |
| Kendall's tau (CodeExecutor) | 0.2022 |
| Delta (CE − nomic) | 0.0228 |
| Gate threshold | 0.05 |
| Pairs scored | 53 |
| Pairs skipped (missing vector) | 0 |

## Verdict

**FAIL — feature rejected; do not build Phase B**

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
# Embed the OVMS candidates (writes qwen3_06b_vectors.json):
OVMS_SKIP_PULL=1 ./run_panel_supplement_ovms.sh
# Build the HF-served nomic baseline (torch+XPU venv from requirements-hf.txt):
uv venv .venv-hf && uv pip install --python .venv-hf/bin/python -r requirements-hf.txt
LD_LIBRARY_PATH="$PWD/.venv-hf/lib:$LD_LIBRARY_PATH" .venv-hf/bin/python embed_hf.py \
  --model nomic-ai/nomic-embed-text-v1 --features features.json \
  --out nomic_hf_vectors.json --cache-dir hf_cache --batch-size 4 --pooling mean
# Score this pairing:
.venv-ovms/bin/python analyze.py \
  --nomic nomic_hf_vectors.json \
  --codeexecutor qwen3_06b_vectors.json \
  --out qwen3_06b_vs_nomic_hf_ovms.md
```
