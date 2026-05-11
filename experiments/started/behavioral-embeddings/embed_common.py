"""Shared helpers for both embedding scripts."""
import json
from typing import Any

# Per-modality character caps applied BEFORE concatenation. Picked so that
# every embedder sees the same truncated text:
#   - Ollama's nomic-embed-text returns 500 Internal Server Error on prompts
#     that exceed ~8K tokens; the huge-fork PRs in this dataset (hundreds of
#     changed paths) hit that limit and were dropped (34/200 in the first
#     run). Capping per-modality keeps total length comfortably inside the
#     window.
#   - CodeExecutor truncates at 512 tokens; the cap below lands roughly
#     within that window so the model sees a representative slice of each
#     modality instead of just the first ~512 tokens of paths.
# Caps total ≈ 5 KB which is what spoon's BuildFeatures aims for in
# production (DiffChunk alone is capped at 4 KB upstream).
PATHS_MAX_CHARS = 1500
COMMITS_MAX_CHARS = 1000
README_MAX_CHARS = 1000
DIFF_MAX_CHARS = 1500


def _truncate(s: str, n: int) -> str:
    if len(s) <= n:
        return s
    return s[:n]


def build_text(features: dict[str, str]) -> str:
    """Concatenate the four ForkFeatures modalities with structural tags.

    Each modality is truncated to a per-modality character cap so both
    embedders see the same input. This protects the nomic-via-Ollama path
    from 500 errors on oversize prompts AND keeps the CodeExecutor 512-token
    window from being eaten entirely by long path lists.

    Mirrors spoon's codeAwareEmbed path (internal/embed/multimodal.go:53).
    """
    paths = _truncate(features.get("Paths", ""), PATHS_MAX_CHARS)
    commits = _truncate(features.get("Commits", ""), COMMITS_MAX_CHARS)
    readme = _truncate(features.get("ReadmeDoc", ""), README_MAX_CHARS)
    diff = _truncate(features.get("DiffChunk", ""), DIFF_MAX_CHARS)
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
