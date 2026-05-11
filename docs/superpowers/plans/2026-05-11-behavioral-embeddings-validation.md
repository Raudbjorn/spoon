# Behavioral Embeddings — Validation Experiment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run the gate experiment from `docs/superpowers/specs/future/future-work-behavioral-embeddings.md` to decide whether the CodeExecutor sidecar feature is worth building. Produce a reproducible harness + a committed `RESULTS.md` with go/no-go conclusion.

**Architecture:** Pure experiment scaffolding under `experiments/behavioral-embeddings/`. A small Go program reuses spoon's existing `forge` + `embed.BuildFeatures` pipeline to materialize features for the top-N forks of `IBM/mcp-context-forge` and dumps them as JSON. Two Python scripts embed those features via Ollama (nomic-embed-text) and local HuggingFace transformers (CodeExecutor). A third Python script computes pair-wise cosine similarity, compares each embedder's pair ranking against a hand-curated judgment file, and emits Kendall's tau plus the gate decision.

**Tech Stack:** Go 1.26+ (for `dump_features`), Python 3.11+ via `uv` (for embedding/analysis), HuggingFace `transformers` + `torch`, Ollama running locally with `nomic-embed-text` already pulled. No new spoon dependencies. No production code touched outside `experiments/`.

**Scope (split from full spec):** This plan covers **Phase A — validation experiment only**. The sidecar implementation (`embed/sidecar/`, `internal/embed/sidecar.go`, bootstrap wiring, README, Dockerfile, perf-test) is deliberately deferred to a follow-up plan that will be written **only if** this experiment's Kendall's tau improvement clears the **≥ 0.05** threshold required by the spec's "How to complete" §1.

**Gate criteria (verbatim from spec):**
- Strict ≥ 0.05 Kendall's tau improvement over nomic-embed-text → Phase B greenlit.
- < 0.05 → feature rejected. Phase B plan is **not** written; this branch's RESULTS.md becomes the final record.

---

## File Structure

Everything goes under a single experiments tree, isolated from production code:

```
experiments/
  behavioral-embeddings/
    README.md                            (Task 1) — methodology, layout, reproduce steps
    .gitignore                           (Task 1) — .venv/, __pycache__/, HF cache
    requirements.txt                     (Task 2) — pinned Python deps
    cmd/dump_features/
      main.go                            (Task 3) — flag-driven entrypoint
      dump.go                            (Task 3) — testable dumpFeatures()
      dump_test.go                       (Task 3) — unit tests with a stub forge.Forge
    features.json                        (Task 4) — committed fixture; ~1 MB
    judgments.json                       (Task 5) — committed; 40 hand-labeled pairs
    embed_common.py                      (Task 6) — build_text() shared by both embedders
    embed_common_test.py                 (Task 6) — pytest for build_text
    embed_nomic.py                       (Task 6) — Ollama-side embedding
    embed_codeexecutor.py                (Task 7) — local HF embedding
    nomic_vectors.json                   (Task 6/Task 10 run output) — gitignored
    codeexecutor_vectors.json            (Task 7/Task 10 run output) — gitignored
    analyze.py                           (Task 8) — cosine + Kendall's tau + gate check
    analyze_test.py                      (Task 8) — pytest for the math
    run.sh                               (Task 9) — orchestrator
    RESULTS.md                           (Task 10) — committed final report
```

The `*_vectors.json` artifacts are added to `.gitignore` because they're ~5–20 MB each, regenerable, and would noise diffs. `features.json` and `judgments.json` are committed so the experiment is reproducible without re-paying the GitHub API cost or re-doing the labeling.

---

## Task 1: Scaffold the experiment directory and methodology doc

**Files:**
- Create: `experiments/behavioral-embeddings/README.md`
- Create: `experiments/behavioral-embeddings/.gitignore`

- [ ] **Step 1: Create the directory**

```bash
mkdir -p experiments/behavioral-embeddings/cmd/dump_features
```

- [ ] **Step 2: Write the README**

Write `experiments/behavioral-embeddings/README.md`:

```markdown
# Behavioral Embeddings — Validation Experiment

Gate experiment for the deferred behavioral-embeddings feature
(`docs/superpowers/specs/future/future-work-behavioral-embeddings.md`).

## Question

Does swapping nomic-embed-text for CodeExecutor produce embeddings that rank
fork-pair similarity more like a human does?

## Method

1. `cmd/dump_features` runs the existing spoon pipeline over the top-200 forks
   of `IBM/mcp-context-forge` and dumps each fork's `embed.ForkFeatures` as
   JSON to `features.json`.
2. `judgments.json` records 40 hand-curated fork pairs: 20 labeled `1`
   (functionally same intent) and 20 labeled `0` (functionally different).
3. `embed_nomic.py` hits the local Ollama `/api/embeddings` endpoint once per
   fork and writes `nomic_vectors.json`.
4. `embed_codeexecutor.py` loads `microsoft/codeexecutor` via the HuggingFace
   transformers library and writes `codeexecutor_vectors.json`.
5. `analyze.py` computes pair-wise cosine similarity for each embedder,
   computes Kendall's tau against the hand judgments, prints both taus and
   the delta, and writes `RESULTS.md`.

## Gate

The feature is greenlit iff `tau(codeexecutor) - tau(nomic) >= 0.05`.

## Reproduce

```sh
cd experiments/behavioral-embeddings
./run.sh
```

Requires:
- Ollama running on `http://localhost:11434` with `nomic-embed-text` pulled.
- `gh auth status` showing an authenticated user (for `dump_features`).
- Python 3.11+ and `uv` on PATH.
- ~4 GB free RAM and ~1 GB free disk for the model cache.
```

- [ ] **Step 3: Write the .gitignore**

Write `experiments/behavioral-embeddings/.gitignore`:

```
.venv/
__pycache__/
*.pyc
nomic_vectors.json
codeexecutor_vectors.json
hf_cache/
```

- [ ] **Step 4: Commit**

```bash
git add experiments/behavioral-embeddings/README.md experiments/behavioral-embeddings/.gitignore
git commit -m "experiments: scaffold behavioral-embeddings validation harness"
```

---

## Task 2: Pin Python dependencies

**Files:**
- Create: `experiments/behavioral-embeddings/requirements.txt`

- [ ] **Step 1: Write requirements.txt**

Write `experiments/behavioral-embeddings/requirements.txt`:

```
torch==2.5.1
transformers==4.46.2
numpy==2.1.3
scipy==1.14.1
requests==2.32.3
pytest==8.3.4
```

Versions are recent stable releases as of 2026-05; if `uv pip install` reports a wheel-availability problem on this machine, bump only the offending package and note it in RESULTS.md.

- [ ] **Step 2: Create the virtualenv to confirm it resolves**

```bash
cd experiments/behavioral-embeddings && uv venv .venv && uv pip install --python .venv/bin/python -r requirements.txt
```

Expected: terminal prints `Installed N packages` with no resolution error.

- [ ] **Step 3: Commit**

```bash
git add experiments/behavioral-embeddings/requirements.txt
git commit -m "experiments: pin python deps for behavioral-embeddings"
```

---

## Task 3: Build the feature-dump Go helper (test-first)

**Files:**
- Create: `experiments/behavioral-embeddings/cmd/dump_features/dump.go`
- Create: `experiments/behavioral-embeddings/cmd/dump_features/dump_test.go`
- Create: `experiments/behavioral-embeddings/cmd/dump_features/main.go`

The helper uses the existing `forge.Forge` interface (`internal/forge/types.go:163`), `embed.BuildFeatures` (`internal/embed/features.go:27`), and the GitHub provider constructor `gh.NewGHProvider` (used the same way in `cmd/spn/forks.go:48`).

The testable seam is a `dumpFeatures(ctx, provider forge.Forge, owner, name string, topN int) ([]ForkRecord, error)` function. The test stubs `forge.Forge` with a fake that returns canned `ListForks` / `Compare` results.

- [ ] **Step 1: Write the failing test**

Write `experiments/behavioral-embeddings/cmd/dump_features/dump_test.go`:

```go
package main

import (
	"context"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

type stubForge struct {
	parent  forge.ParentData
	forks   []forge.T1Data
	compare map[string]forge.T2Data
}

func (s *stubForge) Auth(_ context.Context) (forge.AuthInfo, error) {
	return forge.AuthInfo{}, nil
}

func (s *stubForge) Parent(_ context.Context, _, _ string) (forge.ParentData, error) {
	return s.parent, nil
}

func (s *stubForge) ListForks(_ context.Context, _, _ string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg, len(s.forks))
	for _, f := range s.forks {
		ch <- forge.ForkMsg{Fork: f}
	}
	close(ch)
	return ch, nil
}

func (s *stubForge) Compare(_ context.Context, fork forge.T1Data, _ string) (forge.T2Data, error) {
	return s.compare[fork.ID], nil
}

func (s *stubForge) Contributors(_ context.Context, _ forge.T1Data) (forge.T3Data, error) {
	return forge.T3Data{}, nil
}

