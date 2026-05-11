# Behavioral Embeddings Validation — Results

**Date:** 2026-05-11
**Verdict:** **FAIL — feature rejected; do not build Phase B.**
**Branch:** `behavioral-embeddings-validation`
**Spec:** `docs/superpowers/specs/future/future-work-behavioral-embeddings.md`

## Headline numbers

| metric | value |
| --- | --- |
| Kendall's tau (nomic-embed-text) | **−0.0085** |
| Kendall's tau (CodeExecutor) | **−0.0911** |
| Delta (CE − nomic) | **−0.0826** |
| Gate threshold | ≥ 0.05 |
| Acceptance threshold (full feature) | ≥ 0.15 |
| Pairs scored | 53 of 53 |
| Pairs skipped (missing vector) | 0 |

The gate **fails** by 0.13 (delta is −0.08 vs. required +0.05). The full feature's acceptance threshold (+0.15) fails by 0.24. CodeExecutor is consistently worse than nomic-embed-text on this dataset, not better.

## Reproduce

```sh
cd experiments/behavioral-embeddings
./run.sh
```

Requires Ollama running with `nomic-embed-text` pulled, `gh auth status` showing an authenticated user, and `uv` on PATH.

## What was measured

53 hand-curated PR pairs from `IBM/mcp-context-forge`:
- **27 labeled `1`** (functionally same intent): e.g., two versions of the same fix, two release-1.0.0 PRs, two PRs caching the same user-team lookup.
- **26 labeled `0`** (functionally different intent): pairs touching distinct areas or distinct bugs in the same file.

Each PR's `embed.ForkFeatures` (Paths, Commits, ReadmeDoc, DiffChunk) was concatenated with structural tags, truncated to ~5 KB total (per-modality caps), and embedded both by Ollama-served `nomic-embed-text` (768-dim) and by `microsoft/codeexecutor` via HuggingFace transformers (mean-pooled). Pair-wise cosine similarity was computed; Kendall's tau (variant `c`) was computed between similarity ranking and the binary judgment vector. The gate compares the two taus.

## Two-run summary

| run | τ nomic | τ CE | Δ | pairs scored | failed embeds | note |
| --- | --- | --- | --- | --- | --- | --- |
| 1: no truncation | 0.3592 | 0.2873 | −0.0718 | 35 | 34 nomic 500s | biased subset (small-medium PRs only) |
| 2: per-modality cap | −0.0085 | −0.0911 | −0.0826 | **53** | **0** | full coverage |

The first run misleadingly looked like nomic was producing useful rank correlation. That τ ≈ 0.36 came from the 35 pairs whose PRs were small enough to fit Ollama's context window — the 18 big-PR pairs were dropped entirely because nomic returned HTTP 500. After applying per-modality truncation so both embedders see the same input, the dropped pairs come back with truncated content that doesn't carry intent well, and both taus collapse toward zero.

**The full-dataset numbers are the authoritative ones.** The first run's positive nomic τ is a sampling artifact.

## Deviations from the original plan

These are non-trivial and worth carrying into any future re-run:

1. **Source population pivoted from forks to PRs.** The plan asked for "20 fork pairs from IBM/mcp-context-forge." Probing showed ~98% of forks on GitHub are pristine clones on default branch — only 8 of 653 IBM/mcp-context-forge forks had any divergence to compare. Switching upstream repos didn't help (cli/cli, bubbletea, astral-sh/uv all showed 1–10% divergence rate on top-30 starred forks). PRs are the canonical signal of fork-with-intent; we listed 200 most-recently-updated PRs and used their diffs.

   Implication: 158 of the 200 PRs are from internal branches on `IBM/mcp-context-forge` itself rather than external forks. The experiment measures embedder fidelity on PR-style diffs, which is what spoon's clustering downstream would actually consume — but it isn't strictly the "fork-pair similarity" the spec described.

