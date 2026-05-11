"""Compute cosine similarity, Kendall's tau, and the gate decision."""
import argparse
import json
import math
import sys
from typing import Any

from scipy.stats import kendalltau


def cosine(u: list[float], v: list[float]) -> float:
    """Cosine similarity. Returns 0.0 if either input is zero-norm."""
    nu = math.sqrt(sum(x * x for x in u))
    nv = math.sqrt(sum(x * x for x in v))
    if nu == 0.0 or nv == 0.0:
        return 0.0
    dot = sum(a * b for a, b in zip(u, v))
    return dot / (nu * nv)


def pair_similarities(
    vectors: dict[str, list[float]], judgments: list[dict[str, Any]]
) -> list[float | None]:
    """Return one cosine per judgment, or None when a fork's vector is missing."""
    out: list[float | None] = []
    for j in judgments:
        a, b = vectors.get(j["a"]), vectors.get(j["b"])
        if a is None or b is None:
            out.append(None)
            continue
        out.append(cosine(a, b))
    return out


def kendall_tau_vs_labels(sims: list[float], labels: list[int]) -> float:
    """Kendall's tau between a similarity ranking and binary labels.

    A pair labeled 1 (same intent) should rank higher in cosine sim than a
    pair labeled 0. tau=+1 means the ranking is perfectly aligned with the
    labels; tau=-1 means perfectly inverted.
    """
    tau, _p = kendalltau(sims, labels, variant="c")
    if math.isnan(tau):
        return 0.0
    return float(tau)


def compute_gate(
    nomic_sims: list[float | None],
    ce_sims: list[float | None],
    labels: list[int],
    threshold: float,
) -> dict[str, Any]:
    """Pure gate-decision logic. Drops any pair where either embedder failed
    to produce a vector, computes Kendall's tau for both embedders against
    the labels, and returns the decision payload.
    """
    if not (len(nomic_sims) == len(ce_sims) == len(labels)):
        raise ValueError("sims/labels length mismatch")

    kept_nomic: list[float] = []
    kept_ce: list[float] = []
    kept_labels: list[int] = []
    skipped = 0
    for ns, cs, lab in zip(nomic_sims, ce_sims, labels):
        if ns is None or cs is None:
            skipped += 1
            continue
        kept_nomic.append(ns)
        kept_ce.append(cs)
        kept_labels.append(int(lab))

    if len(kept_labels) < 2:
        raise RuntimeError(
            f"too few labeled pairs survived vector lookup ({len(kept_labels)}); "
            "check that vector keys line up with judgment fork ids"
        )

    tau_nomic = kendall_tau_vs_labels(kept_nomic, kept_labels)
    tau_ce = kendall_tau_vs_labels(kept_ce, kept_labels)
    delta = tau_ce - tau_nomic

    return {
        "tau_nomic": tau_nomic,
        "tau_codeexecutor": tau_ce,
        "delta": delta,
        "threshold": threshold,
        "pass": delta >= threshold,
        "n_pairs_kept": len(kept_labels),
        "n_pairs_skipped": skipped,
    }


def run_gate(
    features_path: str,
    judgments_path: str,
    nomic_path: str,
    ce_path: str,
    threshold: float = 0.05,
) -> dict[str, Any]:
    """Disk wrapper: load files, hand sims/labels to compute_gate."""
    del features_path  # currently unused; kept in signature for future audit fields
    with open(judgments_path) as f:
        judgments = json.load(f)
    with open(nomic_path) as f:
        nomic_vecs = json.load(f)
    with open(ce_path) as f:
        ce_vecs = json.load(f)

    nomic_sims = pair_similarities(nomic_vecs, judgments)
    ce_sims = pair_similarities(ce_vecs, judgments)
    labels = [int(j["label"]) for j in judgments]
    return compute_gate(nomic_sims, ce_sims, labels, threshold)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--features", default="features.json")
    ap.add_argument("--judgments", default="judgments.json")
    ap.add_argument("--nomic", default="nomic_vectors.json")
    ap.add_argument("--codeexecutor", default="codeexecutor_vectors.json")
    ap.add_argument("--threshold", type=float, default=0.05)
    ap.add_argument("--out", default="RESULTS.md")
    args = ap.parse_args()

    result = run_gate(
        args.features, args.judgments, args.nomic, args.codeexecutor, args.threshold
    )
    write_results_md(args.out, result)
    print(json.dumps(result, indent=2))
    return 0


def write_results_md(path: str, r: dict[str, Any]) -> None:
    verdict = "**PASS — proceed to Phase B (sidecar build)**" if r["pass"] else "**FAIL — feature rejected; do not build Phase B**"
    body = f"""# Behavioral Embeddings Validation — Results

| metric | value |
| --- | --- |
| Kendall's tau (nomic-embed-text) | {r['tau_nomic']:.4f} |
| Kendall's tau (CodeExecutor) | {r['tau_codeexecutor']:.4f} |
| Delta (CE − nomic) | {r['delta']:.4f} |
| Gate threshold | {r['threshold']:.2f} |
| Pairs scored | {r['n_pairs_kept']} |
| Pairs skipped (missing vector) | {r['n_pairs_skipped']} |

## Verdict

{verdict}

## Reproduce

```sh
cd experiments/behavioral-embeddings
./run.sh
```
"""
    with open(path, "w") as f:
        f.write(body)


if __name__ == "__main__":
    sys.exit(main())
