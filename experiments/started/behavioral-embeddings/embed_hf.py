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


def _effective_max_length(tokenizer, ceiling: int = 32768) -> int:
    """Return the tokenizer's native max_length, capped at `ceiling` and
    floored at 512. Some HF tokenizers report a sentinel value (e.g., 1e9)
    when no limit was set during pre-training; we cap to avoid absurdly
    long tensors. Tokenizers without the attribute fall back to 512.
    """
    n = getattr(tokenizer, "model_max_length", None)
    if not n or n <= 0:
        return 512
    return min(n, ceiling)


def embed_batch(model, tokenizer, texts: list[str], device: str) -> list[list[float]]:
    enc = tokenizer(
        texts,
        padding=True,
        truncation=True,
        max_length=_effective_max_length(tokenizer),
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
    # trust_remote_code=True is required for nomic-ai/nomic-embed-text-v1 and
    # jinaai/jina-embeddings-v2-base-code (custom model implementations on
    # the HF repo). The panel models are all well-known public weights.
    tokenizer = AutoTokenizer.from_pretrained(
        args.model, cache_dir=args.cache_dir, trust_remote_code=True
    )
    model = AutoModel.from_pretrained(
        args.model, cache_dir=args.cache_dir, trust_remote_code=True
    ).to(device)
    model.eval()

    feats = load_features(args.features)
    ids = list(feats.keys())

    # Resume support: if --out already exists (a previous partial run), load
    # it as the starting state and skip ids that already have a vector. This
    # plus per-batch try/except means a transient OOM or tokenizer hiccup
    # mid-run doesn't waste the embedded prefix.
    out: dict[str, list[float]] = {}
    if os.path.exists(args.out):
        try:
            with open(args.out) as f:
                out = json.load(f)
            print(f"resuming: {len(out)} vectors already in {args.out}", file=sys.stderr)
        except (OSError, json.JSONDecodeError) as e:
            print(f"could not resume from {args.out}: {e}; starting fresh", file=sys.stderr)
            out = {}

    todo_ids = [i for i in ids if i not in out]
    failed = 0
    for i in range(0, len(todo_ids), args.batch_size):
        batch_ids = todo_ids[i : i + args.batch_size]
        batch_texts = [build_text(feats[bid]["features"]) for bid in batch_ids]
        try:
            vecs = embed_batch(model, tokenizer, batch_texts, device)
        except Exception as e:
            failed += len(batch_ids)
            print(f"  batch {i//args.batch_size}: skip ({len(batch_ids)} ids): {e}", file=sys.stderr)
            # Flush what we have so far so a later batch failing doesn't kill
            # progress from earlier batches.
            with open(args.out, "w") as f:
                json.dump(out, f)
            continue
        for bid, v in zip(batch_ids, vecs):
            out[bid] = v
        # Periodic flush every 4 batches (≈ 128 records) — bounds work-loss
        # on a hard crash to roughly that interval.
        if (i // args.batch_size) % 4 == 3:
            with open(args.out, "w") as f:
                json.dump(out, f)
        print(f"[{i + len(batch_ids)}/{len(todo_ids)} new] embedded "
              f"(total {len(out)}/{len(ids)})", file=sys.stderr)

    with open(args.out, "w") as f:
        json.dump(out, f)
    print(f"wrote {len(out)} vectors to {args.out} (failed batches: {failed})", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
