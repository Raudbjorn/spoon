"""Embed every fork in features.json via local HF-served CodeExecutor.

Uses mean-pooling over the last hidden state to produce a single vector per
input. Model is downloaded to ./hf_cache on first run (~500 MB).
"""
import argparse
import json
import os
import sys

import torch
from transformers import AutoModel, AutoTokenizer

from embed_common import build_text, load_features


def embed_batch(model, tokenizer, texts: list[str], device: str) -> list[list[float]]:
    enc = tokenizer(
        texts,
        padding=True,
        truncation=True,
        max_length=512,
        return_tensors="pt",
    ).to(device)
    with torch.no_grad():
        out = model(**enc)
    hidden = out.last_hidden_state  # [B, T, H]
    mask = enc["attention_mask"].unsqueeze(-1).to(hidden.dtype)  # [B, T, 1]
    summed = (hidden * mask).sum(dim=1)  # [B, H]
    counts = mask.sum(dim=1).clamp(min=1)  # [B, 1]
    pooled = summed / counts  # [B, H]
    return pooled.cpu().tolist()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--features", default="features.json")
    ap.add_argument("--out", default="codeexecutor_vectors.json")
    ap.add_argument("--model", default="microsoft/codeexecutor")
    ap.add_argument("--cache-dir", default="hf_cache")
    ap.add_argument("--batch-size", type=int, default=32)
    args = ap.parse_args()

    os.makedirs(args.cache_dir, exist_ok=True)
    device = "cuda" if torch.cuda.is_available() else "cpu"
    print(f"loading {args.model} on {device}", file=sys.stderr)
    tokenizer = AutoTokenizer.from_pretrained(args.model, cache_dir=args.cache_dir)
    model = AutoModel.from_pretrained(args.model, cache_dir=args.cache_dir).to(device)
    model.eval()

    feats = load_features(args.features)
    ids = list(feats.keys())
    out: dict[str, list[float]] = {}
    for i in range(0, len(ids), args.batch_size):
        batch_ids = ids[i : i + args.batch_size]
        batch_texts = [build_text(feats[bid]["features"]) for bid in batch_ids]
        vecs = embed_batch(model, tokenizer, batch_texts, device)
        for bid, v in zip(batch_ids, vecs):
            out[bid] = v
        print(f"[{i + len(batch_ids)}/{len(ids)}] embedded", file=sys.stderr)

    with open(args.out, "w") as f:
        json.dump(out, f)
    print(f"wrote {len(out)} vectors to {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
