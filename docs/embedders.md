# Spoon's embedders

Spoon's default configuration runs every model-backed feature **in-process** —
no external service, no model server, no API key. There is one local embedder,
**fastembed**, plus a zero-setup lexical fallback.

Setting `VOYAGE_AI_API_KEY` adds a second, optional layer: Voyage AI's
`voyage-code-3` embeddings and `rerank-2.5` cross-encoder. Voyage is the only
part of spoon that calls an external service. It is **additive** — it runs
alongside fastembed rather than replacing it, and turning it on or off never
invalidates the local index.

| Feature | What it does | Model (default) | Runtime |
|---|---|---|---|
| Semantic embedder | persistence, `spn search`, clustering, categories | fastembed `fast-bge-small-en-v1.5` (384-dim) | ONNX Runtime |
| Lexical fallback | clustering when fastembed is unavailable / in the TUI; `--query` and `/` relevance | built-in (deterministic) | none |
| Voyage embedder (opt-in) | a second persistent index for `spn search --voyage` | `voyage-code-3` (1024-dim) | Voyage API |
| Voyage reranker (opt-in) | second-stage relevance for `spn search`, `--query`, TUI `R` | `rerank-2.5` | Voyage API |

## FastEmbed — the embedder

FastEmbed is the fixed `fast-bge-small-en-v1.5` BGE model (384 dimensions, max
length 512) run in-process through native Go and ONNX Runtime. Its semantic
identity is `fastembed:fast-bge-small-en-v1.5:maxlen=512:prompts=bge` (used as the
embeddings' cache/model key). Models cache under
`$XDG_CACHE_HOME/spoon/models/fastembed`.

It is the **default** on every `spn forks list` — opt-out, not opt-in — and
powers:

- **Persistence + semantic index** — after each run, new or changed fork
  documents are embedded and stored in the global libsql store
  (`$XDG_CONFIG_HOME/spoon/spoon.db`; `/var/lib/spoon/spoon.db` on no-home
  hosts).
- **`spn search "<query>"`** — cosine ranking over the stored vectors.
- **Clustering** — when fastembed is active it embeds the modality blobs (see
  below); otherwise the lexical fallback does.
- **Zero-shot categories** — the per-fork `category` facet.

### Setup, opt-out, and graceful degradation

FastEmbed needs ONNX Runtime present:

```sh
export ONNX_PATH=/path/to/libonnxruntime.so   # e.g. /usr/lib/libonnxruntime.so
spoon setup                                    # downloads the model, writes config
```

- If ONNX Runtime is **unavailable**, a run degrades: it emits an
  `embed_unavailable` warning and continues without semantic indexing —
  listing, scoring, and (lexical) clustering are unaffected. A missing native
  runtime never fails a run.
- Pass `--no-embed` (or `SPOON_NO_EMBED=1`) to skip embedding entirely.
- The global store, by contrast, is mandatory: it is the cross-invocation
  cache and the persistence layer in one, and a run that cannot open it fails
  loudly rather than running silently uncached.

### Semantic search

```sh
spn forks list owner/repo          # prospect + index (fastembed default)
spn search "oauth rate limiting" --top 20          # cross-repo semantic search
spn search "wayland support" --repo owner/repo     # scoped to one upstream
```

Each list run embeds only new or changed documents in batches of 32, so re-runs
re-embed nothing unchanged. The pending set is computed by joining each
document's content hash against the embedding row for that model — the hash
covers the **body alone**, deliberately not `modelID + body`, so two models can
hold vectors for the same document without either orphaning the other's rows.
`spn search` emits deterministic score-descending NDJSON; an empty index is a
successful empty result with a `semantic_index_empty` warning.

## Voyage AI (opt-in)

Voyage is enabled when **both** of these hold:

1. an API key resolves — `VOYAGE_AI_API_KEY`, or `VOYAGE_API_KEY` (what Voyage's
   own SDKs read), or `embedder.voyage.apiKeyFile` in the config pointing at a
   0600 file; and
2. the store is writable, verified by an actual write before any request.

The second condition is not an optimization. The store holds both the cached
responses and the vectors Voyage is paid to produce, so with nowhere durable to
write, every run would buy results it discards at exit and buy them again next
time. That is worse than not using the provider, so Voyage stays off and says so.

```sh
export VOYAGE_AI_API_KEY=...
spn forks list owner/repo            # indexes with fastembed AND voyage-code-3
spn search "oauth refresh" --voyage  # ranks against the Voyage index
spn search "oauth refresh"           # ranks against fastembed, still reranked
```

### Additive, not a replacement

`embeddings` is keyed `(document_id, model)`, so a Voyage vector and a fastembed
vector coexist for the same document. Consequences worth knowing:

- Enabling Voyage does **not** rebuild or invalidate the fastembed index, and
  disabling it does not lose anything either.
- `spn search` defaults to the fastembed index. `--voyage` selects the other
  partition; each output record carries `model`, so which index answered is
  always visible.
- `spn search --voyage` needs no ONNX Runtime at all — fastembed is not loaded
  on that path.
- The stored vector's identity is `voyage:voyage-code-3:dim=1024:input_type=qd`.
  Changing `outputDimension` changes that identity, which re-partitions the
  index: the old rows stay behind unreferenced and every document becomes
  pending under the new one.
- **Clustering deliberately stays local.** It embeds four modality blobs per
  fork and needs only within-run comparability, which the local embedders
  already give — routing it through a paid API would multiply the spend for no
  ranking gain.
- The two layers fail independently, so "fastembed unavailable but Voyage on" is
  a reachable state: the run emits `embed_unavailable`, clusters lexically at
  epsilon 0.55, and still builds the Voyage index. `spn search` then finds
  nothing (its default partition is empty) while `spn search --voyage` works.

### Reranking

A reranker is a cross-encoder: it scores a (query, document) pair jointly rather
than comparing two independently produced embeddings, which is why it is worth a
network round trip on top of vector retrieval. It fills the `embed.QueryScorer`
seam that the lexical scorer has stood in for.

Three surfaces, all falling back to the lexical scorer when Voyage is off:

| Surface | Candidates | Output |
|---|---|---|
| `spn search` | top `--top` x `--rerank-overfetch` (default 5) by cosine | `rerankScore`, `rerankModel`; `score` stays the retrieval cosine |
| `spn forks list --query` | every enriched fork's change digest | `queryScore`, `queryMethod` = `voyage` |
| `spoon` TUI `R` | every fork the `/` filter admits, with compare data | table re-ordered; footer names the scorer |

Reranking is orthogonal to which index retrieved the candidates — it never
touches the stored vectors — so `spn search` reranks the fastembed candidate set
just as well as the Voyage one. That is the cheapest useful configuration: one
rerank call per search, no per-document embedding spend.

Scores are clamped to `[0,1]`, one per input document in input order, matching
the lexical scorer's contract. Requests are chunked well under Voyage's caps
(1000 documents, and a token budget charged as
`query tokens x documents + sum of document tokens`); cross-encoder scores are
per-pair and therefore comparable across chunks.

### Cost control

Voyage bills per token, so nothing is paid for twice:

- **Documents** are embedded only when new or changed, via the same
  content-hash join fastembed uses.
- **Every request item** is cached in `spoon.db` (`voyage_cache`), keyed by a
  SHA-256 of everything that affects the response: model, input type, output
  dimension and text for an embedding; model, query and document for a rerank
  pair. Caching is per *item*, not per request body — so adding one fork to a
  hundred-fork run costs one item, not a hundred.
- **Duplicates collapse** within a single call. This is not hypothetical: spoon
  tracks forks that carry identical work, and identical work yields an identical
  digest.
- Entries expire after 30 days, since a model served under an unchanged name can
  be updated upstream. `SPOON_VOYAGE_NO_CACHE=1` bypasses the cache and re-pays.

Each run reports what it spent and what it saved on stderr, as
`voyage_indexing` (documents about to be sent, emitted even when zero, so
"this cost nothing" is distinguishable from "Voyage never ran") and
`voyage_tokens` (tokens billed, items served from cache, items deduplicated).

### Degradation

The rule: **a Voyage failure is fatal only on an invocation where the user named
Voyage explicitly.**

- `spn forks list` never fails on Voyage. A bad key, a 429, an outage or an
  unwritable store all emit a `voyage_unavailable` warning and the run continues
  with fastembed. Voyage indexing is a side effect of listing forks, and this
  repo's contract is that an optional model backend going missing must not fail a
  run.
- `spn search --voyage` and `spn search --rerank` **do** fail (exit 2) without a
  usable key: the user asked for that index by name, and silently answering from
  a different model's index would answer a different question.
- A reranker outage on `spn search` degrades to cosine order with a
  `rerank_unavailable` warning. Losing the second stage is acceptable; losing the
  search is not.
- 429 and 5xx are retried up to 3 times, honoring `Retry-After`.
- `--no-voyage` / `SPOON_NO_VOYAGE=1` skips Voyage while leaving fastembed on.

The API key is held in an unexported field and only ever sent as a header;
nothing interpolates it into an error, a log line or a URL, and server-supplied
error text is scrubbed of it as a second line of defense.

## The lexical fallback

When fastembed is not active (no ONNX Runtime, `--no-embed`, or the interactive
`spoon` TUI, which always clusters lexically), clustering uses a deterministic
built-in lexical embedder — zero setup, no downloads, no "embedder unreachable"
failure mode.

For each fork it builds four modality blobs — touched file paths, commit
messages (merges excluded), a README excerpt, and a normalized diffstat chunk —
and embeds each:

- tokens are lowercase alphanumeric runs plus within-line adjacent bigrams (so
  `internal/auth` path structure and "rate limit" commit phrasing carry
  signal);
- weights are sublinear TF × smoothed IDF, with IDF computed over the run's own
  corpus — boilerplate shared by every fork cancels out, fork-distinctive
  tokens dominate;
- token weights fold into a fixed 512-dim vector via signed feature hashing,
  L2-normalized.

The four vectors are concatenated with weights
`[paths 0.3, commits 0.3, readme 0.2, diff 0.2]`. Because its IDF is per-run,
the lexical embedder is only comparable *within* a run — which is why the
persistent index and `spn search` use fastembed (stable across runs), not the
lexical fallback.

## Clustering

Both embedders feed single-link agglomerative clustering at a cosine-distance
cutoff. The default `--cluster-epsilon` is **0.35** when fastembed is active
(dense-encoder cosines separate at a tighter scale) and **0.55** for the lexical
fallback. Cluster labels are heuristic and deterministic: the dominant directory
prefix (up to two levels) plus the top TF-IDF discriminators from member commit
messages, with a path-segment fallback when commit text is unavailable.

```sh
spoon --cluster-epsilon 0.45 golang/go     # tighter clusters
spoon --cluster-min-size 2 golang/go       # allow pairs
spoon --no-cluster golang/go               # skip clustering entirely
spoon --cluster-top 100 golang/go          # embed more forks
```

## Zero-shot categories

With fastembed active, each clustered fork gets a `category` facet (`feature`,
`bugfix`, `security`, `ci-build`, `docs`, `localization`, `dependencies`,
`port`, `config`) assigned by cosine similarity between its change digest and
anchor descriptions — no extra model, one extra embedding batch. Forks matching
no anchor stay uncategorized.

## Query-driven fork search (`--query`)

```sh
spn forks list owner/repo --query "wayland support"
```

Every enriched fork's change digest (commit subjects + touched paths) is scored
against the query and output is sorted by relevance; each NDJSON record gains
`queryScore` (0..1) and `queryMethod` — `voyage` when a Voyage key is
configured, `lexical` otherwise. This is distinct from `spn search`, which is
vector-similarity retrieval over a persistent index.

## Ranking forks in the TUI (`R`)

The TUI has two distinct query keys, and they compose:

- **`/` filters** by a case-insensitive substring of `owner/name`. Free, local,
  order-preserving.
- **`R` ranks** whatever survives the filter by relevance to a free-text intent,
  using the same scorer and the same digests as `spn forks list --query`.

So `/wayland` then `R "compositor protocol"` narrows to forks whose name mentions
wayland and orders those by how well their actual changes match the intent.

They are separate keys on purpose. A substring match costs nothing and preserves
the user's chosen sort column; ranking costs a network call and replaces the sort
column. Folding both into one key would hide a paid operation behind a free one,
and an intent that matches no `owner/name` would rank nothing even when it
describes the work well.

`R` ranks rather than hides: a relevance floor would differ per scorer and per
query, so dropping rows would lose forks with no way to tell. Enter applies, an
empty query clears, and Esc cancels the edit while leaving an active ranking
alone — matching how `/` behaves. Scoring happens on Enter, not per keystroke: a
network call per character would need debouncing and cancellation to show
rankings nobody asked for yet. Ranking only ever scores forks the filter admits,
so filtered-away rows are never paid for. The footer names the scorer, because a
Voyage ranking and a lexical one are not the same judgment.

## Building

```sh
go build ./cmd/spoon ./cmd/spn   # all features; no build tags
```

FastEmbed links ONNX Runtime; nothing needs an SDK at build time beyond cgo
(required for the libsql store and tree-sitter as well). Voyage adds no build
dependency — it is stdlib `net/http` and `encoding/json`.

## History

Earlier versions delegated embedding to external services (Ollama, a Python
sidecar serving arctic-embed-l-v2.0, or any OpenAI-compatible endpoint), then to
an in-process OpenVINO encoder. Both are gone — as are the later OpenVINO
reranker and label-polisher features: the local semantic embedder is fastembed
(a fixed BGE model over ONNX Runtime), which gives stable, cross-run-comparable
vectors — the prerequisite for the persistent index and `spn search`. The
built-in lexical embedder remains as the zero-setup clustering fallback and
default `--query` scorer. The evaluation that informed earlier model choices is
preserved under `experiments/started/behavioral-embeddings/`.

Voyage AI (2026-08) reintroduces an external provider, deliberately and on
different terms than the removals above. Those backends were *replacements* for
the local embedder, which made the whole semantic index depend on a reachable
service; Voyage is *additive*, so fastembed remains the default and the
zero-configuration path is unchanged. It also fills the `QueryScorer` seam that
survived the OpenVINO purge — `spoon` had no reranker at all between `19dd9f5`
and this change. The two motivating gaps: `voyage-code-3` is trained for code
retrieval and `fast-bge-small-en-v1.5` is not, and a cross-encoder can separate
candidates that a bi-encoder scores identically.

To compare Voyage against the local baselines on the 53 hand-labeled intent
pairs:

```sh
SPOON_EVAL_EMBEDDERS="builtin,voyage" VOYAGE_AI_API_KEY=... \
  go test -run TestEvalEmbedders_Manual -v ./internal/embed/
```

(There is no in-process `fastembed` leg: constructing one reaches fastembed-go's
tokenizer, which panics from inside its own goroutines when the model cache is
not fully usable — unrecoverable from this package, so it would abort the test
binary instead of reporting a skipped leg. Compare the FastEmbed baseline through
`spn search` output instead.)
