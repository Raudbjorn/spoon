"""Shared helpers for the panel embedding scripts.

Phase A used per-modality char caps (PATHS_MAX_CHARS, etc.) totaling ~5 KB
to work around 512-token-window embedders (CodeBERT family) and Ollama's
default 2048 num_ctx for nomic. The Phase B panel uses long-context models
(7K-32K) and Ollama with num_ctx=8192, so the artificial caps would
handicap candidates and baseline alike. Concatenate the full payload;
let each tokenizer's natural truncation apply.
"""
import json
from typing import Any


def build_text(features: dict[str, str]) -> str:
    """Concatenate the four ForkFeatures modalities with structural tags.

    Mirrors spoon's codeAwareEmbed path (internal/embed/multimodal.go:53).
    No truncation here — each downstream tokenizer truncates at its own
    model_max_length.
    """
    paths = features.get("Paths", "")
    commits = features.get("Commits", "")
    readme = features.get("ReadmeDoc", "")
    diff = features.get("DiffChunk", "")
    return (
        f"<paths>{paths}</paths>"
        f"<commits>{commits}</commits>"
        f"<readme>{readme}</readme>"
        f"<diff>{diff}</diff>"
    )


def load_features(path: str) -> dict[str, dict[str, Any]]:
    """Load features.json and index by fork id."""
    with open(path) as f:
        records = json.load(f)
    return {r["id"]: r for r in records}