func TestDumpFeatures_TopN(t *testing.T) {
	sf := &stubForge{
		forks: []forge.T1Data{
			{ID: "a", Owner: "x", Name: "r1"},
			{ID: "b", Owner: "y", Name: "r2"},
			{ID: "c", Owner: "z", Name: "r3"},
		},
		compare: map[string]forge.T2Data{
			"a": {Diffs: []forge.FileDiff{{Path: "main.go", Additions: 1, Deletions: 0}}},
			"b": {Diffs: []forge.FileDiff{{Path: "README.md", Additions: 2, Deletions: 0}}},
			"c": {Diffs: []forge.FileDiff{{Path: "go.mod", Additions: 3, Deletions: 0}}},
		},
	}
	got, err := dumpFeatures(context.Background(), sf, "owner", "repo", 2)
	if err != nil {
		t.Fatalf("dumpFeatures: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 records, got %d", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("unexpected order: %v", got)
	}
	if got[0].Features.Paths != "main.go" {
		t.Errorf("Paths not populated: %q", got[0].Features.Paths)
	}
}

func TestDumpFeatures_SkipsCompareErrors(t *testing.T) {
	sf := &stubForgeWithErr{
		stubForge: stubForge{
			forks:   []forge.T1Data{{ID: "ok", Owner: "x", Name: "r"}, {ID: "bad", Owner: "y", Name: "r"}},
			compare: map[string]forge.T2Data{"ok": {Diffs: []forge.FileDiff{{Path: "f", Additions: 1}}}},
		},
		failIDs: map[string]bool{"bad": true},
	}
	got, err := dumpFeatures(context.Background(), sf, "owner", "repo", 0)
	if err != nil {
		t.Fatalf("dumpFeatures: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ok" {
		t.Errorf("want 1 record with id=ok, got %v", got)
	}
}

type stubForgeWithErr struct {
	stubForge
	failIDs map[string]bool
}

func (s *stubForgeWithErr) Compare(ctx context.Context, fork forge.T1Data, branch string) (forge.T2Data, error) {
	if s.failIDs[fork.ID] {
		return forge.T2Data{}, context.Canceled
	}
	return s.stubForge.Compare(ctx, fork, branch)
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./experiments/behavioral-embeddings/cmd/dump_features/...
```

Expected: FAIL — `undefined: dumpFeatures` and `undefined: ForkRecord`.

- [ ] **Step 3: Implement dump.go**

Write `experiments/behavioral-embeddings/cmd/dump_features/dump.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
)

// ForkRecord is one fork's payload in features.json. Stable JSON shape — the
// Python side depends on the field names.
type ForkRecord struct {
	ID       string             `json:"id"`
	Owner    string             `json:"owner"`
	Name     string             `json:"name"`
	URL      string             `json:"url"`
	Stars    int                `json:"stars"`
	Features embed.ForkFeatures `json:"features"`
}

// dumpFeatures enumerates forks via p.ListForks, calls p.Compare per fork,
// and builds embed.ForkFeatures with the same parameters spoon uses
// (default 4 KB diff cap, no README). Compare failures are logged to stderr
// and skipped. topN<=0 means no cap.
func dumpFeatures(ctx context.Context, p forge.Forge, owner, repo string, topN int) ([]ForkRecord, error) {
	ch, err := p.ListForks(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("list forks: %w", err)
	}
	var t1s []forge.T1Data
	for msg := range ch {
		if msg.Err != nil {
			continue
		}
		t1s = append(t1s, msg.Fork)
		if topN > 0 && len(t1s) >= topN {
			break
		}
	}
	out := make([]ForkRecord, 0, len(t1s))
	for _, t1 := range t1s {
		t2, err := p.Compare(ctx, t1, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "compare %s/%s: %v\n", t1.Owner, t1.Name, err)
			continue
		}
		out = append(out, ForkRecord{
			ID:       t1.ID,
			Owner:    t1.Owner,
			Name:     t1.Name,
			URL:      t1.URL,
			Stars:    t1.Stars,
			Features: embed.BuildFeatures(t2, "", 0),
		})
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./experiments/behavioral-embeddings/cmd/dump_features/...
```

Expected: PASS — both `TestDumpFeatures_TopN` and `TestDumpFeatures_SkipsCompareErrors`.

If `stubForge` fails to satisfy `forge.Forge` (signature mismatch), open `internal/forge/types.go` and copy the method signatures verbatim; the interface there is the source of truth.

- [ ] **Step 5: Write main.go**

Write `experiments/behavioral-embeddings/cmd/dump_features/main.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	gh "github.com/svnbjrn/spoon/internal/github"
)

func main() {
	repo := flag.String("repo", "", "owner/name of upstream repository")
	topN := flag.Int("n", 200, "max forks to dump (heat order from ListForks)")
	outPath := flag.String("out", "", "output JSON path (default: stdout)")
	flag.Parse()

	if *repo == "" {
		fmt.Fprintln(os.Stderr, "usage: dump_features -repo OWNER/NAME [-n N] [-out PATH]")
		os.Exit(2)
	}
	owner, name, err := splitRepo(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	client, status, cerr := gh.CheckAuth()
	if cerr != nil {
		fmt.Fprintln(os.Stderr, "github auth:", cerr)
		os.Exit(1)
	}
	provider := gh.NewGHProvider(client, status)

	records, err := dumpFeatures(context.Background(), provider, owner, name, *topN)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dump:", err)
		os.Exit(1)
	}

	w := os.Stdout
	if *outPath != "" {
		f, ferr := os.Create(*outPath)
		if ferr != nil {
			fmt.Fprintln(os.Stderr, "create:", ferr)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(records); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
}

func splitRepo(s string) (string, string, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("expected OWNER/NAME")
	}
	return parts[0], parts[1], nil
}
```

- [ ] **Step 6: Verify build**

```bash
go build ./experiments/behavioral-embeddings/cmd/dump_features
```

Expected: successful build, no compile errors.

- [ ] **Step 7: Commit**

```bash
git add experiments/behavioral-embeddings/cmd/dump_features/
git commit -m "experiments: add dump_features helper for behavioral-embeddings validation"
```

---

## Task 4: Materialize features.json from IBM/mcp-context-forge

**Files:**
- Create: `experiments/behavioral-embeddings/features.json` (~1 MB; committed as fixture)

- [ ] **Step 1: Verify gh auth**

```bash
gh auth status
```

Expected: shows an authenticated github.com account.

- [ ] **Step 2: Run the helper**

```bash
cd experiments/behavioral-embeddings && go run ./cmd/dump_features -repo IBM/mcp-context-forge -n 200 -out features.json
```

Expected: stderr may print a handful of `compare ...:` lines for forks where the compare API fails; stdout is silent. `features.json` should exist and be 0.5–2 MB.

If the run takes much longer than ~5 minutes, the GitHub rate limiter may be throttling; let it run. If it errors out with `rate limited`, wait for the reset and re-run with a smaller `-n` (e.g., 100) and note the reduced sample size in RESULTS.md.

- [ ] **Step 3: Sanity-check the output**

```bash
jq 'length' experiments/behavioral-embeddings/features.json
jq '.[0] | keys' experiments/behavioral-embeddings/features.json
jq '.[0].features | keys' experiments/behavioral-embeddings/features.json
```

Expected:
- A number ≥ 30 (some forks may have failed compare and been skipped).
- `["features", "id", "name", "owner", "stars", "url"]`.
- `["Commits", "DiffChunk", "Paths", "ReadmeDoc"]`.

- [ ] **Step 4: Commit**

```bash
git add experiments/behavioral-embeddings/features.json
git commit -m "experiments: dump IBM/mcp-context-forge features fixture"
```

---

## Task 5: Hand-curate the judgments file

**Files:**
- Create: `experiments/behavioral-embeddings/judgments.json` (40 entries)

This task is the only manual step. Plan content shows the file format and one fully-worked-out example pair so the curator has zero ambiguity about the shape. The remaining 39 entries are produced by inspecting fork pairs in `features.json` and on GitHub.

- [ ] **Step 1: Write the initial judgments.json with one example entry**

Write `experiments/behavioral-embeddings/judgments.json`:

```json
[
  {
    "a": "<fork-id-from-features.json>",
    "b": "<fork-id-from-features.json>",
    "label": 1,
    "rationale": "Both forks add the same OAuth provider, differing only in HTTP client library."
  }
]
```

- [ ] **Step 2: Document the curation procedure**

Append to `experiments/behavioral-embeddings/README.md`:

```markdown

## Curating judgments.json

Open `features.json` and `https://github.com/IBM/mcp-context-forge/network`
side-by-side. Pick 40 pairs:

- **20 with `label: 1` (functionally same intent):** Pairs where both forks
  appear to be making the same kind of change. Common patterns: same plugin
  added, same bug fixed, same provider integrated. Look for overlapping
  paths/commits in `features.json` as a first filter, then confirm by reading
  each fork's commit history on GitHub.
- **20 with `label: 0` (functionally different intent):** Pairs where both
  forks have non-trivial changes but the changes target different things.
  E.g., one adds metrics, the other adds OAuth.

Rules:
- A fork may appear in at most two pairs (one same-label, one different-label).
  Forces breadth.
- Skip forks whose `DiffChunk` is empty in `features.json` — there's no
  signal to embed.
- Record the rationale field as one short sentence — enough to defend the
  label without writing a paragraph.

The labels are the **ground truth** the experiment measures both embedders
against. Be conservative: if intent is genuinely ambiguous after 30 seconds
of reading the diff, skip the pair.
```

- [ ] **Step 3: Hand-curate the 40 pairs**

This step is human work, not automatable. Output: `judgments.json` filled with 40 entries (20 with `"label": 1`, 20 with `"label": 0`).

Acceptance: `jq 'length' experiments/behavioral-embeddings/judgments.json` returns `40`, `jq '[.[] | select(.label==1)] | length' …` returns `20`, and same for `label==0`.

- [ ] **Step 4: Commit**

```bash
git add experiments/behavioral-embeddings/judgments.json experiments/behavioral-embeddings/README.md
git commit -m "experiments: hand-curate fork-pair judgments for behavioral-embeddings"
```

---

## Task 6: Implement build_text() and Ollama embedding script

**Files:**
- Create: `experiments/behavioral-embeddings/embed_common.py`
- Create: `experiments/behavioral-embeddings/embed_common_test.py`
- Create: `experiments/behavioral-embeddings/embed_nomic.py`

The two embedders share the input-text construction: both consume `ForkFeatures` (paths/commits/readmeDoc/diffChunk) and need to turn them into one or more strings. We mirror spoon's `codeAwareEmbed` path from `internal/embed/multimodal.go:53` — a single concatenated prompt with structural tags — since both embedders are single-vector models and we want the most direct comparison.

- [ ] **Step 1: Write the failing test for build_text**

Write `experiments/behavioral-embeddings/embed_common_test.py`:

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

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd experiments/behavioral-embeddings && .venv/bin/python -m pytest embed_common_test.py -v
```

Expected: FAIL — `ModuleNotFoundError: No module named 'embed_common'`.

- [ ] **Step 3: Implement embed_common.py**

Write `experiments/behavioral-embeddings/embed_common.py`:

```python
"""Shared helpers for both embedding scripts."""
import json
from typing import Any


def build_text(features: dict[str, str]) -> str:
    """Concatenate the four ForkFeatures modalities with structural tags.

    Mirrors spoon's codeAwareEmbed path (internal/embed/multimodal.go:53)
    so the experiment exercises the same input layout the production
    pipeline would use.
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

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd experiments/behavioral-embeddings && .venv/bin/python -m pytest embed_common_test.py -v
```

Expected: 3 passed.

- [ ] **Step 5: Implement embed_nomic.py**

Write `experiments/behavioral-embeddings/embed_nomic.py`:

```python
"""Embed every fork in features.json via Ollama-served nomic-embed-text."""
import argparse
import json
import sys

import requests

from embed_common import build_text, load_features


def embed_one(endpoint: str, model: str, text: str) -> list[float]:
    r = requests.post(
        f"{endpoint.rstrip('/')}/api/embeddings",
        json={"model": model, "prompt": text},
        timeout=60,
    )
    r.raise_for_status()
    data = r.json()
    if "error" in data and data["error"]:
        raise RuntimeError(f"ollama error: {data['error']}")
    vec = data.get("embedding") or []
    if not vec:
        raise RuntimeError(f"empty embedding from {endpoint}/{model}")
    return vec


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--features", default="features.json")
    ap.add_argument("--out", default="nomic_vectors.json")
    ap.add_argument("--endpoint", default="http://localhost:11434")
    ap.add_argument("--model", default="nomic-embed-text")
    args = ap.parse_args()

    feats = load_features(args.features)
    out: dict[str, list[float]] = {}
    for i, (fid, rec) in enumerate(feats.items(), start=1):
        text = build_text(rec["features"])
        try:
            out[fid] = embed_one(args.endpoint, args.model, text)
        except Exception as e:
            print(f"[{i}/{len(feats)}] skip {fid}: {e}", file=sys.stderr)
            continue
        if i % 25 == 0:
            print(f"[{i}/{len(feats)}] embedded", file=sys.stderr)

    with open(args.out, "w") as f:
        json.dump(out, f)
    print(f"wrote {len(out)} vectors to {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
```

- [ ] **Step 6: Smoke-test embed_nomic against Ollama**

Pre-req: Ollama running locally with `nomic-embed-text` already pulled (`ollama list` should show it).

```bash
cd experiments/behavioral-embeddings && .venv/bin/python embed_nomic.py --features features.json --out /tmp/nomic_smoke.json
```

Expected: progress lines on stderr every 25 forks; final `wrote N vectors to /tmp/nomic_smoke.json` where N is within a few of `jq 'length' features.json`. Smoke output is not committed.

```bash
jq 'keys | length' /tmp/nomic_smoke.json
jq 'to_entries[0].value | length' /tmp/nomic_smoke.json
```

Expected: matches the embedded count; per-vector length is 768 for nomic-embed-text.

- [ ] **Step 7: Commit**

```bash
git add experiments/behavioral-embeddings/embed_common.py experiments/behavioral-embeddings/embed_common_test.py experiments/behavioral-embeddings/embed_nomic.py
git commit -m "experiments: add nomic-embed-text embedding script"
```

---

## Task 7: Implement the CodeExecutor embedding script

**Files:**
- Create: `experiments/behavioral-embeddings/embed_codeexecutor.py`

CodeExecutor (`microsoft/codeexecutor` on HuggingFace) is a UniXcoder-derivative; we mean-pool the last hidden state to get a single fixed-length vector per input. The spec calls for batching up to 32 inputs per forward pass.

- [ ] **Step 1: Implement embed_codeexecutor.py**

Write `experiments/behavioral-embeddings/embed_codeexecutor.py`:

```python
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


def embed_batch(model, tokenizer, texts: list[str], device: str) -> list[list[float]]:
    enc = tokenizer(
        texts,
        padding=True,
        truncation=True,
        max_length=512,
        return_tensors="pt",
    ).to(device)
    with torch.no_grad():
        out = model(**enc)
    hidden = out.last_hidden_state  # [B, T, H]
    mask = enc["attention_mask"].unsqueeze(-1).to(hidden.dtype)  # [B, T, 1]
    summed = (hidden * mask).sum(dim=1)  # [B, H]
    counts = mask.sum(dim=1).clamp(min=1)  # [B, 1]
    pooled = summed / counts  # [B, H]
    return pooled.cpu().tolist()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--features", default="features.json")
    ap.add_argument("--out", default="codeexecutor_vectors.json")
    ap.add_argument("--model", default="microsoft/codeexecutor")
    ap.add_argument("--cache-dir", default="hf_cache")
    ap.add_argument("--batch-size", type=int, default=32)
    args = ap.parse_args()

    os.makedirs(args.cache_dir, exist_ok=True)
    device = "cuda" if torch.cuda.is_available() else "cpu"
    print(f"loading {args.model} on {device}", file=sys.stderr)
    tokenizer = AutoTokenizer.from_pretrained(args.model, cache_dir=args.cache_dir)
    model = AutoModel.from_pretrained(args.model, cache_dir=args.cache_dir).to(device)
    model.eval()

    feats = load_features(args.features)
    ids = list(feats.keys())
    out: dict[str, list[float]] = {}
    for i in range(0, len(ids), args.batch_size):
        batch_ids = ids[i : i + args.batch_size]
        batch_texts = [build_text(feats[bid]["features"]) for bid in batch_ids]
        vecs = embed_batch(model, tokenizer, batch_texts, device)
        for bid, v in zip(batch_ids, vecs):
            out[bid] = v
        print(f"[{i + len(batch_ids)}/{len(ids)}] embedded", file=sys.stderr)

    with open(args.out, "w") as f:
        json.dump(out, f)
    print(f"wrote {len(out)} vectors to {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
```

- [ ] **Step 2: Smoke-test embed_codeexecutor with a tiny features subset**

```bash
cd experiments/behavioral-embeddings && jq '.[0:3]' features.json > /tmp/feat3.json && .venv/bin/python embed_codeexecutor.py --features /tmp/feat3.json --out /tmp/ce_smoke.json --cache-dir ./hf_cache
```

Expected: first run downloads ~500 MB to `./hf_cache`, then prints `[3/3] embedded` and `wrote 3 vectors to /tmp/ce_smoke.json`. Second run uses the cache and is fast.

```bash
jq 'keys | length' /tmp/ce_smoke.json
jq 'to_entries[0].value | length' /tmp/ce_smoke.json
```

Expected: `3` and a hidden-state size in the 768-range (CodeExecutor's hidden dim).

If the model name `microsoft/codeexecutor` 404s on HuggingFace, search the Hub for the most current canonical name (the spec cites the Liu et al. 2023 paper; the release name may have shifted). Pin whatever name resolves into RESULTS.md.

- [ ] **Step 3: Commit**

```bash
git add experiments/behavioral-embeddings/embed_codeexecutor.py
git commit -m "experiments: add CodeExecutor HF embedding script"
```

---

## Task 8: Implement the analysis script (TDD)

**Files:**
- Create: `experiments/behavioral-embeddings/analyze.py`
- Create: `experiments/behavioral-embeddings/analyze_test.py`

The two math operations are: pair-wise cosine similarity, and Kendall's tau between two ranked lists. SciPy's `kendalltau` is the canonical implementation; we wrap it so the gate logic and the cosine math have unit tests.

- [ ] **Step 1: Write the failing test**

Write `experiments/behavioral-embeddings/analyze_test.py`:

```python
import pytest

from analyze import compute_gate, cosine, kendall_tau_vs_labels, pair_similarities


def test_cosine_identical_is_one():
    assert cosine([1.0, 0.0], [1.0, 0.0]) == pytest.approx(1.0)


def test_cosine_orthogonal_is_zero():
    assert cosine([1.0, 0.0], [0.0, 1.0]) == pytest.approx(0.0)


def test_cosine_zero_vector_is_zero():
    assert cosine([0.0, 0.0], [1.0, 1.0]) == 0.0


def test_pair_similarities_keys_off_judgments():
    vecs = {"a": [1.0, 0.0], "b": [1.0, 0.0], "c": [0.0, 1.0]}
    judgments = [{"a": "a", "b": "b", "label": 1}, {"a": "a", "b": "c", "label": 0}]
    sims = pair_similarities(vecs, judgments)
    assert sims == [pytest.approx(1.0), pytest.approx(0.0)]


def test_pair_similarities_skips_missing_vectors():
    vecs = {"a": [1.0, 0.0]}
    judgments = [{"a": "a", "b": "missing", "label": 1}]
    sims = pair_similarities(vecs, judgments)
    assert sims == [None]


def test_kendall_tau_perfect_agreement():
    # Sim-rank for label=1 pairs is strictly higher than for label=0 pairs.
    sims = [0.9, 0.8, 0.2, 0.1]
    labels = [1, 1, 0, 0]
    tau = kendall_tau_vs_labels(sims, labels)
    assert tau == pytest.approx(1.0)


def test_kendall_tau_perfect_disagreement():
    sims = [0.1, 0.2, 0.8, 0.9]
    labels = [1, 1, 0, 0]
    tau = kendall_tau_vs_labels(sims, labels)
    assert tau == pytest.approx(-1.0)


def test_compute_gate_passes_when_delta_ge_threshold():
    # nomic is perfectly wrong (tau=-1); CE is perfectly right (tau=+1).
    # Delta = 2.0, well above the 0.05 threshold.
    nomic_sims = [0.1, 0.2, 0.8, 0.9]
    ce_sims = [0.9, 0.8, 0.2, 0.1]
    labels = [1, 1, 0, 0]
    r = compute_gate(nomic_sims, ce_sims, labels, threshold=0.05)
    assert r["tau_nomic"] == pytest.approx(-1.0)
    assert r["tau_codeexecutor"] == pytest.approx(1.0)
    assert r["delta"] == pytest.approx(2.0)
    assert r["pass"] is True


def test_compute_gate_fails_when_delta_below_threshold():
    # Both embedders produce the same ranking, so delta is exactly 0.
    sims = [0.9, 0.8, 0.2, 0.1]
    labels = [1, 1, 0, 0]
    r = compute_gate(sims, sims, labels, threshold=0.05)
    assert r["delta"] == pytest.approx(0.0)
    assert r["pass"] is False


def test_compute_gate_skips_pairs_with_missing_sims():
    # First sim missing on the nomic side; that pair should drop out for both.
    nomic_sims = [None, 0.2, 0.8, 0.9]
    ce_sims = [0.9, 0.8, 0.2, 0.1]
    labels = [1, 1, 0, 0]
    r = compute_gate(nomic_sims, ce_sims, labels, threshold=0.05)
    assert r["n_pairs_kept"] == 3
    assert r["n_pairs_skipped"] == 1
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd experiments/behavioral-embeddings && .venv/bin/python -m pytest analyze_test.py -v
```

Expected: FAIL — `ModuleNotFoundError: No module named 'analyze'`.

- [ ] **Step 3: Implement analyze.py**

Write `experiments/behavioral-embeddings/analyze.py`:

```python
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
    tau, _p = kendalltau(sims, labels)
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
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd experiments/behavioral-embeddings && .venv/bin/python -m pytest analyze_test.py -v
```

Expected: all tests pass. If a `kendall_tau_vs_labels` test fails by a small numeric margin, inspect the fixture — Kendall's tau on a discrete-tie input is sensitive; the assertions use ranges generous enough for SciPy's variant choice.

- [ ] **Step 5: Commit**

```bash
git add experiments/behavioral-embeddings/analyze.py experiments/behavioral-embeddings/analyze_test.py
git commit -m "experiments: add cosine + Kendall's tau gate analysis"
```

---

## Task 9: Orchestrator script

**Files:**
- Create: `experiments/behavioral-embeddings/run.sh`

- [ ] **Step 1: Write run.sh**

Write `experiments/behavioral-embeddings/run.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

if [ ! -d .venv ]; then
  uv venv .venv
  uv pip install --python .venv/bin/python -r requirements.txt
fi

if [ ! -f features.json ]; then
  echo "features.json missing; run: go run ./cmd/dump_features -repo IBM/mcp-context-forge -n 200 -out features.json" >&2
  exit 2
fi

if [ ! -f judgments.json ]; then
  echo "judgments.json missing; see README.md for the curation procedure" >&2
  exit 2
fi

.venv/bin/python embed_nomic.py --features features.json --out nomic_vectors.json
.venv/bin/python embed_codeexecutor.py --features features.json --out codeexecutor_vectors.json
.venv/bin/python analyze.py --features features.json --judgments judgments.json \
  --nomic nomic_vectors.json --codeexecutor codeexecutor_vectors.json --out RESULTS.md
echo "wrote RESULTS.md"
```

- [ ] **Step 2: Make it executable**

```bash
chmod +x experiments/behavioral-embeddings/run.sh
```

- [ ] **Step 3: Commit**

```bash
git add experiments/behavioral-embeddings/run.sh
git commit -m "experiments: add behavioral-embeddings orchestrator"
```

---

## Task 10: Run the experiment, commit RESULTS.md, decide

**Files:**
- Create: `experiments/behavioral-embeddings/RESULTS.md`

This task closes the loop. It is the only task where the *content* of an output file depends on a run that nobody has executed yet.

- [ ] **Step 1: Execute the full pipeline**

```bash
cd experiments/behavioral-embeddings && ./run.sh
```

Expected duration:
- `embed_nomic.py`: a few minutes (one Ollama call per fork; ~200 forks at ~1 s each).
- `embed_codeexecutor.py`: 1–8 minutes for the model download on first run, then 5–30 minutes for inference depending on CPU/GPU.
- `analyze.py`: subsecond.

If a phase fails, fix it before continuing. Common failure modes:
- Ollama not running → start it (`ollama serve`) or pull the model (`ollama pull nomic-embed-text`).
- CodeExecutor model name 404 → look up the current name on HuggingFace and pass `--model <name>` to `embed_codeexecutor.py`.
- OOM on CodeExecutor → lower `--batch-size` (16, then 8).

- [ ] **Step 2: Inspect RESULTS.md**

```bash
cat experiments/behavioral-embeddings/RESULTS.md
```

Expected: a markdown table with both taus, the delta, and either a PASS or FAIL verdict.

- [ ] **Step 3: Sanity-check the numbers**

If `tau_nomic` and `tau_codeexecutor` are both at or near 0, the judgments and the vectors disagree at random — that usually means a key mismatch (vector keys don't line up with judgment IDs). Re-check `pair_similarities` skip count in the JSON dump; if `n_pairs_skipped` is large, the judgments reference fork IDs that aren't in features.json.

If both taus are very high (≥ 0.9), the judgments may be trivial — pairs picked with too-obvious labels. The spec's accept criterion (≥ 0.15 improvement) implies the field is meant to be challenging; ≥ 0.05 is the bare-minimum gate.

- [ ] **Step 4: Commit RESULTS.md**

```bash
git add experiments/behavioral-embeddings/RESULTS.md
git commit -m "experiments: behavioral-embeddings validation results"
```

- [ ] **Step 5: Surface the decision**

If `pass: true` in the JSON output of `analyze.py`:

> Open a follow-up plan at `docs/superpowers/plans/YYYY-MM-DD-behavioral-embeddings-sidecar.md`
> covering the remaining spec items (Python sidecar, internal/embed/sidecar.go,
> bootstrap wiring, fallback logic, --embedder-backend flag, README docs,
> Dockerfile, perf test on ≥ 200 forks).

If `pass: false`:

> Append a one-paragraph postmortem to `experiments/behavioral-embeddings/RESULTS.md`
> explaining why the gate did not clear, and update the spec's status header
> from "Deferred from v1" to "Rejected after gate experiment on YYYY-MM-DD".

- [ ] **Step 6: Open a PR or merge to main**

```bash
git push -u origin behavioral-embeddings-validation
gh pr create --title "Behavioral embeddings: gate experiment + decision" --body "$(cat <<'EOF'
## Summary
- Added the validation harness under `experiments/behavioral-embeddings/`.
- Ran the gate experiment on `IBM/mcp-context-forge` with 40 hand-curated pairs.
- Decision in `experiments/behavioral-embeddings/RESULTS.md`.

## Test plan
- [ ] `go test ./experiments/...` passes
- [ ] `pytest experiments/behavioral-embeddings/` passes
- [ ] RESULTS.md is committed with both tau values and a clear verdict
EOF
)"
```

---

## Self-Review Notes

This plan was self-reviewed against the spec on 2026-05-11:

- **Spec coverage:** "How to complete" §1 (validation experiment first) is covered by Tasks 1–10. §§2–6 (sidecar build, Go wrapper, bootstrap wiring, docs, perf test) are explicitly deferred to a follow-up plan gated on Task 10's outcome.
- **Placeholder scan:** No "TBD" / "implement later" / "fill in details" left in the body. The judgments.json content (Task 5) is intentionally hand-curated by a human; the plan shows the exact file format and one example row.
- **Type consistency:** `ForkRecord` in `dump.go` is the only Go type defined; the Python side reads `features.json` as `dict[str, Any]` and is decoupled from the Go struct shape (it pulls by key name). `vectors.json` shape is `{fork_id: list[float]}` in both `embed_nomic.py` and `embed_codeexecutor.py`. `analyze.py`'s `pair_similarities` consumes that exact shape.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-11-behavioral-embeddings-validation.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
