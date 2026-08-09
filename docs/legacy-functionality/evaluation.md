# Evaluation and model-selection evidence

## 1. Preserved harness

The complete experiment is under
`experiments/started/behavioral-embeddings/` — the remaining cross-model,
historical evaluation harness (Go-side manual gates are covered in §4; both
survive `19dd9f5`, which removed only the reranker/labeler evaluators).
Important artifacts:

- `features.json`: extracted PR/fork feature records;
- `judgments.json`: 53 hand-curated pairs, 27 same-intent and 26 different-intent;
- `pr_states.json`: source PR state;
- `embed_nomic.py`, `embed_hf.py`, `embed_ovms.py`, `embed_common.py`: embedding
  runners and shared feature preparation;
- `analyze.py`: cosine pair scoring and Kendall tau-c analysis;
- `run.sh`, `run_panel.sh`, and supplement scripts;
- `RESULTS.md`, `RESULTS_PANEL.md`, and model-specific result files;
- unit tests for feature preparation, embedding, and analysis.

This is valuable because it retains both the labels and the methodological
history. Do not treat the first result file as the final verdict without reading
the panel result: the Phase-A conclusion was superseded.

## 2. What the dataset measures

The original population was PRs from `IBM/mcp-context-forge`, not a pure set of
divergent GitHub forks. The reason was empirical: most public forks were pristine
copies with no comparable work, while PR diffs were the canonical available
signal for change intent.

Each record is built from Spoon's `embed.ForkFeatures`: touched paths, commit
subjects, README content, and diff content. The experiment concatenates those
modalities with structural tags, embeds each record, computes pairwise cosine
similarity, and compares the resulting ranking with binary same/different intent
labels using Kendall's tau-c.

The committed pair set is intentionally close to balanced but not statistically
independent: some PRs occur in more than two pairs. That makes it a useful
engineering regression fixture, not a definitive population estimate.

## 3. Historical results that matter for reimplementation

### Phase A: misleading short-context result

`RESULTS.md` records a 53-pair full-coverage run where a CodeExecutor-style
candidate underperformed the `nomic-embed-text` baseline. The initial positive
result used only 35 pairs because Ollama returned HTTP 500 for large prompts;
per-modality truncation restored the missing pairs and both correlations dropped
near zero. The authoritative Phase-A delta was `-0.0826`, not the biased first
run's apparent positive signal.

Lesson: a model comparison that silently drops long documents is invalid.
Control context limits, truncation, failed requests, and the set of pairs before
comparing scores.

### Panel re-test

`RESULTS_PANEL.md` is the later panel record. It evaluated the same 53 labels
using full/native context where possible and reported:

| Model | Context/params | Kendall tau-c | Delta vs nomic |
|---|---|---:|---:|
| nomic-embed-text-v1 | 8K / 137M | 0.0883 | baseline |
| jinaai/jina-embeddings-v2-base-code | 8K / 161M | 0.0199 | -0.0684 |
| Salesforce/SFR-Embedding-Code-400M_R | 32K / 400M | 0.0570 | -0.0313 |
| Snowflake/snowflake-arctic-embed-l-v2.0 | 8K / 568M | 0.2079 | +0.1196 |
| Salesforce/SFR-Embedding-Code-2B_R | 32K / 2B | 0.0313 | -0.0570 |

A community 7B Q6_K model showed a passing delta on only 43/53 pairs, but
that result was biased by context drops. The panel authorized the Snowflake
Arctic sidecar plan, not the heavier code-specific model.

The panel also records that mean-pooling was used for encoder models and that a
2B decoder-style model performed poorly under an encoder-style mean-pooling
assumption. Pooling is therefore part of the model contract, not an incidental
implementation detail.

## 4. In-process model evaluation

`internal/embed/eval_models_test.go` is a manual harness, enabled by environment
variables rather than a CI gate. **At HEAD only the embedder gate survives:**
`19dd9f5` changed this file by +5/−96 (net −91 lines) — the reranker and
labeler evaluation paths were excised along with the features. The reranker
evaluator reported accuracy@1 and MRR over one positive against ten random
negatives; the labeler evaluator was qualitative (representative cluster
hints). Neither `TestEvalRerankers_Manual` nor `TestEvalLabelers_Manual`
exists at HEAD.

```sh
# survives at HEAD:
SPOON_EVAL_EMBEDDERS="builtin,/path/model[:pooling],..." \
  go test -run TestEvalEmbedders_Manual -v ./internal/embed/
```

The embedder evaluator reports AUC: probability that same-intent cosine exceeds
different-intent cosine.

The OpenVINO defaults retained by the current docs were revalidated on
2026-06-12 (historical record — the models themselves are removed at HEAD;
see [reranker-labeler.md](reranker-labeler.md)):

- reranker: `bge-reranker-base-fp16`, retained over
  `Qwen3-Reranker-0.6B-seq-cls-fp16` (`acc@1 0.63`, `MRR 0.77`, 1.7 s in the
  recorded run);
- labeler: `Qwen2.5-1.5B-Instruct-int4`, retained over `Qwen3-0.6B-int4`
  (3/3 usable labels at 109 ms warm versus 0/3 when thinking consumed the
  output budget).

These are small evaluation samples. They justify the historical defaults but do
not justify treating them as immutable.

## 5. Revalidation checklist

Before restoring or replacing a removed model-backed feature:

1. Freeze the exact feature-document builder and modality caps.
2. Record model ID, tokenizer version/template, pooling, normalization,
   maximum context, dimension, and serving backend.
3. Run all 53 judgments with no silent request drops. Report failed rows.
4. Compare against the current FastEmbed baseline and the lexical baseline.
5. Use paired confidence intervals or a paired significance test; do not compare
   headline tau/AUC values from different pair subsets.
6. Include long diffs and decoder-style models in separate controlled cases.
7. Add a Go regression test for the model identity and vector dimension before
   enabling persistence.
8. Rebuild the semantic index when changing body construction, model identity,
   dimension, pooling, or normalization.
