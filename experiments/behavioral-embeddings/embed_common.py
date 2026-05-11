"""Shared helpers for both embedding scripts."""
import json
from typing import Any


def build_text(features: dict[str, str]) -> str:
    """Concatenate the four ForkFeatures modalities with structural tags.

    Mirrors spoon's codeAwareEmbed path (internal/embed/multimodal.go:53)
    so the experiment exercises the same input layout the production
    pipeline would use.
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
