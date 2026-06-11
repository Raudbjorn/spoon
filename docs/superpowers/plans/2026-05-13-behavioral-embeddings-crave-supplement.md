# Behavioral Embeddings — CRAVE Supplement Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Strengthen the panel re-test verdict by evaluating each panel model on a second, larger, public dataset — `TuringEnterprises/CRAVE`'s 1,174 hand-labeled PR patches — via a binary classification metric (AUC of APPROVE vs REQUEST_CHANGES).

**Architecture:** Reuse the panel models loaded during the main re-test (`embed_hf.py` infrastructure unchanged). Add a small new pipeline: fetch CRAVE → embed each patch with each model → train a linear probe (or compute centroid AUC) per model → emit a side-by-side table. The result lives alongside the panel results: `RESULTS_PANEL.md` cites both numbers per model.

**Tech Stack:** `datasets` (HuggingFace), `scikit-learn` (LogisticRegression for the linear probe), already-installed `numpy`/`scipy`/`pytest`. No torch changes; the existing `embed_hf.py` works directly.

**Scope (single subsystem):** This plan is for the CRAVE evaluation only. Not in scope: changes to the panel itself, changes to the 53-pair Phase B verdict, MULocBench (deferred to a separate plan if needed).

**Pre-requisite:** Phase B panel re-test plan (`docs/superpowers/plans/2026-05-12-behavioral-embeddings-panel.md`) must have produced a `<MODEL>_vectors.json` for each panel member. CRAVE runs as a *supplement*, not a replacement.

---

## When to execute

The decision matrix in the spec gates this:

| panel verdict | run CRAVE? |
| --- | --- |
| ≥ +0.15 (acceptance) | optional; CRAVE adds confidence but doesn't change Phase B authorization |
| +0.05 to +0.15 (gate) | **YES** — second-axis confirmation before writing the Phase B sidecar plan |
| −0.02 to +0.05 (borderline) | **YES** — primary supplement; combined with MULocBench is the tiebreaker |
| < −0.02 (clear fail) | optional; CRAVE provides a stronger rejection but doesn't change the outcome |

---

## File Structure

All new files under `experiments/started/behavioral-embeddings/`:

```
crave_fetch.py                       NEW — download dataset, save flat JSONL
crave_data.jsonl                     NEW — gitignored; 1,174 lines, one patch each
crave_embed.py                       NEW — embed crave_data.jsonl with one model
crave_<model>_vectors.json           NEW — gitignored; per-model output
crave_eval.py                        NEW — linear probe (LogisticRegression) + AUC
crave_eval_test.py                   NEW — TDD on the AUC computation
RESULTS_CRAVE.md                     NEW — committed; the supplement's writeup
```

`RESULTS_PANEL.md` (from the panel plan) gets a "Cross-task validation via CRAVE" section appended at the end.

---

## Task 1: Fetch CRAVE dataset

**Files:**
- Create: `experiments/started/behavioral-embeddings/crave_fetch.py`
- Create (gitignored): `experiments/started/behavioral-embeddings/crave_data.jsonl`

- [ ] **Step 1: Add `datasets` to requirements.txt and install**

Read `experiments/started/behavioral-embeddings/requirements.txt`. Append:

```
datasets==3.2.0
scikit-learn==1.5.2
```

Then:
```bash
cd experiments/started/behavioral-embeddings && uv pip install --python .venv/bin/python -r requirements.txt
```

Expected: `Installed 2 packages` (or "Already up to date" if some were transitive deps).

- [ ] **Step 2: Write crave_fetch.py**

Create `experiments/started/behavioral-embeddings/crave_fetch.py`:

```python
"""Fetch CRAVE (TuringEnterprises/CRAVE) and write a flat JSONL with the
fields needed for downstream embedding + AUC. Idempotent — skips work if
crave_data.jsonl already exists.
"""
import argparse
import json
import os
import sys

from datasets import load_dataset


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="crave_data.jsonl")
    ap.add_argument("--split", default="train+validation+test",
                    help="Splits to concatenate. Default: all three predefined splits.")
    args = ap.parse_args()

    if os.path.exists(args.out):
        with open(args.out) as f:
            n = sum(1 for _ in f)
        print(f"{args.out} already exists with {n} rows; not refetching", file=sys.stderr)
        return 0

    print(f"loading TuringEnterprises/CRAVE [{args.split}]", file=sys.stderr)
    ds = load_dataset("TuringEnterprises/CRAVE", split=args.split)
    print(f"got {len(ds)} rows", file=sys.stderr)

    with open(args.out, "w") as f:
        for i, row in enumerate(ds):
            rec = {
                "id": f"crave-{i}",
                "patch": row["patch"],
                "label": 1 if row["label"] == "APPROVE" else 0,
                "pr_title": row.get("pull_request_title", ""),
                "repo": row.get("repo", ""),
                "pr_number": row.get("pr_number", ""),
            }
            f.write(json.dumps(rec) + "\n")
    print(f"wrote {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
```

