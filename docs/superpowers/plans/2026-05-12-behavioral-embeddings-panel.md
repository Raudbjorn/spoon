# Behavioral Embeddings — 4-Model Panel Re-Test Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run 4 modern long-context embedders through the existing Phase A harness and decide whether any clears the +0.05 Kendall's tau gate that CodeBERTa and CodeExecutor missed.

**Architecture:** Reuse the existing `experiments/started/behavioral-embeddings/` harness. Three small code changes lift the artificial 5 KB cap that was a Phase A workaround for 512-token-window models. Re-run the nomic baseline against the new full-context inputs. Run 4 panel models. Compare via `analyze.py` and emit `RESULTS_PANEL.md`. The decision-matrix outcome (write Phase B plan OR rejection addendum) is the final task.

**Tech Stack:** Python 3.14 in the existing `.venv` (torch, transformers, scipy, requests, pytest). HuggingFace transformers for the 4 panel models. Ollama for the nomic baseline. No new dependencies.

**Scope (single subsystem):** This plan is for the experiment and its decision-matrix outcome. The Phase B sidecar plan (if triggered) is a follow-up plan written as part of Task 10's conditional branch.

---

## File Structure

```
experiments/started/behavioral-embeddings/
  embed_hf.py                    (RENAMED from embed_codeexecutor.py) - dynamic max_length
  embed_nomic.py                 (MODIFY) - num_ctx=8192
  embed_common.py                (MODIFY) - drop *_MAX_CHARS, drop _truncate
  embed_common_test.py           (MODIFY) - drop truncation test
  embed_hf_test.py               (NEW) - test _effective_max_length helper
  run.sh                         (MODIFY) - orchestrate 4 panel runs
  .gitignore                     (MODIFY) - 4 new vector files
  RESULTS_PANEL.md               (NEW) - panel table + verdict
  nomic_vectors.json             (REGENERATE) - new num_ctx baseline
  nomic_embed_code_vectors.json  (NEW, gitignored)
  jina_v2_code_vectors.json      (NEW, gitignored)
  sfr_code_400m_vectors.json     (NEW, gitignored)
  arctic_l_v2_vectors.json       (NEW, gitignored)
```

Conditional follow-up files (Task 10):
- `docs/superpowers/plans/2026-05-12-behavioral-embeddings-sidecar.md` — Phase B plan if a model passes.
- `experiments/started/behavioral-embeddings/RESULTS.md` — addendum appended if all models fail (the Phase A result stays intact above the addendum).
- `docs/superpowers/specs/future/future-work-behavioral-embeddings.md` — spec status note updated either way.

---

## Task 1: Verify the 4 HF model names

**Files:**
- No code changes. Side effect: a recorded list of canonical model identifiers (and substitutions, if any) for use in subsequent tasks.

HF model identifiers drift sometimes. Probe each before kicking off long downloads. The four target models from the spec are listed below. If any name has shifted, pick the closest current name and record the substitution.

- [ ] **Step 1: Probe each model URL via the HF public API**

Run:
```bash
for m in \
  nomic-ai/nomic-embed-code \
  jinaai/jina-embeddings-v2-base-code \
  Salesforce/SFR-Embedding-Code-400M_R \
  Snowflake/snowflake-arctic-embed-l-v2.0 ; do
  echo -n "$m: "
  curl -s -o /dev/null -w "%{http_code}\n" "https://huggingface.co/api/models/$m"
done
```

Expected: all four return `200`. Note any `404`s.

If a `404` occurs:
- For `nomic-ai/nomic-embed-code`: try `nomic-ai/nomic-embed-code-v1`.
- For `jinaai/jina-embeddings-v2-base-code`: try `jina/jina-embeddings-v2-base-code` (older namespace).
- For `Salesforce/SFR-Embedding-Code-400M_R`: try `salesforce-sfr/SFR-Embedding-Code-400M-R` (occasional separator variant).
- For `Snowflake/snowflake-arctic-embed-l-v2.0`: try `Snowflake/snowflake-arctic-embed-v2.0`.

- [ ] **Step 2: Record the four verified names**

If any model required a substitution, note the canonical name to use for subsequent steps. The names in subsequent tasks will need to be replaced with the verified names.

**No commit for this task** — it's pure verification. The downstream tasks carry the names forward.

---

## Task 2: Rename embed_codeexecutor.py → embed_hf.py

