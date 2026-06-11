# Behavioral Embeddings — 4-Model Panel Re-Test (Design)

**Status:** Draft — design for a second-pass gate experiment after Phase A's failure.
**Parent context:**
- Phase A spec (rejected): `docs/superpowers/specs/future/future-work-behavioral-embeddings.md`
- Phase A plan: `docs/superpowers/plans/2026-05-11-behavioral-embeddings-validation.md`
- Phase A results: `experiments/started/behavioral-embeddings/RESULTS.md`
- Phase A PR: #13

**Estimated effort:** Half a day (design + plan + execution + write-up). Reuses the existing harness.

## Context

Phase A's gate experiment failed: CodeExecutor and CodeBERTa each underperformed nomic-embed-text by Kendall's tau ≈ −0.08. RESULTS.md attributes this to a *structural* limitation of "code-aware MLM with 512-token window + mean-pooling" rather than to behavioral-vs-syntactic pre-training. The spec was marked Rejected.

This design proposes a second-pass experiment to test whether the rejection generalizes to *modern long-context* code embedders. The hypothesis: Phase A's 512-token window was the binding constraint; a model with 7K–32K context that's also trained on code may clear the +0.05 gate.

## Goals

1. Determine empirically whether *any* of four chosen long-context embedders beats nomic-embed-text by ≥ 0.05 Kendall's tau on the same 53 hand-curated PR pairs.
2. If yes → write a Phase B sidecar implementation plan that uses the winning model. The Rejected spec status flips to "Phase B in progress."
3. If no → strengthen the rejection. The result will then show 6 distinct encoder embedders failing across two pre-training paradigms (MLM, execution-simulation) and two model sizes (137M–568M). The Rejected status stands.

## Non-Goals

