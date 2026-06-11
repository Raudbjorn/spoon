# Behavioral Embeddings Validation — Results

| metric | value |
| --- | --- |
| Kendall's tau (nomic-embed-text) | 0.1595 |
| Kendall's tau (CodeExecutor) | 0.2136 |
| Delta (CE − nomic) | 0.0541 |
| Gate threshold | 0.05 |
| Pairs scored | 53 |
| Pairs skipped (missing vector) | 0 |

## Verdict

**PASS — proceed to Phase B (sidecar build)**

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
./run.sh
```