The file in Phase A was named after the specific model. With four candidate models in Phase B's panel, the generic name is clearer. This is a pure rename + reference-update.

**Files:**
- Rename: `experiments/started/behavioral-embeddings/embed_codeexecutor.py` → `experiments/started/behavioral-embeddings/embed_hf.py`
- Modify: `experiments/started/behavioral-embeddings/run.sh:22` (references the old filename)

- [ ] **Step 1: Rename the script**

Run:
```bash
cd experiments/started/behavioral-embeddings
git mv embed_codeexecutor.py embed_hf.py
```

- [ ] **Step 2: Update run.sh to reference the new name**

Read `experiments/started/behavioral-embeddings/run.sh`. Find the line that invokes the script (currently `.venv/bin/python embed_codeexecutor.py …`). Replace `embed_codeexecutor.py` with `embed_hf.py`. No other changes.

- [ ] **Step 3: Verify build/import smoke**

Run:
```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python -c "import embed_hf; print('ok')"
```

Expected: `ok`. If `ModuleNotFoundError` occurs, check that the file was actually renamed (not duplicated).

- [ ] **Step 4: Commit**

```bash
git add experiments/started/behavioral-embeddings/embed_hf.py experiments/started/behavioral-embeddings/run.sh
git commit -m "experiments: rename embed_codeexecutor.py to embed_hf.py for panel re-test"
```

---

## Task 3: Make tokenizer max_length dynamic (TDD)

The Phase A code hardcoded `max_length=512` to fit CodeBERT-family models. Panel models support 7K–32K context, so this needs to be dynamic. Extract the logic into a helper function so it can be unit-tested without loading a real tokenizer.

**Files:**
- Modify: `experiments/started/behavioral-embeddings/embed_hf.py` (the file we just renamed)
- Create: `experiments/started/behavioral-embeddings/embed_hf_test.py`

- [ ] **Step 1: Write the failing test**

Create `experiments/started/behavioral-embeddings/embed_hf_test.py`:

```python
"""Unit tests for the dynamic max_length helper in embed_hf."""
from embed_hf import _effective_max_length


class _FakeTokenizer:
    def __init__(self, max_len):
        self.model_max_length = max_len


class _NoAttrTokenizer:
    pass


def test_effective_max_length_uses_tokenizer_value():
    tok = _FakeTokenizer(8192)
    assert _effective_max_length(tok) == 8192


def test_effective_max_length_caps_at_32k():
    # Some HF tokenizers report a sentinel like 1e9 when no real limit is set.
    # We cap at 32K to avoid allocating absurd-length tensors.
    tok = _FakeTokenizer(1_000_000_000)
    assert _effective_max_length(tok) == 32768


def test_effective_max_length_falls_back_to_512_when_missing():
    tok = _NoAttrTokenizer()
    assert _effective_max_length(tok) == 512


def test_effective_max_length_respects_low_native_window():
    # CodeBERT family (model_max_length=512) must still get 512, not the cap.
    tok = _FakeTokenizer(512)
    assert _effective_max_length(tok) == 512
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python -m pytest embed_hf_test.py -v
```

Expected: FAIL with `ImportError: cannot import name '_effective_max_length' from 'embed_hf'`.

- [ ] **Step 3: Add the helper and wire it in**

Read `experiments/started/behavioral-embeddings/embed_hf.py`. Find the `embed_batch` function and the tokenizer call inside it (currently has `max_length=512` hardcoded).

Add this helper near the top of the file, after imports:

```python
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
```

Then modify the tokenizer call in `embed_batch` so it uses the helper. Replace:

```python
    enc = tokenizer(
        texts,
        padding=True,
        truncation=True,
        max_length=512,
        return_tensors="pt",
    ).to(device)
```

with:

```python
    enc = tokenizer(
        texts,
        padding=True,
        truncation=True,
        max_length=_effective_max_length(tokenizer),
        return_tensors="pt",
    ).to(device)
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python -m pytest embed_hf_test.py -v
```

Expected: 4 passed.

