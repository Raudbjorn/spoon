# Behavioral Embeddings Validation — Results

| metric | value |
| --- | --- |
| Kendall's tau (nomic-embed-text) | 0.3396 |
| Kendall's tau (CodeExecutor) | 0.4963 |
| Delta (CE − nomic) | 0.1567 |
| Gate threshold | 0.05 |
| Pairs scored | 35 |
| Pairs skipped (missing vector) | 18 |

## Verdict

**PASS — proceed to Phase B (sidecar build)**

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
# Embed the OVMS candidates (writes granite_311m_r2_vectors.json);
# nomic_vectors.json is the Ollama/panel baseline:
OVMS_SKIP_PULL=1 ./run_panel_supplement_ovms.sh
# Score this pairing:
.venv-ovms/bin/python analyze.py \
  --nomic nomic_vectors.json \
  --codeexecutor granite_311m_r2_vectors.json \
  --out granite_311m_r2_vs_nomic_ovms.md
```
