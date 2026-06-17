# Behavioral Embeddings Validation — Results

| metric | value |
| --- | --- |
| Kendall's tau (nomic-embed-text) | 0.0883 |
| Kendall's tau (CodeExecutor) | 0.0313 |
| Delta (CE − nomic) | -0.0570 |
| Gate threshold | 0.05 |
| Pairs scored | 53 |
| Pairs skipped (missing vector) | 0 |

## Verdict

**FAIL — feature rejected; do not build Phase B**

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
./run.sh
```