- [ ] **Step 5: Run the existing embed_common tests to confirm no regression**

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python -m pytest embed_common_test.py analyze_test.py -v
```

Expected: 14 passed (or however many existed prior — they should be unchanged).

- [ ] **Step 6: Commit**

```bash
git add experiments/started/behavioral-embeddings/embed_hf.py experiments/started/behavioral-embeddings/embed_hf_test.py
git commit -m "experiments: dynamic tokenizer max_length for panel models"
```

---

## Task 4: Add num_ctx=8192 to embed_nomic.py

Ollama defaults the context window for `nomic-embed-text` to 2048 tokens, which is why Phase A's first run produced 34 HTTP 500 errors on oversize prompts. nomic-embed-text actually supports 8192. Set it explicitly.

**Files:**
- Modify: `experiments/started/behavioral-embeddings/embed_nomic.py` (the `embed_one` function)

- [ ] **Step 1: Read the current embed_one body**

Read `experiments/started/behavioral-embeddings/embed_nomic.py`. The relevant function looks like:

```python
def embed_one(endpoint: str, model: str, text: str) -> list[float]:
    r = requests.post(
        f"{endpoint.rstrip('/')}/api/embeddings",
        json={"model": model, "prompt": text},
        timeout=60,
    )
    r.raise_for_status()
    …
```

- [ ] **Step 2: Add the num_ctx option to the API payload**

Replace the `json={"model": model, "prompt": text}` argument with:

```python
        json={
            "model": model,
            "prompt": text,
            "options": {"num_ctx": 8192},
        },
```

(Add a comment above the request explaining why this matters, since it's a non-obvious setting:)

```python
    # num_ctx=8192 matches nomic-embed-text's native window. Without this,
    # Ollama uses its default 2048 and returns HTTP 500 on prompts that
    # exceed it (Phase A's first run dropped 34/200 records to this).
```

- [ ] **Step 3: Quick smoke against a tiny prompt**

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python -c "
from embed_nomic import embed_one
v = embed_one('http://localhost:11434', 'nomic-embed-text', 'def hello(): pass')
assert len(v) == 768, f'unexpected dim: {len(v)}'
print('ok, dim=', len(v))
"
```

Expected: `ok, dim= 768`. Requires Ollama running and `nomic-embed-text` already pulled (Phase A pulled it; check with `ollama list`).

- [ ] **Step 4: Commit**

```bash
git add experiments/started/behavioral-embeddings/embed_nomic.py
git commit -m "experiments: set Ollama num_ctx=8192 for nomic baseline"
```

---

## Task 5: Remove truncation caps in embed_common.py

The 5 KB per-modality cap was a Phase A workaround for 512-token windows and Ollama's 2048 default. Both are addressed by Tasks 3 and 4. Removing the caps lets each model see the full feature content (subject to its own tokenizer truncation).

**Files:**
- Modify: `experiments/started/behavioral-embeddings/embed_common.py`
- Modify: `experiments/started/behavioral-embeddings/embed_common_test.py`

- [ ] **Step 1: Rewrite embed_common.py**

Replace the entire contents of `experiments/started/behavioral-embeddings/embed_common.py` with:

```python
"""Shared helpers for the panel embedding scripts.

Phase A used per-modality char caps (PATHS_MAX_CHARS, etc.) totaling ~5 KB
to work around 512-token-window embedders (CodeBERT family) and Ollama's
default 2048 num_ctx for nomic. The Phase B panel uses long-context models
(7K-32K) and Ollama with num_ctx=8192, so the artificial caps would
handicap candidates and baseline alike. Concatenate the full payload;
let each tokenizer's natural truncation apply.
"""
import json
from typing import Any


def build_text(features: dict[str, str]) -> str:
    """Concatenate the four ForkFeatures modalities with structural tags.

    Mirrors spoon's codeAwareEmbed path (internal/embed/multimodal.go:53).
    No truncation here — each downstream tokenizer truncates at its own
    model_max_length.
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
```

- [ ] **Step 2: Rewrite embed_common_test.py**

Replace the entire contents of `experiments/started/behavioral-embeddings/embed_common_test.py` with:

```python
from embed_common import build_text, load_features


def test_build_text_concatenates_with_tags():
    feats = {
        "Paths": "main.go\nREADME.md",
        "Commits": "fix nil deref",
        "ReadmeDoc": "spoon finds forks",
        "DiffChunk": "@@@@",
    }
    got = build_text(feats)
    assert "<paths>main.go\nREADME.md</paths>" in got
    assert "<commits>fix nil deref</commits>" in got
    assert "<readme>spoon finds forks</readme>" in got
    assert "<diff>@@@@</diff>" in got


def test_build_text_empty_blocks_emit_open_close_tags():
    feats = {"Paths": "", "Commits": "", "ReadmeDoc": "", "DiffChunk": ""}
    got = build_text(feats)
    assert got == "<paths></paths><commits></commits><readme></readme><diff></diff>"


def test_build_text_preserves_full_oversize_payload():
    # Phase B removed the per-modality cap. Confirm the helper passes long
    # text through unchanged so downstream tokenizers can do their own
    # truncation against their native max_length.
    feats = {
        "Paths": "p" * 10_000,
        "Commits": "",
        "ReadmeDoc": "",
        "DiffChunk": "d" * 10_000,
    }
    got = build_text(feats)
    paths_block = got.split("</paths>", 1)[0].removeprefix("<paths>")
    diff_block = got.split("<diff>", 1)[1].removesuffix("</diff>")
    assert len(paths_block) == 10_000
    assert len(diff_block) == 10_000


def test_load_features_returns_dict_by_id(tmp_path):
    p = tmp_path / "features.json"
    p.write_text(
        '[{"id":"a","owner":"o","name":"n","url":"u","stars":0,'
        '"features":{"Paths":"p","Commits":"c","ReadmeDoc":"r","DiffChunk":"d"}}]'
    )
    got = load_features(str(p))
    assert "a" in got
    assert got["a"]["features"]["Paths"] == "p"
```

- [ ] **Step 3: Run tests to verify they pass**

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python -m pytest embed_common_test.py analyze_test.py embed_hf_test.py -v
```

Expected: all pass (15+ tests). The new `test_build_text_preserves_full_oversize_payload` should pass since the function now does no truncation.

- [ ] **Step 4: Commit**

```bash
git add experiments/started/behavioral-embeddings/embed_common.py experiments/started/behavioral-embeddings/embed_common_test.py
git commit -m "experiments: remove per-modality truncation caps for long-context panel"
```

---

## Task 6: Update .gitignore for the 4 panel vector files

The 4 candidate vector files are regenerable (each ~3 MB; collectively ~12 MB). Phase A gitignored `nomic_vectors.json` and `codeexecutor_vectors.json` for the same reason — extend the pattern.

**Files:**
- Modify: `experiments/started/behavioral-embeddings/.gitignore`

- [ ] **Step 1: Append the 4 panel vector files to .gitignore**

Read `experiments/started/behavioral-embeddings/.gitignore`. Append to the end:

```
nomic_embed_code_vectors.json
jina_v2_code_vectors.json
sfr_code_400m_vectors.json
arctic_l_v2_vectors.json
```

- [ ] **Step 2: Commit**

```bash
git add experiments/started/behavioral-embeddings/.gitignore
git commit -m "experiments: gitignore panel candidate vector files"
```

---

## Task 7: Re-run the nomic baseline with new num_ctx

The new baseline replaces Phase A's `nomic_vectors.json`. With `num_ctx=8192` and no artificial cap, expect all 200/200 records to embed successfully (vs Phase A's first run which dropped 34).

**Files:**
- Regenerate: `experiments/started/behavioral-embeddings/nomic_vectors.json` (gitignored)

- [ ] **Step 1: Confirm Ollama is running and the model is pulled**

```bash
curl -s http://localhost:11434/api/tags | jq '.models[].name'
```

Expected: list includes `"nomic-embed-text:latest"`. If not, run `ollama pull nomic-embed-text` first.

- [ ] **Step 2: Wipe the old baseline and re-run**

```bash
cd experiments/started/behavioral-embeddings && rm -f nomic_vectors.json
.venv/bin/python embed_nomic.py --features features.json --out nomic_vectors.json 2>nomic_run.log
```

Expected wall-clock: ~3–5 minutes (200 sequential Ollama calls). Watch for HTTP 500s in `nomic_run.log`.

- [ ] **Step 3: Verify full coverage**

```bash
jq 'length' nomic_vectors.json && grep -c "skip pr-" nomic_run.log
```

Expected: `200` (vector count) and `0` (skip count). If skip count > 0, inspect `nomic_run.log`: any remaining errors mean the prompts are exceeding even 8192 tokens. If 1–5 records fail, log the IDs in RESULTS_PANEL.md and continue. If >5, stop and escalate (a structural issue with the truncation removal).

- [ ] **Step 4: Add nomic_run.log to .gitignore**

Read `experiments/started/behavioral-embeddings/.gitignore`. Append:

```
nomic_run.log
```

- [ ] **Step 5: Commit (the .gitignore change only — the vector file is gitignored)**

```bash
git add experiments/started/behavioral-embeddings/.gitignore
git commit -m "experiments: gitignore nomic_run.log from baseline rerun"
```

---

## Task 8: Run the 4 panel models

Each panel model gets a separate `embed_hf.py` invocation. The script's resume support (added in Phase A) handles interruptions; restart skips already-embedded records.

**Files:**
- Generate (gitignored): `nomic_embed_code_vectors.json`, `jina_v2_code_vectors.json`, `sfr_code_400m_vectors.json`, `arctic_l_v2_vectors.json`

Use the model names verified in Task 1. The commands below use the spec's nominal names; substitute if Task 1 found drift.

- [ ] **Step 1: Run nomic-ai/nomic-embed-code (~137M, 7K context, ~1.5 GB RAM)**

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python embed_hf.py \
  --model nomic-ai/nomic-embed-code \
  --features features.json \
  --out nomic_embed_code_vectors.json \
  --cache-dir hf_cache 2>nomic_embed_code_run.log
```

