# Spoon Embedders

Spoon's fork-clustering pipeline uses text embeddings to compare forks.
Three embedder backends are supported: `ollama` (default), `sidecar`, and
`openai`.

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
docker run --rm -d -p 8766:8766 spoon-sidecar
spn forks list --embedder-backend sidecar golang/go
```

Or via env: `SPOON_EMBEDDER_BACKEND=sidecar SPOON_SIDECAR_ENDPOINT=http://localhost:8766 spn forks list golang/go`.

Falls back to Ollama if the sidecar is unreachable.

See `embed/sidecar/README.md` for sidecar setup details. See
`experiments/started/behavioral-embeddings/RESULTS_PANEL.md` for the
evaluation that motivated this choice.

## Optional: OpenAI-compatible endpoint (`openai`) — incl. GPU via OVMS

Point spoon at any server that speaks the OpenAI embeddings API
(`POST {base}/embeddings`). This covers [OpenVINO Model Server
(OVMS)](https://github.com/openvinotoolkit/model_server) — which serves
embedding models on an Intel GPU — as well as vLLM, LocalAI, and the OpenAI
API itself. This is the recommended way to run embeddings on a GPU.

```sh
# OVMS serving an embedding model on an Intel Arc GPU (OpenAI /v3 API):
spn forks list --embedder-backend openai \
  --embedder http://localhost:8000 \
  --embedder-model nomic-ai/nomic-embed-text-v1.5 \
  golang/go
```

Or via env: `SPOON_EMBEDDER_BACKEND=openai SPOON_OPENAI_BASE_URL=http://localhost:8000`.
Set `OPENAI_API_KEY` for hosted endpoints that require auth (OVMS does not).

The base URL's API version is inferred: pass `.../v1` for OpenAI/vLLM; a bare
host (e.g. `http://localhost:8000`) defaults to OVMS's `/v3`. `spoon setup
--embedder-backend openai --embedder URL` reports reachability and the served
model list. The default model is `nomic-ai/nomic-embed-text-v1.5`.

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
