"""Behavioral-embeddings sidecar for spoon.

Loads Snowflake/snowflake-arctic-embed-l-v2.0 via sentence-transformers
(which handles the model's CLS-pooling and L2-normalization conventions
automatically), and exposes POST /embed + GET /health.
"""
import logging
import os
import sys
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from sentence_transformers import SentenceTransformer

DEFAULT_MODEL = "Snowflake/snowflake-arctic-embed-l-v2.0"
MODEL_NAME = os.environ.get("SPOON_SIDECAR_MODEL", DEFAULT_MODEL)
DEVICE = "cuda" if os.environ.get("SPOON_SIDECAR_DEVICE", "cpu") == "cuda" else "cpu"

logger = logging.getLogger("spoon.sidecar")
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")

_model: SentenceTransformer | None = None


@asynccontextmanager
async def lifespan(app: FastAPI):
    global _model
    logger.info(f"loading {MODEL_NAME} on {DEVICE}")
    _model = SentenceTransformer(MODEL_NAME, device=DEVICE, trust_remote_code=True)
    logger.info(f"loaded; dim={_model.get_sentence_embedding_dimension()}")
    yield
    _model = None
    logger.info("shutdown")


app = FastAPI(title="spoon-behavioral-embeddings-sidecar", lifespan=lifespan)


class EmbedRequest(BaseModel):
    texts: list[str]


class EmbedResponse(BaseModel):
    vectors: list[list[float]]
    dim: int


@app.get("/health")
def health() -> dict[str, Any]:
    if _model is None:
        raise HTTPException(status_code=503, detail="model not loaded")
    return {
        "status": "ok",
        "model": MODEL_NAME,
        "device": DEVICE,
        "dim": _model.get_sentence_embedding_dimension(),
    }


@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest) -> EmbedResponse:
    if _model is None:
        raise HTTPException(status_code=503, detail="model not loaded")
    if not req.texts:
        return EmbedResponse(vectors=[], dim=_model.get_sentence_embedding_dimension())
    vecs = _model.encode(
        req.texts,
        batch_size=16,
        show_progress_bar=False,
        normalize_embeddings=True,
        convert_to_numpy=False,
    )
    return EmbedResponse(
        vectors=[v.tolist() for v in vecs],
        dim=_model.get_sentence_embedding_dimension(),
    )