- [ ] **Step 3: Run the fetch**

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python crave_fetch.py
```

Expected: stderr prints `got 1174 rows` (or thereabouts, depending on split filter) and `wrote crave_data.jsonl`. Total file ~25 MB.

- [ ] **Step 4: Sanity-check label balance**

```bash
wc -l crave_data.jsonl
jq -s 'group_by(.label) | map({label: .[0].label, count: length})' crave_data.jsonl
```

Expected: ~1174 rows; balanced ~600 with `label=1` (APPROVE) and ~600 with `label=0` (REQUEST_CHANGES). Slight imbalance is fine; the AUC metric handles it.

- [ ] **Step 5: Add crave_data.jsonl to .gitignore**

Read `experiments/started/behavioral-embeddings/.gitignore`. Append:

```
crave_data.jsonl
```

- [ ] **Step 6: Commit**

```bash
git add experiments/started/behavioral-embeddings/requirements.txt experiments/started/behavioral-embeddings/crave_fetch.py experiments/started/behavioral-embeddings/.gitignore
git commit -m "experiments: fetch CRAVE supplement dataset"
```

---

## Task 2: Embed CRAVE patches with each panel model

**Files:**
- Create: `experiments/started/behavioral-embeddings/crave_embed.py`
- Create (gitignored): one `crave_<model>_vectors.json` per panel model

This is parallel work to the existing `embed_hf.py` for the 53-pair set. CRAVE's "patch" field is a single string per row, so the prompt builder is simpler than `build_text` (no per-modality structure). Write a small wrapper rather than retrofit `embed_hf.py`.

- [ ] **Step 1: Write crave_embed.py**

Create `experiments/started/behavioral-embeddings/crave_embed.py`:

```python
"""Embed CRAVE patches with a single HF model, write vectors keyed by crave id.

Mirrors embed_hf.py's resume + flush behavior. Input is crave_data.jsonl
(one JSON object per line); output is a JSON dict {crave-id: [floats]}.
"""
import argparse
import json
import os
import sys

import torch
from transformers import AutoModel, AutoTokenizer

# Reuse the helpers in embed_hf.py
from embed_hf import _effective_max_length


def embed_batch(model, tokenizer, texts, device):
    enc = tokenizer(
        texts, padding=True, truncation=True,
        max_length=_effective_max_length(tokenizer),
        return_tensors="pt",
    ).to(device)
    with torch.no_grad():
        out = model(**enc)
    hidden = out.last_hidden_state
    mask = enc["attention_mask"].unsqueeze(-1).to(hidden.dtype)
    summed = (hidden * mask).sum(dim=1)
    counts = mask.sum(dim=1).clamp(min=1)
    return (summed / counts).cpu().tolist()