Expected wall-clock: ~10–15 min (first run includes model download ~300 MB + CPU embed). Verify completion:

```bash
jq 'length' nomic_embed_code_vectors.json
```

Expected: `200`. If less, check `nomic_embed_code_run.log` for batch failures.

- [ ] **Step 2: Run jinaai/jina-embeddings-v2-base-code (~161M, 8K context, ~1.5 GB RAM)**

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python embed_hf.py \
  --model jinaai/jina-embeddings-v2-base-code \
  --features features.json \
  --out jina_v2_code_vectors.json \
  --cache-dir hf_cache 2>jina_v2_code_run.log
```

Note: Jina requires `trust_remote_code=True` for some versions. If the model load errors with "trust_remote_code required", edit `embed_hf.py` and add `trust_remote_code=True` to both `AutoTokenizer.from_pretrained` and `AutoModel.from_pretrained` calls. Then commit that change as a follow-up.

Verify: `jq 'length' jina_v2_code_vectors.json` returns `200`.

- [ ] **Step 3: Run Salesforce/SFR-Embedding-Code-400M_R (~400M, 32K context, ~3 GB RAM)**

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python embed_hf.py \
  --model Salesforce/SFR-Embedding-Code-400M_R \
  --features features.json \
  --out sfr_code_400m_vectors.json \
  --cache-dir hf_cache 2>sfr_code_400m_run.log
```

Expected wall-clock: ~20–30 min (model is the heaviest; some prompts may use 5-10K tokens). Watch for OOM. If OOM occurs:
- Lower `--batch-size` from default 32 to 8 or 4.

Verify: `jq 'length' sfr_code_400m_vectors.json` returns `200`.

- [ ] **Step 4: Run Snowflake/snowflake-arctic-embed-l-v2.0 (~568M, 8K context, ~2 GB RAM)**

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python embed_hf.py \
  --model Snowflake/snowflake-arctic-embed-l-v2.0 \
  --features features.json \
  --out arctic_l_v2_vectors.json \
  --cache-dir hf_cache 2>arctic_l_v2_run.log
```

Expected wall-clock: ~15–20 min.

Verify: `jq 'length' arctic_l_v2_vectors.json` returns `200`.

- [ ] **Step 5: Add the 4 run logs to .gitignore**

Read `experiments/started/behavioral-embeddings/.gitignore` and append:

```
nomic_embed_code_run.log
jina_v2_code_run.log
sfr_code_400m_run.log
arctic_l_v2_run.log
```

- [ ] **Step 6: Commit the .gitignore changes**

```bash
git add experiments/started/behavioral-embeddings/.gitignore
git commit -m "experiments: gitignore panel run logs"
```

---

## Task 9: Run analyze.py 4× and write RESULTS_PANEL.md

The existing `analyze.py` takes `--codeexecutor X.json` (single comparison). Call it once per candidate; collect the four deltas; write the panel table.

**Files:**
- Create: `experiments/started/behavioral-embeddings/RESULTS_PANEL.md`

- [ ] **Step 1: Run analyze.py for each candidate**

```bash
cd experiments/started/behavioral-embeddings
for v in nomic_embed_code_vectors jina_v2_code_vectors sfr_code_400m_vectors arctic_l_v2_vectors ; do
  echo "=== $v ==="
  .venv/bin/python analyze.py \
    --nomic nomic_vectors.json \
    --codeexecutor "$v.json" \
    --out "${v}_analyze.md"