- Changing the labeling, the 200-PR fixture, or the 53 judgment pairs. Phase A's data is the input.
- Touching `internal/embed/` production code. No production behavior changes until Phase B itself, which this experiment gates.
- Testing decoder-as-embedder models (different category, different pooling). Reserved as a borderline-case escalation only.
- Testing commercial APIs (breaks spoon's local-first design).

## Architecture & scope

A second-pass gate experiment that reuses the existing `experiments/started/behavioral-embeddings/` harness almost verbatim. Each of 4 HF models is loaded via the existing `embed_codeexecutor.py` (to be renamed `embed_hf.py`) and produces a `<model>_vectors.json`. `analyze.py` is invoked once per candidate, comparing it to a freshly-recomputed nomic-embed-text baseline. The aggregated results land in a new `RESULTS_PANEL.md` next to (not replacing) `RESULTS.md`.

**What's NOT in scope:** changing the prompt-construction algorithm itself, the judgment set, or spoon's `internal/embed` code.

## The 4-model panel

Each model represents a different hypothesis about why CodeBERTa/CodeExecutor failed in Phase A. If one clears the gate, we know which factor was decisive.

| # | Model | Params | Context | RAM (CPU) | Hypothesis it tests |
| --- | --- | --- | --- | --- | --- |
| 1 | `nomic-ai/nomic-embed-code` | ~137M | 7K tokens | ~1.5 GB | "The code variant of our baseline is better." Same shop as nomic-embed-text, released early 2025, code-specific pre-training, long context. Cleanest scientific control: code-specialization on top of our working baseline architecture. |
| 2 | `jina-embeddings-v2-base-code` | ~161M | 8K tokens | ~1.5 GB | "The model spoon already cataloged as code-aware works." It's in `PreferredEmbeddingModels` (`internal/embed/embed.go:47`) so someone evaluated it before; never benchmarked end-to-end. Cheap honorable mention. |
| 3 | `Salesforce/SFR-Embedding-Code-400M_R` | ~400M | 32K tokens | ~3 GB | "Recency + scale + huge context beats everything." MTEB Code Leaderboard tier as of late 2024 (the 400M variant; the 2B sibling is held in reserve as a borderline escalation). Tests whether the failure was just "we didn't use a modern-enough code model." |
| 4 | `Snowflake/snowflake-arctic-embed-l-v2.0` | ~568M | 8K tokens | ~2 GB | "Code-awareness was a red herring; what mattered was context length." A *general* long-context embedder with strong MTEB-general scores. If a non-code model with 8K context wins, the v1 design (use general embedders) was already correct and Phase B should be a context-length upgrade, not a behavioral one. |

**Not in the panel, on purpose:**
- `Salesforce/SFR-Embedding-Code-2B_R` (2B sibling): kept in reserve. Escalation only if model #3 is borderline (Δ in [−0.02, +0.04]). CPU embed time is ~30 min on its own.
- Decoder-as-embedder models (e.g., `Qwen2-7B-Embedding`): different category. If the encoder panel all fails, that's the next architectural pivot, and deserves its own design.
- CodeBERT-base / CodeBERTa / CodeExecutor: already tested in Phase A; not re-run.

## Truncation & prompt strategy

The only methodology change from Phase A. The 5 KB cap there was a workaround for a 512-token-window failure mode; it artificially handicapped both candidates and baseline. Now that the panel has 7K–32K context, the cap can come off.

1. **Remove the artificial char cap in `build_text()`.** Current `PATHS_MAX_CHARS=1500 / DIFF_MAX_CHARS=1500 / …` totals ~5 KB. Drop these; concatenate the full Paths/Commits/Readme/Diff payload. Spoon's Go-side `BuildFeatures` already caps `DiffChunk` at 4 KB, so natural per-record max is ~30 KB worst case (mostly the Paths list for huge PRs).

2. **Use each tokenizer's native max_length.** Replace the hardcoded `max_length=512` in `embed_codeexecutor.py:21` with `tokenizer.model_max_length`, capped at 32K for safety against tokenizers that report a sentinel value (some HF tokenizers report 1e9).

3. **Extend Ollama's `num_ctx` for the nomic baseline.** Ollama defaults `num_ctx` to 2048; nomic-embed-text supports 8192. Set `"options": {"num_ctx": 8192}` in `embed_nomic.embed_one()`. This is why Phase A's first run dropped 34 records — Ollama 500'd on prompts exceeding its (default-low) context, not because nomic itself couldn't handle them.

**Implication: the nomic baseline τ will change.** Phase A measured `nomic τ = −0.0085` at the 5 KB cap. With num_ctx=8192 and uncapped prompts, nomic will see substantially more content per record. Its τ will almost certainly rise — Phase A's first (biased) run hit τ ≈ 0.36 on the records that fit, suggesting somewhere in [0.1, 0.3] on the full set. **The gate becomes: panel_τ − new_nomic_τ ≥ +0.05.** A higher baseline raises the bar; this is the fair test.

The result isn't directly comparable to Phase A's −0.08 deficit (different prompt sizes both sides). Instead, it's a fresh "given fair context, can any code-aware embedder beat nomic" measurement. The Phase A record is preserved in RESULTS.md as historical context.

**Risk: slow CPU embeds at full context.** A 30 KB prompt is ~7.5K tokens. SFR-400M on CPU at that length is ~5–10 sec per record. 200 records × 4 models = ~70 min wall-clock worst case. Acceptable. The resume mechanism added during Phase A's second pass handles interruptions.

## Decision criteria

On the best-performing model in the panel:

"Second-pass rejection" = an addendum to `RESULTS.md` stating the 4-model panel ran and confirmed Phase A's verdict; spec status remains Rejected.

| best Δ (panel_τ − new_nomic_τ) | verdict | next step |
| --- | --- | --- |
| ≥ +0.15 | acceptance threshold met | write Phase B sidecar plan for this model. Update spec status from Rejected → "Phase B in progress." |
| +0.05 to +0.15 | gate cleared, below acceptance | escalate to SFR-2B (the heaviest in-class candidate). If it crosses +0.15, use it; otherwise Phase B proceeds with the panel winner, caveats noted. |
| −0.02 to +0.05 | borderline | escalate to SFR-2B AND one decoder-as-embedder candidate. If neither crosses +0.05, second-pass rejection. |
| < −0.02 | clear failure | second-pass rejection. Strong addendum to RESULTS.md: 6 distinct encoder embedders evaluated, none beat the general-text baseline. Spec stays Rejected. |

## Deliverables

1. **This design doc** at `docs/superpowers/specs/2026-05-12-behavioral-embeddings-panel-design.md`.
2. **Implementation plan** at `docs/superpowers/plans/2026-05-12-behavioral-embeddings-panel.md`, written next via the writing-plans skill.
3. **Code changes** on branch `behavioral-embeddings-validation`:
   - `experiments/started/behavioral-embeddings/embed_codeexecutor.py` → renamed `embed_hf.py`; max_length now dynamic.
   - `experiments/started/behavioral-embeddings/embed_nomic.py`: `num_ctx=8192` in the API call.
   - `experiments/started/behavioral-embeddings/embed_common.py`: drop the `*_MAX_CHARS` constants and the `_truncate()` calls; keep `build_text()` and `load_features()`.
   - `experiments/started/behavioral-embeddings/analyze.py`: unchanged externally — called 4× with different `--codeexecutor X.json --out X_results.md`.
   - `experiments/started/behavioral-embeddings/run.sh`: orchestrate 4 panel runs after the baseline re-run.
   - `experiments/started/behavioral-embeddings/.gitignore`: add the 4 new vector files.
4. **`experiments/started/behavioral-embeddings/RESULTS_PANEL.md`** (new). Cites both the Phase A and panel results, includes the decision-matrix outcome.
5. **Conditional**: either `docs/superpowers/plans/2026-05-12-behavioral-embeddings-sidecar.md` (if any model passes) OR a one-page addendum to `RESULTS.md` (if all fail).
6. **PR #13 updated** with an "Update: panel re-test" section in the description. No force-push needed — additive commits only.

**Not touched:**
- `features.json`, `judgments.json`, `pr_states.json` (Phase A's labeled data is the input).
- `internal/embed/` (no production code changes until Phase B itself).

**Cost estimate:**
- Disk: ~3–4 GB model weights downloaded once.
- CPU: ~70 min wall-clock for the 4 panel runs + baseline re-run.
- Token cost: 0 (local).
- Calendar: half a day end-to-end.

## Risks & open questions

1. **Model identifier drift.** HF model names may have shifted (e.g., `nomic-ai/nomic-embed-code` could be `…-v1`). Plan Task 1 verifies each name via `huggingface_hub` before runs commit time.

2. **License compatibility.** SFR-Embedding models are CC-BY-NC (non-commercial). For the experiment itself this is fine (research use). For Phase B, the chosen model's license must be permissive for spoon's distribution.

3. **Memory pressure.** ~3 GB heaviest panel model + Ollama in background. Fits 15 GiB RAM but tight. The Phase A resume mechanism mitigates the cost of OOM-mid-run — restart skips already-embedded records.

4. **Spec-state un-rejection.** If the panel passes, the Phase A spec status flips back to in-progress, but for a model that wasn't in the original spec. The status note should preserve the Phase A rejection as history and append the re-evaluation outcome transparently: "Originally deferred from v1, rejected after Phase A gate (2026-05-11), re-evaluated 2026-05-12, model X cleared the gate, Phase B authorized."

5. **Decoder-as-embedder escalation cost.** The decision matrix references a decoder-model escalation for the borderline case. If that triggers, it's non-trivial new code (last-token pooling, decoder-specific tokenization). Worth flagging as "may need its own design" point if it fires.

6. **Open question: commit new vector files or gitignore?** Phase A gitignored vector files (regenerable, ~3 MB each). This design extends the same pattern to the 4 new candidates (`.gitignore` update is one of the deliverables). Saves ~15 MB; no reproducibility cost since the experiment is end-to-end automatable.

## How to complete

1. **Verify HF model names.** Probe `huggingface_hub` for each of the 4. If any has drifted, pick the closest current name and document the substitution in `RESULTS_PANEL.md`.
2. **Rename `embed_codeexecutor.py` → `embed_hf.py`.** Update `run.sh` references.
3. **Remove truncation caps from `embed_common.py`.** Keep tests passing — the truncation test will need updating or removal.
4. **Switch HF tokenizer max_length to dynamic.** `tokenizer.model_max_length` capped at 32K.
5. **Set `num_ctx=8192` in `embed_nomic.py`.**
6. **Rerun the baseline.** New `nomic_vectors.json`. The Phase A nomic numbers in RESULTS.md stay as historical record.
7. **Run each of the 4 panel models.** One at a time; resume support already exists. ~5–20 min each.
8. **Run analyze.py 4× against the new baseline.** Collect the 4 deltas.
9. **Write `RESULTS_PANEL.md`** with the panel table and the decision-matrix outcome.
10. **Take the deliverable for the matched verdict row.** Phase B sidecar plan, escalation runs, or rejection addendum.
