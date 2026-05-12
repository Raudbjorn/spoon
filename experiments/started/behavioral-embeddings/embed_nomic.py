"""Embed every fork in features.json via Ollama-served nomic-embed-text."""
import argparse
import json
import sys

import requests

from embed_common import build_text, load_features


def embed_one(endpoint: str, model: str, text: str) -> list[float]:
    # num_ctx=8192 matches nomic-embed-text's native window. Without this,
    # Ollama uses its default 2048 and returns HTTP 500 on prompts that
    # exceed it (Phase A's first run dropped 34/200 records to this).
    r = requests.post(
        f"{endpoint.rstrip('/')}/api/embeddings",
        json={
            "model": model,
            "prompt": text,
            "options": {"num_ctx": 8192},
        },
        timeout=60,
    )
    r.raise_for_status()
    data = r.json()
    if "error" in data and data["error"]:
        raise RuntimeError(f"ollama error: {data['error']}")
    vec = data.get("embedding") or []
    if not vec:
        raise RuntimeError(f"empty embedding from {endpoint}/{model}")
    return vec


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--features", default="features.json")
    ap.add_argument("--out", default="nomic_vectors.json")
    ap.add_argument("--endpoint", default="http://localhost:11434")
    ap.add_argument("--model", default="nomic-embed-text")
    args = ap.parse_args()

    feats = load_features(args.features)
    out: dict[str, list[float]] = {}
    for i, (fid, rec) in enumerate(feats.items(), start=1):
        text = build_text(rec["features"])
        try:
            out[fid] = embed_one(args.endpoint, args.model, text)
        except Exception as e:
            print(f"[{i}/{len(feats)}] skip {fid}: {e}", file=sys.stderr)
            continue
        if i % 25 == 0:
            print(f"[{i}/{len(feats)}] embedded", file=sys.stderr)

    with open(args.out, "w") as f:
        json.dump(out, f)
    print(f"wrote {len(out)} vectors to {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
