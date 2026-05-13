# Behavioral Embeddings Validation — Results

| metric | value |
| --- | --- |
| Kendall's tau (nomic-embed-text) | 0.3878 |
| Kendall's tau (CodeExecutor) | 0.4898 |
| Delta (CE − nomic) | 0.1020 |
| Gate threshold | 0.05 |
| Pairs scored | 28 |
| Pairs skipped (missing vector) | 25 |

## Verdict

**PASS — proceed to Phase B (sidecar build)**

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
./run.sh
```
