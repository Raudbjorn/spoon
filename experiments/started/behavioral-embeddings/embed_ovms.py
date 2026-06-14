"""Embed every fork in features.json via OpenVINO Model Server.

Speaks OVMS's OpenAI-compatible `/v3/embeddings` endpoint. The model must
already be loaded by the server (run ./setup_ovms_models.sh from this
directory — on a host with the ovms package it delegates to the packaged
/usr/lib/ovms/contrib/setup_embeddings_arc.sh helper). Inference target
(CPU / GPU / NPU) is decided when the model is pulled, not by this client;
for Intel Arc A770 the helper pulls with --target_device GPU.

Same on-disk vector format and resume semantics as embed_hf.py so
analyze.py consumes either interchangeably.
"""
import argparse
import json
import os
import sys
import tempfile

import requests

from embed_common import build_text, load_features


def _atomic_dump(obj: object, path: str) -> None:
    """Write JSON to ``path`` atomically.

    A bare ``json.dump`` to the destination leaves a truncated/corrupt file
    if the process is interrupted (Ctrl+C, OOM) mid-write — fatal for the
    resume path, which trusts whatever is on disk. Write to a temp file in
    the same directory, then ``os.replace`` (atomic on POSIX) over the
    target, so a partial write can never clobber a good checkpoint.
    """
    dst_dir = os.path.dirname(path) or "."
    with tempfile.NamedTemporaryFile("w", dir=dst_dir, delete=False) as tf:
        json.dump(obj, tf)
        tmp_name = tf.name
    os.replace(tmp_name, path)


def embed_batch(endpoint: str, model: str, texts: list[str], timeout: float) -> list[list[float]]:
    """One /v3/embeddings call for a batch. Raises on transport or API error."""
    r = requests.post(
        f"{endpoint.rstrip('/')}/v3/embeddings",
        json={"model": model, "input": texts, "encoding_format": "float"},
        timeout=timeout,
    )
    r.raise_for_status()
    body = r.json()
    if "error" in body and body["error"]:
        raise RuntimeError(f"ovms error: {body['error']}")
    data = body.get("data") or []
    if len(data) != len(texts):
        raise RuntimeError(
            f"ovms returned {len(data)} embeddings for {len(texts)} inputs"
        )
    # Preserve client-side order via the `index` field (OpenAI spec — OVMS
    # may return out of order under pipelining).
    out: list[list[float] | None] = [None] * len(texts)
    for item in data:
        # `index` is required to map a result back to its input slot; a
        # missing/out-of-range value (or a duplicate clobbering an earlier
        # slot) means we can't trust the ordering, so fail loudly instead
        # of silently writing to slot 0 or raising a bare IndexError.
        # `type(idx) is int` (rather than isinstance) also rejects bool, which
        # is a subclass of int.
        idx = item.get("index")
        if type(idx) is not int or not (0 <= idx < len(texts)):
            raise RuntimeError(f"ovms returned out-of-bounds or missing index {idx!r}")
        if out[idx] is not None:
            raise RuntimeError(f"ovms returned duplicate index {idx}")
        vec = item.get("embedding") or []
        if not vec:
            raise RuntimeError(f"empty embedding at index {idx}")
        out[idx] = vec
    if any(v is None for v in out):
        raise RuntimeError("ovms response missing index slot")
    return out  # type: ignore[return-value]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--features", default="features.json")
    ap.add_argument("--out", required=True)
    ap.add_argument(
        "--model",
        required=True,
        help="Model name registered in OVMS config.json (HF source path "
        "by default, e.g. Qwen/Qwen3-Embedding-0.6B).",
    )
    ap.add_argument(
        "--endpoint",
        default=os.environ.get("OVMS_ENDPOINT", "http://localhost:8978"),
        help="OVMS REST base URL. Defaults to $OVMS_ENDPOINT or "
        "http://localhost:8978 (the ovms.service unit override on this "
        "host).",
    )
    ap.add_argument("--batch-size", type=int, default=4)
    ap.add_argument(
        "--timeout",
        type=float,
        default=300.0,
        help="Per-batch HTTP timeout. First call on a GPU target compiles "
        "the kernel and is much slower than steady-state — keep generous.",
    )
    args = ap.parse_args()

    feats = load_features(args.features)
    ids = list(feats.keys())

    # Resume: same on-disk format as embed_hf.py, so partial runs survive.
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
            vecs = embed_batch(args.endpoint, args.model, batch_texts, args.timeout)
        except Exception as e:
            failed += len(batch_ids)
            print(
                f"  batch {i // args.batch_size}: skip "
                f"({len(batch_ids)} ids): {e}",
                file=sys.stderr,
            )
            _atomic_dump(out, args.out)
            continue
        for bid, v in zip(batch_ids, vecs):
            out[bid] = v
        if (i // args.batch_size) % 4 == 3:
            _atomic_dump(out, args.out)
        print(
            f"[{i + len(batch_ids)}/{len(todo_ids)} new] embedded "
            f"(total {len(out)}/{len(ids)})",
            file=sys.stderr,
        )

    _atomic_dump(out, args.out)
    print(
        f"wrote {len(out)} vectors to {args.out} (failed batches: {failed})",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
