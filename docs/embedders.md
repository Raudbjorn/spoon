# Spoon Embedders

Spoon's fork-clustering pipeline uses text embeddings to compare forks.
Two embedder backends are supported.

## Default: Ollama (`nomic-embed-text`)

Zero setup if Ollama is installed and running. Pulled automatically via
`spn embed pull nomic-embed-text` or on first cluster run with
`SPOON_AUTO_PULL=1`.

```sh
spn forks list golang/go
```

## Optional: Behavioral-embeddings sidecar (`arctic-embed-l-v2`)

A higher-fidelity embedder (Δ = +0.12 Kendall's tau over nomic on the
behavioral-embeddings gate experiment). Requires a Python sidecar process.

```sh
docker build -t spoon-sidecar embed/sidecar/
docker run --rm -d -p 8765:8765 spoon-sidecar
spn forks list --embedder-backend sidecar golang/go
```

Or via env: `SPOON_EMBEDDER_BACKEND=sidecar SPOON_SIDECAR_ENDPOINT=http://localhost:8765 spn forks list golang/go`.

Falls back to Ollama if the sidecar is unreachable.

See `embed/sidecar/README.md` for sidecar setup details. See
`experiments/started/behavioral-embeddings/RESULTS_PANEL.md` for the
evaluation that motivated this choice.

## Choosing a backend

| Property | Ollama (`nomic-embed-text`) | Sidecar (`arctic-embed-l-v2`) |
|---|---|---|
| Setup | `ollama pull` (zero-config if Ollama installed) | `docker run` or Python venv |
| Memory | ~280 MB resident | ~2 GB resident |
| Quality (Kendall's tau on intent-pair ranking) | baseline | +0.12 |
| Embedding dim | 768 | 1024 |
| First-request latency | ~50 ms | ~50–80 ms (after model load) |
| Operational complexity | low (single process) | medium (separate sidecar; needs Docker or Python) |

If you don't have a strong reason to switch, the default is fine. The
sidecar pays off most clearly on large fork sets where same-intent pair
ranking matters (incremental clustering, novelty scoring with many candidates).
