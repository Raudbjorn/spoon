# Spoon Behavioral-Embeddings Sidecar

A Python service that serves `Snowflake/snowflake-arctic-embed-l-v2.0`
(Apache 2.0) as an HTTP embedder for spoon's fork-clustering pipeline.

## When to use it

The default spoon embedder is Ollama-served `nomic-embed-text` (8 K context,
274 MB model, drop-in zero-setup). The arctic sidecar produces fork-pair
similarity rankings that match human judgment ~0.12 Kendall's tau better
than nomic-embed-text on a 53-pair hand-curated test
(see `../../experiments/started/behavioral-embeddings/RESULTS_PANEL.md`).

Trade-off: ~2 GB resident memory and a Python process to manage.

## Running

### Docker (recommended)

```sh
docker build -t spoon-sidecar embed/sidecar/
docker run --rm -d -p 8765:8765 --name spoon-sidecar spoon-sidecar
```

Then point spoon at it:

```sh
spn forks list --embedder-backend sidecar --sidecar-endpoint http://localhost:8765 owner/repo
```

### Local Python

```sh
cd embed/sidecar
uv venv .venv
uv pip install --python .venv/bin/python -r requirements.txt
.venv/bin/uvicorn server:app --host 0.0.0.0 --port 8765
```

First start downloads ~700 MB of weights to `~/.cache/huggingface/`.

## Endpoints

| method | path | request | response |
|---|---|---|---|
| `GET`  | `/health` | — | `{"status":"ok","model":"...","device":"cpu","dim":1024}` |
| `POST` | `/embed`  | `{"texts":[...]}` | `{"vectors":[[...],...],"dim":1024}` |

## Environment

| var | default | meaning |
|---|---|---|
| `SPOON_SIDECAR_MODEL`  | `Snowflake/snowflake-arctic-embed-l-v2.0` | HF model to load |
| `SPOON_SIDECAR_DEVICE` | `cpu` | `cuda` to use GPU if available |

## License & attribution

The model `Snowflake/snowflake-arctic-embed-l-v2.0` is distributed by Snowflake
under the Apache License 2.0. Redistribution (including in the Docker image
built by this directory's Dockerfile) is permitted with attribution. See
[the model card](https://huggingface.co/Snowflake/snowflake-arctic-embed-l-v2.0)
for the canonical license text.
