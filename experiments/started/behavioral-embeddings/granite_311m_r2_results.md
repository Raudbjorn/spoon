# Behavioral Embeddings Validation — Results

| metric | value |
| --- | --- |
| Kendall's tau (nomic-embed-text) | 0.3289 |
| Kendall's tau (CodeExecutor) | 0.4267 |
| Delta (CE − nomic) | 0.0978 |
| Gate threshold | 0.05 |
| Pairs scored | 30 |
| Pairs skipped (missing vector) | 23 |

## Verdict

**PASS — proceed to Phase B (sidecar build)**

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
./run.sh
```