done
```

Expected output per run: a JSON payload like `{"tau_nomic": 0.X, "tau_codeexecutor": 0.Y, "delta": 0.Z, "pass": ..., "n_pairs_kept": 53, ...}`. Save all 4 outputs.

- [ ] **Step 2: Write RESULTS_PANEL.md**

Create `experiments/started/behavioral-embeddings/RESULTS_PANEL.md` with the panel table. Use the four `*_analyze.md` outputs you just generated to fill in the τ_panel and Δ columns.

Template (replace `<FILL>` with actual numbers from Step 1's outputs):

```markdown
# Behavioral Embeddings Validation — Panel Re-Test Results

**Date:** 2026-05-12
**Source:** `docs/superpowers/specs/2026-05-12-behavioral-embeddings-panel-design.md`
**Phase A reference:** `experiments/started/behavioral-embeddings/RESULTS.md`

## Panel results

All four models embedded all 200 PRs (n_pairs_kept = 53 of 53 in each case).

| model | params | context | τ panel | Δ vs nomic | gate |
| --- | --- | --- | --- | --- | --- |
| nomic-embed-text (baseline) | — | 8K (Ollama) | <FILL> | — | — |
| nomic-ai/nomic-embed-code | ~137M | 7K | <FILL> | <FILL> | <PASS/FAIL> |
| jinaai/jina-embeddings-v2-base-code | ~161M | 8K | <FILL> | <FILL> | <PASS/FAIL> |
| Salesforce/SFR-Embedding-Code-400M_R | ~400M | 32K | <FILL> | <FILL> | <PASS/FAIL> |
| Snowflake/snowflake-arctic-embed-l-v2.0 | ~568M | 8K | <FILL> | <FILL> | <PASS/FAIL> |

## Comparison to Phase A

| metric | Phase A (5 KB cap, CodeBERTa) | Phase B panel best |
| --- | --- | --- |
| nomic τ | −0.0085 | <FILL> |
| best candidate τ | −0.0911 (CodeExecutor) | <FILL> |
| best Δ | −0.0826 | <FILL> |

## Verdict

<FILL: one of>
- **PASS at acceptance.** Δ ≥ +0.15. Phase B greenlit with model X. See `docs/superpowers/plans/2026-05-12-behavioral-embeddings-sidecar.md`.
- **PASS at gate.** Δ in [+0.05, +0.15). Escalating to SFR-2B for stronger evidence; Phase B will proceed regardless.
- **BORDERLINE.** Δ in [−0.02, +0.05). Escalating to SFR-2B and one decoder-as-embedder model before deciding.
- **FAIL.** Δ < −0.02. Second-pass rejection. Six distinct encoder embedders have now been evaluated; none beat the general-text baseline.

## Reproduce

```sh
cd experiments/started/behavioral-embeddings
./run.sh
```

