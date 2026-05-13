"""Benchmark the spoon sidecar against features.json from the panel re-test.

Usage:
    .venv/bin/python bench.py --features ../../experiments/started/behavioral-embeddings/features.json
    .venv/bin/python bench.py --sidecar http://localhost:8765 --batch-size 16
"""
import argparse
import json
import time

import requests


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--features", required=True)
    ap.add_argument("--sidecar", default="http://localhost:8765")
    ap.add_argument("--batch-size", type=int, default=16)
    args = ap.parse_args()

    with open(args.features) as f:
        records = json.load(f)
    texts = [
        f"<paths>{r['features']['Paths']}</paths>"
        f"<commits>{r['features']['Commits']}</commits>"
        f"<readme>{r['features']['ReadmeDoc']}</readme>"
        f"<diff>{r['features']['DiffChunk']}</diff>"
        for r in records
    ]

    print(f"benchmarking {len(texts)} texts via {args.sidecar} at batch={args.batch_size}")
    t0 = time.monotonic()
    total_vectors = 0
    for i in range(0, len(texts), args.batch_size):
        batch = texts[i : i + args.batch_size]
        r = requests.post(
            f"{args.sidecar}/embed",
            json={"texts": batch},
            timeout=120,
        )
        r.raise_for_status()
        total_vectors += len(r.json()["vectors"])
    elapsed = time.monotonic() - t0
    rps = total_vectors / elapsed
    print(f"total: {total_vectors} vectors in {elapsed:.1f}s = {rps:.1f} req/sec")
    print(f"avg latency per record: {1000 * elapsed / total_vectors:.0f} ms")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