def load_crave(path):
    rows = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if line:
                rows.append(json.loads(line))
    return rows


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--input", default="crave_data.jsonl")
    ap.add_argument("--out", required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("--cache-dir", default="hf_cache")
    ap.add_argument("--batch-size", type=int, default=16)
    args = ap.parse_args()

    os.makedirs(args.cache_dir, exist_ok=True)
    device = "cuda" if torch.cuda.is_available() else "cpu"
    print(f"loading {args.model} on {device}", file=sys.stderr)
    tokenizer = AutoTokenizer.from_pretrained(args.model, cache_dir=args.cache_dir, trust_remote_code=True)
    model = AutoModel.from_pretrained(args.model, cache_dir=args.cache_dir, trust_remote_code=True).to(device)
    model.eval()

    rows = load_crave(args.input)
    ids = [r["id"] for r in rows]
    texts_by_id = {r["id"]: r["patch"] for r in rows}

    out = {}
    if os.path.exists(args.out):
        try:
            with open(args.out) as f:
                out = json.load(f)
            print(f"resuming: {len(out)} vectors already in {args.out}", file=sys.stderr)
        except Exception as e:
            print(f"could not resume: {e}", file=sys.stderr)
            out = {}

    todo = [i for i in ids if i not in out]
    failed = 0
    for i in range(0, len(todo), args.batch_size):
        batch_ids = todo[i : i + args.batch_size]
        batch_texts = [texts_by_id[bid] for bid in batch_ids]
        try:
            vecs = embed_batch(model, tokenizer, batch_texts, device)
        except Exception as e:
            failed += len(batch_ids)
            print(f"  batch {i // args.batch_size}: skip ({len(batch_ids)} ids): {e}", file=sys.stderr)
            with open(args.out, "w") as f:
                json.dump(out, f)
            continue
        for bid, v in zip(batch_ids, vecs):
            out[bid] = v
        if (i // args.batch_size) % 4 == 3:
            with open(args.out, "w") as f:
                json.dump(out, f)
        print(f"[{i + len(batch_ids)}/{len(todo)} new] embedded (total {len(out)}/{len(ids)})", file=sys.stderr)

    with open(args.out, "w") as f:
        json.dump(out, f)
    print(f"wrote {len(out)} vectors to {args.out} (failed batches: {failed})", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
```

- [ ] **Step 2: Run for each panel model**

Use the same model list as the main panel plus the baseline:

```bash
cd experiments/started/behavioral-embeddings
for entry in \
  "nomic-ai/nomic-embed-text-v1:crave_nomic_baseline_vectors.json" \
  "nomic-ai/CodeRankEmbed:crave_code_rank_embed_vectors.json" \
  "jinaai/jina-embeddings-v2-base-code:crave_jina_v2_code_vectors.json" \
  "Salesforce/SFR-Embedding-Code-400M_R:crave_sfr_code_400m_vectors.json" \
  "Snowflake/snowflake-arctic-embed-l-v2.0:crave_arctic_l_v2_vectors.json" ; do
  IFS=":" read -r M O <<< "$entry"
  echo "=== $M ==="
  .venv/bin/python crave_embed.py --model "$M" --out "$O" --batch-size 16 2>"${O%.json}_run.log"
  echo "  -> $(jq length "$O") vectors"
done
```

Each model takes ~5–15 min for the 1,174 patches on vinbonesjr's CPU. Total ~60 min wall-clock.

- [ ] **Step 3: Verify coverage**

```bash
for f in crave_*_vectors.json ; do echo -n "$f: " ; jq length "$f"; done
```

Expected: each prints `1174` (or thereabouts, allowing a few skipped batches).

- [ ] **Step 4: Add the crave_*_vectors.json files to .gitignore**

Read `.gitignore`. Append:
```
crave_*_vectors.json
crave_*_run.log
```

- [ ] **Step 5: Commit**

```bash
git add experiments/started/behavioral-embeddings/crave_embed.py experiments/started/behavioral-embeddings/.gitignore
git commit -m "experiments: embed CRAVE patches across panel models"
```

---

## Task 3: Linear probe + AUC analysis (TDD)

**Files:**
- Create: `experiments/started/behavioral-embeddings/crave_eval.py`
- Create: `experiments/started/behavioral-embeddings/crave_eval_test.py`

A logistic-regression linear probe on embeddings is the standard frozen-features evaluation. Train on 80% of CRAVE, evaluate AUC on 20%. The metric is AUC of binary classification (APPROVE vs REQUEST_CHANGES) — directly comparable to the Perera et al. paper our spec cited.

- [ ] **Step 1: Write the failing test**

Create `experiments/started/behavioral-embeddings/crave_eval_test.py`:

```python
"""Unit tests for the linear-probe AUC computation."""
import math
import pytest

from crave_eval import linear_probe_auc, load_vectors_and_labels


def test_linear_probe_auc_perfect_separation():
    # Vectors are 2-D points on a line, separated by label. AUC should be 1.0.
    vectors = {
        "a": [1.0, 0.0], "b": [2.0, 0.0], "c": [3.0, 0.0],   # label=1 cluster
        "d": [-1.0, 0.0], "e": [-2.0, 0.0], "f": [-3.0, 0.0], # label=0 cluster
    }
    labels = {"a": 1, "b": 1, "c": 1, "d": 0, "e": 0, "f": 0}
    auc = linear_probe_auc(vectors, labels, seed=42)
    assert auc == pytest.approx(1.0, abs=0.01)


def test_linear_probe_auc_random_is_about_half():
    # 100 random 4-D vectors with random binary labels — AUC should be ~0.5.
    import random
    random.seed(42)
    vectors = {f"r{i}": [random.gauss(0, 1) for _ in range(4)] for i in range(100)}
    labels = {fid: random.choice([0, 1]) for fid in vectors}
    auc = linear_probe_auc(vectors, labels, seed=42)
    # Random AUC has noise; accept anything in [0.35, 0.65].
    assert 0.35 < auc < 0.65, f"random AUC = {auc}"


def test_load_vectors_and_labels_join(tmp_path):
    vecs = tmp_path / "vecs.json"
    vecs.write_text('{"a":[1.0,2.0],"b":[3.0,4.0]}')
    data = tmp_path / "crave.jsonl"
    data.write_text('{"id":"a","patch":"x","label":1}\n{"id":"b","patch":"y","label":0}\n')
    v, l = load_vectors_and_labels(str(vecs), str(data))
    assert v == {"a": [1.0, 2.0], "b": [3.0, 4.0]}
    assert l == {"a": 1, "b": 0}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python -m pytest crave_eval_test.py -v
```

Expected: FAIL — `ModuleNotFoundError: No module named 'crave_eval'`.

- [ ] **Step 3: Implement crave_eval.py**

Create `experiments/started/behavioral-embeddings/crave_eval.py`:

```python
"""Linear-probe AUC on CRAVE embeddings.

Loads a per-model vectors JSON (output of crave_embed.py) and the CRAVE
labels (from crave_data.jsonl), trains an L2-regularized logistic
regression on 80% of the data, evaluates AUC on the held-out 20%, and
returns the AUC score. Output mirrors analyze.py's style.
"""
import argparse
import json
import sys

import numpy as np
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import roc_auc_score
from sklearn.model_selection import train_test_split


def load_vectors_and_labels(vectors_path: str, crave_data_path: str):
    with open(vectors_path) as f:
        vectors = json.load(f)
    labels = {}
    with open(crave_data_path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            r = json.loads(line)
            labels[r["id"]] = int(r["label"])
    return vectors, labels


def linear_probe_auc(vectors: dict, labels: dict, seed: int = 42) -> float:
    """Train logistic regression on 80% of (vector, label) pairs; return AUC
    on the 20% held-out set."""
    ids = sorted(set(vectors.keys()) & set(labels.keys()))
    X = np.array([vectors[i] for i in ids])
    y = np.array([labels[i] for i in ids])
    X_train, X_test, y_train, y_test = train_test_split(
        X, y, test_size=0.2, random_state=seed, stratify=y
    )
    clf = LogisticRegression(max_iter=2000, C=1.0, random_state=seed)
    clf.fit(X_train, y_train)
    probs = clf.predict_proba(X_test)[:, 1]
    return float(roc_auc_score(y_test, probs))


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--vectors", required=True)
    ap.add_argument("--crave-data", default="crave_data.jsonl")
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--out", default="-")
    args = ap.parse_args()

    vectors, labels = load_vectors_and_labels(args.vectors, args.crave_data)
    auc = linear_probe_auc(vectors, labels, seed=args.seed)
    result = {
        "vectors_file": args.vectors,
        "n_paired": len(set(vectors) & set(labels)),
        "auc": auc,
    }
    line = json.dumps(result, indent=2)
    if args.out == "-":
        print(line)
    else:
        with open(args.out, "w") as f:
            f.write(line + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python -m pytest crave_eval_test.py -v
```

Expected: 3 passed.

- [ ] **Step 5: Commit**

```bash
git add experiments/started/behavioral-embeddings/crave_eval.py experiments/started/behavioral-embeddings/crave_eval_test.py
git commit -m "experiments: linear-probe AUC analysis for CRAVE supplement"
```

---

## Task 4: Run AUC for each model + write RESULTS_CRAVE.md

**Files:**
- Create: `experiments/started/behavioral-embeddings/RESULTS_CRAVE.md`

- [ ] **Step 1: Compute AUC for each candidate**

```bash
cd experiments/started/behavioral-embeddings
for f in crave_*_vectors.json ; do
  echo "=== $f ==="
  .venv/bin/python crave_eval.py --vectors "$f"
done
```

Each prints a JSON with `{"vectors_file": ..., "n_paired": ..., "auc": ...}`. Save the 6 AUCs.

- [ ] **Step 2: Write RESULTS_CRAVE.md**

Create `experiments/started/behavioral-embeddings/RESULTS_CRAVE.md`:

```markdown
# Behavioral Embeddings — CRAVE Supplement Results

**Date:** <FILL>
**Plan:** `docs/superpowers/plans/2026-05-13-behavioral-embeddings-crave-supplement.md`
**Parent results:** `experiments/started/behavioral-embeddings/RESULTS_PANEL.md`

## TL;DR

<FILL: 2-sentence summary. Does the CRAVE binary-classification AUC ranking match the panel's Kendall's tau ranking? If yes, the verdict is reinforced. If no, the inconsistency is itself informative.>

## AUC per model on CRAVE (APPROVE vs REQUEST_CHANGES, 1,174 patches)

Linear probe: L2-regularized logistic regression on 80% of the embeddings → AUC on 20% held-out (seed=42).

| model | params | CRAVE AUC | panel τ (Δ vs nomic) | consistent? |
| --- | --- | --- | --- | --- |
| nomic-embed-text-v1 (baseline) | 137M | <FILL> | — | — |
| nomic-ai/CodeRankEmbed | 137M | <FILL> | <FILL> | <FILL> |
| jinaai/jina-embeddings-v2-base-code | 161M | <FILL> | <FILL> | <FILL> |
| Salesforce/SFR-Embedding-Code-400M_R | 400M | <FILL> | <FILL> | <FILL> |
| Snowflake/snowflake-arctic-embed-l-v2.0 | 568M | <FILL> | <FILL> | <FILL> |
| jazzcort/nomic-embed-code-Q6_K | 7B Q6_K | <FILL> | <FILL> | <FILL> |

Reference point: Perera et al. (the paper the original spec cited) reported AUCs in the 0.96–0.99 range for the smart-contract vulnerability task with similar embedders. AUCs in the 0.50–0.75 range here would indicate substantially harder fitness for PR-verdict prediction than for vulnerability detection.

## Interpretation

<FILL: 2-3 paragraphs. What does the CRAVE result add to the panel verdict? Strengthen, weaken, or qualify it? Which models perform consistently across both tasks? Which models show task-specific behavior (good on one, bad on the other)?>

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
.venv/bin/python crave_fetch.py
for entry in \
  "nomic-ai/nomic-embed-text-v1:crave_nomic_baseline_vectors.json" \
  "nomic-ai/CodeRankEmbed:crave_code_rank_embed_vectors.json" \
  "jinaai/jina-embeddings-v2-base-code:crave_jina_v2_code_vectors.json" \
  "Salesforce/SFR-Embedding-Code-400M_R:crave_sfr_code_400m_vectors.json" \
  "Snowflake/snowflake-arctic-embed-l-v2.0:crave_arctic_l_v2_vectors.json" ; do
  IFS=":" read -r M O <<< "$entry"
  .venv/bin/python crave_embed.py --model "$M" --out "$O"
  .venv/bin/python crave_eval.py --vectors "$O"
done
```

CRAVE downloads are reproducible via the standard `datasets` HF cache; AUC scoring is deterministic at seed=42.
```

Replace `<FILL>` markers with the actual numbers from Step 1's outputs.

- [ ] **Step 3: Update RESULTS_PANEL.md with a cross-task section**

Read `experiments/started/behavioral-embeddings/RESULTS_PANEL.md`. Append at the end (after "Future supplements"):

```markdown

## Cross-task validation via CRAVE

Each panel model was also evaluated on the `TuringEnterprises/CRAVE` dataset (1,174 PR patches with binary APPROVE/REQUEST_CHANGES labels) as a second-axis test. Linear-probe AUC results in `RESULTS_CRAVE.md`. Summary:

<FILL: 1-paragraph cross-reference between the panel τ ranking and the CRAVE AUC ranking. Note which models perform consistently across both, which only on one, and how that informs the Phase B decision.>
```

- [ ] **Step 4: Commit**

```bash
git add experiments/started/behavioral-embeddings/RESULTS_CRAVE.md experiments/started/behavioral-embeddings/RESULTS_PANEL.md
git commit -m "experiments: CRAVE supplement results + cross-task reference in RESULTS_PANEL.md"
```

---

## Self-Review

After writing this plan, checked against the supplement scope:

**1. Coverage:**
- Fetch CRAVE → Task 1
- Embed each model → Task 2
- Compute AUC → Task 3 (TDD)
- Write up → Task 4

**2. Placeholders:** No `TBD` / `implement later`. `<FILL>` markers in the RESULTS_CRAVE.md template are explicit fill-in-the-blanks for measured numbers.

**3. Type consistency:** `linear_probe_auc(vectors, labels, seed)` signature is consistent between Task 3's test and impl. Vector filenames in Task 2 (`crave_<model>_vectors.json`) match those gitignored in Task 2 Step 4 and consumed in Task 4 Step 1.

**4. Dependency clarity:** Task 2 assumes panel models are available (downloaded from the main panel plan). If running CRAVE standalone, hf_cache will repopulate.

---

## Execution Handoff

Plan complete. Two execution options:

1. **Subagent-Driven** — fresh subagent per task, two-stage review.
2. **Inline Execution** — execute tasks in this session with checkpoints.

Trigger this plan **only after** `docs/superpowers/plans/2026-05-12-behavioral-embeddings-panel.md` produces its verdict and RESULTS_PANEL.md cites a decision-matrix row that requires the CRAVE supplement (gate-cleared-but-not-acceptance, or borderline).