Requires Ollama running with `nomic-embed-text` pulled (`ollama pull nomic-embed-text`), `gh auth status` showing an authenticated user, and `uv` on PATH.
```

Pick the verdict that matches the actual numbers. Replace each `<FILL>` with concrete values.

- [ ] **Step 3: Commit**

```bash
git add experiments/started/behavioral-embeddings/RESULTS_PANEL.md
git commit -m "experiments: panel re-test results"
```

---

## Task 10: Apply the decision-matrix outcome

This is the conditional final task. The specific actions depend on the verdict written in RESULTS_PANEL.md.

- [ ] **Step 1: Identify the decision-matrix row**

From the RESULTS_PANEL.md verdict, identify which of four branches applies:

- **Branch A: Δ ≥ +0.15 (acceptance met).** Skip to Step 2.
- **Branch B: Δ in [+0.05, +0.15) (gate cleared).** Skip to Step 3 (escalation).
- **Branch C: Δ in [−0.02, +0.05) (borderline).** Skip to Step 4 (deeper escalation).
- **Branch D: Δ < −0.02 (clear fail).** Skip to Step 5 (rejection addendum).

- [ ] **Step 2: BRANCH A — Write Phase B sidecar plan**

Create `docs/superpowers/plans/2026-05-12-behavioral-embeddings-sidecar.md`. Use the original spec's "Implementation sketch" section (in `docs/superpowers/specs/future/future-work-behavioral-embeddings.md` lines 37–73) as the architectural skeleton. Substitute the winning model name from RESULTS_PANEL.md for "CodeExecutor" throughout. Include:

- Task 1: license check on the chosen model (CC-BY-NC blocks; Apache 2.0 / MIT permits).
- Task 2: minimal Python sidecar (`embed/sidecar/server.py`) — FastAPI + the chosen model.
- Task 3: Go side: `internal/embed/sidecar.go` with process management and the `Embedder` interface implementation.
- Task 4: Bootstrap wiring — `--embedder-backend sidecar` flag and fallback to Ollama on sidecar failure.
- Task 5: Docker recipe.
- Task 6: README documentation.
- Task 7: Performance benchmark on the original 200-PR fixture.

Then update the spec status: read `docs/superpowers/specs/future/future-work-behavioral-embeddings.md`, replace the Phase A "Rejected" line with:

```markdown
**Status:** Originally deferred from v1. Phase A rejected the proposed CodeExecutor model on 2026-05-11. Phase B's 4-model panel (2026-05-12) found that **`<WINNING MODEL>`** clears the +0.15 acceptance threshold; Phase B sidecar plan at `docs/superpowers/plans/2026-05-12-behavioral-embeddings-sidecar.md` is authorized.
```

Commit:
```bash
git add docs/superpowers/plans/2026-05-12-behavioral-embeddings-sidecar.md docs/superpowers/specs/future/future-work-behavioral-embeddings.md
git commit -m "docs: behavioral-embeddings Phase B authorized; sidecar plan for <WINNING MODEL>"
```

End plan execution. **Done.**

- [ ] **Step 3: BRANCH B — Gate cleared but below acceptance; escalate to SFR-2B**

Run the 2B sibling model:

```bash
cd experiments/started/behavioral-embeddings && .venv/bin/python embed_hf.py \
  --model Salesforce/SFR-Embedding-Code-2B_R \
  --features features.json \
  --out sfr_code_2b_vectors.json \
  --cache-dir hf_cache \
  --batch-size 8 2>sfr_code_2b_run.log
```

Expected: ~30–60 min wall-clock; ~8 GB RAM peak. If OOM, lower batch-size to 4 or 2.

Then `.venv/bin/python analyze.py --codeexecutor sfr_code_2b_vectors.json --out sfr_2b_analyze.md`.

Append a new "Escalation: SFR-2B" section to `RESULTS_PANEL.md` with the result. Update the verdict:

- If SFR-2B Δ ≥ +0.15: proceed as Branch A but with SFR-2B as the winning model.
- Otherwise: proceed as Branch A but with the panel winner from Task 9. Note in the Phase B plan that the chosen model is below acceptance and explain that the gate was nonetheless cleared.

Commit the escalation results before writing the sidecar plan:
```bash
git add experiments/started/behavioral-embeddings/RESULTS_PANEL.md experiments/started/behavioral-embeddings/.gitignore
git commit -m "experiments: SFR-2B escalation result"
```

Then continue per Step 2's instructions.

- [ ] **Step 4: BRANCH C — Borderline; escalate to SFR-2B AND a decoder-as-embedder**

Run SFR-2B per Step 3. Additionally, run one decoder-as-embedder candidate. The simplest path is `Qwen/Qwen2-1.5B-Instruct-Embedding` (if it exists) or `intfloat/e5-mistral-7b-instruct`. Note: decoder embedders use last-token pooling, not mean-pooling. This requires a code change to `embed_hf.py`:

In `embed_batch`, replace the mean-pooling block with a pooling-mode switch. Read the current pooling code; insert at the top of `embed_batch`:

```python
def _pool_mean(hidden, mask):
    # [B, T, H] * [B, T, 1] -> [B, H]
    summed = (hidden * mask).sum(dim=1)
    counts = mask.sum(dim=1).clamp(min=1)
    return summed / counts


def _pool_last_token(hidden, mask):
    # For decoder-only models. Pick the last non-padded position.
    # mask: [B, T, 1]
    seq_lens = mask.squeeze(-1).sum(dim=1).long() - 1
    return hidden[torch.arange(hidden.size(0)), seq_lens]
