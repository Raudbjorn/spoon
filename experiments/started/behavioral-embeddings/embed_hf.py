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


def _pool_mean(hidden, attention_mask):
    """Mean-pool over the masked sequence. Correct for encoder models."""
    mask = attention_mask.unsqueeze(-1).to(hidden.dtype)  # [B, T, 1]
    summed = (hidden * mask).sum(dim=1)  # [B, H]
    counts = mask.sum(dim=1).clamp(min=1)  # [B, 1]
    return summed / counts  # [B, H]


def _pool_last_token(hidden, attention_mask):
    """Last-non-padding-token pooling. Correct for left-to-right decoder
    models — using mean-pool on a decoder is a known suboptimal choice
    (see RESULTS_PANEL.md "Additional finding: SFR-2B is worse than
    SFR-400M"). For right-padded inputs, the last real token is at
    index `sum(mask) - 1` per row; for left-padded inputs it's the last
    position. We detect by checking whether position 0 is masked
    anywhere in the batch (left-padding signal) — if so, take index -1
    per row; otherwise take the per-row right-edge of attention.
    """
    seq_lens = attention_mask.sum(dim=1) - 1  # [B], last real index
    # Detect left-padding: in a left-padded tensor, position 0 has mask=0
    # for any row shorter than the batch max. If ALL rows have mask=1 at
    # position 0, the batch is right-padded (or all same length).
    left_padded = (attention_mask[:, 0] == 0).any().item()
    if left_padded:
        # Last token is at the rightmost position for every row.
        return hidden[:, -1, :]
    # Right-padded: gather the last real token per row.
    batch_idx = torch.arange(hidden.size(0), device=hidden.device)
    return hidden[batch_idx, seq_lens]


POOL_FNS = {
    "mean": _pool_mean,
    "last_token": _pool_last_token,
}


def _select_device() -> str:
    """Pick the best device for HF transformers on this host.

    Order: CUDA → Intel XPU (Arc A770 etc.) → CPU. Native torch.xpu lives in
    torch 2.5+; intel_extension_for_pytorch (IPEX) is imported when present
    because pre-2.7 stacks need its side-effect registration before
    torch.xpu.is_available() returns True.
    """
    if torch.cuda.is_available():
        return "cuda"
    try:
        import intel_extension_for_pytorch  # noqa: F401 — registers xpu device
    except ImportError:
        pass
    if hasattr(torch, "xpu") and torch.xpu.is_available():
        return "xpu"
    return "cpu"


def embed_batch(model, tokenizer, texts: list[str], device: str, pooling: str = "mean") -> list[list[float]]:
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
    pool_fn = POOL_FNS[pooling]
    pooled = pool_fn(hidden, enc["attention_mask"])
    return pooled.cpu().tolist()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--features", default="features.json")
    ap.add_argument("--out", default="codeexecutor_vectors.json")
    ap.add_argument("--model", default="microsoft/codeexecutor")
    ap.add_argument("--cache-dir", default="hf_cache")
    ap.add_argument("--batch-size", type=int, default=32)
    ap.add_argument(
        "--pooling",
        choices=sorted(POOL_FNS.keys()),
        default="mean",
        help="Hidden-state pooling. 'mean' for encoders (default; matches "
        "original panel). 'last_token' for decoder-style embedders (Qwen3, "
        "SFR-2B) — mean-pool on a decoder is the known SFR-2B gotcha.",
    )
    args = ap.parse_args()

    os.makedirs(args.cache_dir, exist_ok=True)
    device = _select_device()
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
            vecs = embed_batch(model, tokenizer, batch_texts, device, pooling=args.pooling)
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