2. **Python pin bumps for Python 3.14 (cp314) compatibility:**
   - `torch` 2.5.1 → 2.9.0 (no cp314 wheel for older versions)
   - `transformers` 4.46.2 → 4.47.1 (the older pin pulls `tokenizers==0.20.3` which has no cp314 wheel and fails to build)
   - `numpy` 2.1.3 → 2.4.4 (no cp314 wheel; source build needs Meson/OpenBLAS)
   - `scipy` 1.14.1 → 1.16.3 (no cp314 wheel; source build needs OpenBLAS)

3. **Kendall's tau variant.** SciPy 1.16.3 defaults to `variant='b'`, which penalizes ties in the binary-label vector. We use `variant='c'` (tau-c, designed for rectangular tables) so the formula returns ±1 for perfectly-ranked binary labels rather than ~0.82.

4. **Quota rule informally violated.** The plan said "each PR in at most one same + one different pair." In practice several PRs appear in 2–4 pairs (pr-4678 and pr-4646 each in 4; six others in 3; nine in 2). This introduces correlated noise but is not fatal.

## Caveats — why the verdict is robust but not unimpeachable

Three things would meaningfully change this experiment's outcome if they were fixed:

1. **Per-modality 5 KB truncation may be destroying signal.** The biggest PRs in our set list 100–1240 files; we cap Paths at 1500 chars, which keeps maybe the first 30 file names. CodeExecutor's native window is 512 tokens (~2 KB); even within our 5 KB cap, CodeExecutor only sees ~40% of the text. A more careful experiment would pick a single fixed cap that fits within CodeExecutor's context end-to-end, and tune that cap on a held-out probe set. We did not.

2. **CodeExecutor's pre-training is Python-heavy.** This repo IS Python-heavy (~70% Python files in the dataset by frequency), so language-mismatch isn't the issue here. But CodeExecutor was trained on synthetic execution traces of complete programs, not on diffs. The spec acknowledged this: "May underperform on partial-program inputs. Initial experiments should verify." This is the experiment that verified it.

3. **The PR-bearing-fork dataset is biased toward small fixes.** Median diff size is 994 chars; many of the PRs are dependabot-style version bumps, one-line bug fixes, or release-tag updates. Behavioral embedding is most discriminating when the diff actually *changes behavior*. Tiny diffs may not produce enough activation differential in CodeExecutor's last hidden state for mean-pooling to differentiate them.

If you want to challenge this verdict, the cheapest follow-up is: re-run on a held-out 10% subset where every diff is ≥ 500 lines AND where labels are picked specifically to distinguish "different bug in same file" from "same bug in same file." That's the case the spec's research basis (Perera et al., CodeExecutor) was strongest for, and the case where this run had the fewest examples.

## What was committed

The branch contains:

- **`cmd/dump_features/`** — Go helper that lists PRs from GitHub REST, fetches diffs/commits, and dumps `embed.ForkFeatures` JSON. Tests pass.
- **`features.json`** — 200 PR records (fixture).
- **`judgments.json`** — 53 hand-curated PR-pair labels.
- **`pr_states.json`** — state (MERGED/CLOSED/OPEN) for all 2345 IBM/mcp-context-forge PRs, used to rank candidates during curation.
- **`embed_common.py`**, **`embed_nomic.py`**, **`embed_codeexecutor.py`**, **`analyze.py`** — Python embedding + analysis. 14 unit tests pass.
- **`curate_helper.py`** — generates candidate browser/same/different markdown lists.
- **`curate_cli.py`** — interactive labeling tool: opens PR pairs in browser tabs, prompts y/n/s/q + rationale, saves to judgments.json.
- **`run.sh`** — orchestrator.
- This `RESULTS.md`.

## Recommendation

Do **not** open a follow-up plan for the Phase B sidecar. The deferred status of the feature was correct.

If new evidence emerges (e.g., a CodeExecutor successor model with longer context, or a larger curated dataset with bigger diffs), revisit this experiment by rerunning `./run.sh` after replacing the embedder model in `embed_codeexecutor.py`. The harness is intact and the labeling work is preserved.

The spec at `docs/superpowers/specs/future/future-work-behavioral-embeddings.md` should be updated to reflect this result (status: Rejected after gate experiment on 2026-05-11).