```

Add a CLI flag `--pooling {mean,last}` (default `mean`) and pick the pooling function based on it. Call decoder models with `--pooling last`.

Append a new "Escalation: SFR-2B + decoder" section to `RESULTS_PANEL.md`.

If either escalation crosses +0.05: proceed per Branch A/B with whichever model passes.
If neither crosses: fall through to Branch D.

- [ ] **Step 5: BRANCH D — All variants fail; write rejection addendum**

The Rejected status stays. Append a section to `RESULTS.md` (not RESULTS_PANEL.md — RESULTS.md is the canonical record):

Read the current `RESULTS.md`. After the existing "Addendum — CodeBERTa replication" section, append:

```markdown
## Addendum — 4-model panel + escalations (2026-05-12)

Phase B's 4-model panel ran modern long-context embedders through the
same harness (full-context, num_ctx=8192 baseline). All four — plus the
SFR-2B and decoder-as-embedder escalations, if triggered — failed the
+0.05 gate. Full numbers and per-model verdicts: `RESULTS_PANEL.md`.

**The cumulative result:** six (or more) distinct encoder embedders
across two pre-training paradigms (MLM, execution-simulation) and four
size tiers (137M to 2B) have now been evaluated against the same 53-pair
hand-curated dataset. None beat the general-text baseline by even
the gate threshold of +0.05 Kendall's tau.

The spec status remains **Rejected**. Phase B is not authorized. Any
future re-evaluation should consider a different category of model
(decoder-as-embedder with last-token pooling, or contrastive
fine-tuning on diff-style pairs) and/or a different dataset (larger
median diff size, paired-on-intent rather than paired-on-existence).
```

Then update the spec at `docs/superpowers/specs/future/future-work-behavioral-embeddings.md`. Replace the existing Status line with:

```markdown
**Status:** Rejected after gate experiment on 2026-05-11 (Phase A) and panel re-test on 2026-05-12 (Phase B). Six distinct encoder embedders evaluated across two pre-training paradigms, four size tiers; none beat the general-text baseline by ≥ +0.05 Kendall's tau. See `experiments/started/behavioral-embeddings/RESULTS.md` and `experiments/started/behavioral-embeddings/RESULTS_PANEL.md`.
```

Commit:
```bash
git add experiments/started/behavioral-embeddings/RESULTS.md docs/superpowers/specs/future/future-work-behavioral-embeddings.md
git commit -m "docs: behavioral-embeddings Phase B rejected after 4-model panel"
```

End plan execution. **Done.**

- [ ] **Step 6: Update PR #13 description**

Regardless of branch, append a section to the PR description summarizing the re-test outcome:

```bash
gh pr edit 13 --body "$(gh pr view 13 --json body --jq '.body')"
```

Then manually edit to append (replace `<VERDICT>` with the actual outcome):

```markdown
## Update — Panel re-test (2026-05-12)

A second-pass gate experiment ran 4 modern long-context embedders through the same harness:
- nomic-ai/nomic-embed-code
- jinaai/jina-embeddings-v2-base-code
- Salesforce/SFR-Embedding-Code-400M_R
- Snowflake/snowflake-arctic-embed-l-v2.0

**Verdict: <VERDICT>.** Full numbers in `experiments/started/behavioral-embeddings/RESULTS_PANEL.md`.
```

---

## Self-Review

After writing this plan, checked against the spec at `docs/superpowers/specs/2026-05-12-behavioral-embeddings-panel-design.md`:

**1. Spec coverage:**
- Architecture & scope (single panel re-test, reuse harness) → Task 2 + structure documented in header.
- 4-model panel (specific names + hypotheses) → Tasks 1 (verification) + 8 (runs).
- Truncation & prompt strategy (3 specific changes) → Tasks 3 (dynamic max_length), 4 (num_ctx), 5 (drop caps).
- Decision matrix → Task 10 with all 4 branches.
- Deliverables (RESULTS_PANEL.md + conditional sidecar plan / addendum) → Task 9 (write panel) + Task 10 (apply outcome).
- Risks (HF identifier drift, license, memory) → Task 1 (drift), Task 10 Step 2 (license check), Task 8 (OOM mitigation).

**2. Placeholder scan:** No "TBD" or "implement later" patterns. Every code step has complete code. `<FILL>` markers in RESULTS_PANEL.md template are explicit fill-in-the-blanks for measured numbers, not placeholders for designer intent.

**3. Type consistency:** `_effective_max_length(tokenizer, ceiling)` signature is consistent across Task 3's test and implementation. Model names in Task 8 match those introduced in Task 1. The output JSON filenames in Task 8 match those gitignored in Task 6 and referenced in Task 9.
