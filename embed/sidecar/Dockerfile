# syntax=docker/dockerfile:1.7
FROM python:3.12-slim AS deps

ENV PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential git && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY requirements.txt .
RUN pip install --upgrade pip && pip install -r requirements.txt

# Pre-fetch the model weights into the image so first-request latency is low.
RUN python -c "from sentence_transformers import SentenceTransformer; \
    SentenceTransformer('Snowflake/snowflake-arctic-embed-l-v2.0', trust_remote_code=True)"

FROM python:3.12-slim AS runtime

ENV PYTHONUNBUFFERED=1 \
    SPOON_SIDECAR_DEVICE=cpu \
    HF_HOME=/root/.cache/huggingface

WORKDIR /app
COPY --from=deps /usr/local/lib/python3.12/site-packages /usr/local/lib/python3.12/site-packages
COPY --from=deps /usr/local/bin /usr/local/bin
COPY --from=deps /root/.cache/huggingface /root/.cache/huggingface
COPY server.py .

EXPOSE 8765
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
    CMD python -c "import urllib.request, sys; urllib.request.urlopen('http://localhost:8765/health', timeout=3).read()" || exit 1

CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8765"]
