# Behavioral Embeddings Validation — Results

| metric | value |
| --- | --- |
| Kendall's tau (nomic-embed-text) | 0.3396 |
| Kendall's tau (CodeExecutor) | 0.3396 |
| Delta (CE − nomic) | 0.0000 |
| Gate threshold | 0.05 |
| Pairs scored | 35 |
| Pairs skipped (missing vector) | 18 |

## Verdict

**FAIL — feature rejected; do not build Phase B**

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
./run.sh
```
