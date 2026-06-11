# Behavioral Embeddings Validation — Results

| metric | value |
| --- | --- |
| Kendall's tau (nomic-embed-text) | 0.3356 |
| Kendall's tau (CodeExecutor) | 0.5156 |
| Delta (CE − nomic) | 0.1799 |
| Gate threshold | 0.05 |
| Pairs scored | 34 |
| Pairs skipped (missing vector) | 19 |

## Verdict

**PASS — proceed to Phase B (sidecar build)**

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
./run.sh
```
