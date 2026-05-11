# Behavioral Embeddings — Validation Experiment

Gate experiment for the deferred behavioral-embeddings feature
(`docs/superpowers/specs/future/future-work-behavioral-embeddings.md`).

## Question

Does swapping nomic-embed-text for CodeExecutor produce embeddings that rank
fork-pair similarity more like a human does?

## Method

1. `cmd/dump_features` runs the existing spoon pipeline over the top-200 forks
   of `IBM/mcp-context-forge` and dumps each fork's `embed.ForkFeatures` as
   JSON to `features.json`.
2. `judgments.json` records 40 hand-curated fork pairs: 20 labeled `1`
   (functionally same intent) and 20 labeled `0` (functionally different).
3. `embed_nomic.py` hits the local Ollama `/api/embeddings` endpoint once per
   fork and writes `nomic_vectors.json`.
4. `embed_codeexecutor.py` loads `microsoft/codeexecutor` via the HuggingFace
   transformers library and writes `codeexecutor_vectors.json`.
5. `analyze.py` computes pair-wise cosine similarity for each embedder,
   computes Kendall's tau against the hand judgments, prints both taus and
   the delta, and writes `RESULTS.md`.

## Gate

The feature is greenlit iff `tau(codeexecutor) - tau(nomic) >= 0.05`.

## Reproduce

```sh
cd experiments/behavioral-embeddings
./run.sh
```

Requires:
- Ollama running on `http://localhost:11434` with `nomic-embed-text` pulled.
- `gh auth status` showing an authenticated user (for `dump_features`).
- Python 3.11+ and `uv` on PATH.
- ~4 GB free RAM and ~1 GB free disk for the model cache.
