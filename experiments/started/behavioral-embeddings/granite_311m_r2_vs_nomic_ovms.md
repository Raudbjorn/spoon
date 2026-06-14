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
./run.sh
```
