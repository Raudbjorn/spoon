# Behavioral Embeddings — Phase B Sidecar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a production sidecar that lets spoon's clustering pipeline use `Snowflake/snowflake-arctic-embed-l-v2.0` as an alternate embedder, with graceful fallback to Ollama when the sidecar is unavailable.

**Architecture:** A Python FastAPI sidecar process loads the model once via sentence-transformers (which handles the model's required CLS pooling and query-prefix conventions); spoon-side Go code (`internal/embed/sidecar.go`) speaks HTTP to that process and implements the existing `Embedder` interface. The cluster pipeline doesn't need to know about the model swap — `SelectEmbedder` returns a `SidecarEmbedder` when configured, an `OllamaClient` otherwise.

**Tech Stack:** Go 1.26+ (sidecar wrapper + process management), Python 3.11+ (FastAPI server + sentence-transformers + torch), Docker (optional packaged distribution). No new direct dependencies for spoon's binary — sidecar is opt-in.

**Pre-requisite:** Phase B authorized by `experiments/started/behavioral-embeddings/RESULTS_PANEL.md` (Δ = +0.1196 for arctic-l-v2 vs nomic baseline at 53 hand-curated pairs).

**Scope:**
- In: sidecar implementation, Go wrapper, bootstrap routing, Docker package, README documentation, perf benchmark.
- Out: changing the v1 default embedder (Ollama nomic stays the default); changing the cluster pipeline interface; CRAVE/MULocBench validation (separate plans).

---

## File Structure

```
embed/sidecar/
  server.py            NEW  — FastAPI app with /embed and /health
  requirements.txt     NEW  — torch, transformers, sentence-transformers, fastapi, uvicorn
  Dockerfile           NEW  — container image with the model pre-pulled
  README.md            NEW  — quickstart for running the sidecar locally or via docker

internal/embed/
  sidecar.go           NEW  — SidecarEmbedder type + process management
  sidecar_test.go      NEW  — unit tests against an httptest mock
  bootstrap.go         MODIFY  — branch on SPOON_EMBEDDER_BACKEND / --embedder-backend
  bootstrap_test.go    MODIFY  — add tests for the new branch

cmd/spn/forks.go       MODIFY  — surface --embedder-backend flag
cmd/spoon/main.go      MODIFY  — same surface in the TUI entrypoint

docs/
  embedders.md         NEW  — user docs explaining the sidecar option
```

Roughly 12 commits across 10 tasks.

---

## Task 1: License check + record

**Files:** none yet (note for the eventual `embed/sidecar/README.md` in Task 8).

The chosen model is `Snowflake/snowflake-arctic-embed-l-v2.0`, Apache 2.0 licensed (verified via HF model card on 2026-05-13). For Phase B's distribution, we need to confirm Apache 2.0 is compatible with spoon's own license. Spoon does not appear to have a top-level LICENSE file at the time of writing.

- [ ] **Step 1: Verify spoon's license**

```bash
ls /home/svnbjrn/projects/spoon/spoon-2/LICENSE* 2>/dev/null
git log --all --diff-filter=A -- LICENSE 2>/dev/null | head -3
```

If a LICENSE exists, read it and confirm Apache 2.0 model weights are redistributable. If no LICENSE exists, this is a separate concern — Apache 2.0 model weights don't impose a license on the spoon repo itself, but spoon's lack of a clear license is a downstream user friction. **Out of scope for this plan but flag it.**

- [ ] **Step 2: Confirm Apache 2.0 compatibility**

Apache 2.0 model weights can be redistributed in any open-source or proprietary product, with attribution. The sidecar's Dockerfile (Task 7) should bake in the attribution; the README (Task 8) should cite the model's HF card and Apache 2.0 license.

No commit for this task — informational only. Carry forward to Task 7 and Task 8.

---

## Task 2: Python sidecar server (FastAPI + sentence-transformers)

**Files:**
- Create: `embed/sidecar/server.py`
- Create: `embed/sidecar/requirements.txt`

The server exposes `POST /embed` (input `{"texts": [...]}` → output `{"vectors": [[...]]}`) and `GET /health`. The model is loaded once at startup; embedding is performed via sentence-transformers' `encode` which handles arctic's CLS pooling and L2 normalization automatically.

- [ ] **Step 1: Write requirements.txt**

Create `embed/sidecar/requirements.txt`:

```
torch==2.9.0
transformers==4.47.1
sentence-transformers==3.3.1
fastapi==0.115.6
uvicorn[standard]==0.34.0
einops==0.8.2
```

`einops` is required by the underlying nomic-bert-style implementation arctic uses internally.

- [ ] **Step 2: Write server.py**

Create `embed/sidecar/server.py`:

```python
"""Behavioral-embeddings sidecar for spoon.

Loads Snowflake/snowflake-arctic-embed-l-v2.0 via sentence-transformers
(which handles the model's CLS-pooling and L2-normalization conventions
automatically), and exposes POST /embed + GET /health.
"""
import logging
import os
import sys
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from sentence_transformers import SentenceTransformer

DEFAULT_MODEL = "Snowflake/snowflake-arctic-embed-l-v2.0"
MODEL_NAME = os.environ.get("SPOON_SIDECAR_MODEL", DEFAULT_MODEL)
DEVICE = "cuda" if os.environ.get("SPOON_SIDECAR_DEVICE", "cpu") == "cuda" else "cpu"

logger = logging.getLogger("spoon.sidecar")
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")

_model: SentenceTransformer | None = None


@asynccontextmanager
async def lifespan(app: FastAPI):
    global _model
    logger.info(f"loading {MODEL_NAME} on {DEVICE}")
    _model = SentenceTransformer(MODEL_NAME, device=DEVICE, trust_remote_code=True)
    logger.info(f"loaded; dim={_model.get_sentence_embedding_dimension()}")
    yield
    _model = None
    logger.info("shutdown")


app = FastAPI(title="spoon-behavioral-embeddings-sidecar", lifespan=lifespan)


class EmbedRequest(BaseModel):
    texts: list[str]


class EmbedResponse(BaseModel):
    vectors: list[list[float]]
    dim: int


@app.get("/health")
def health() -> dict[str, Any]:
    if _model is None:
        raise HTTPException(status_code=503, detail="model not loaded")
    return {
        "status": "ok",
        "model": MODEL_NAME,
        "device": DEVICE,
        "dim": _model.get_sentence_embedding_dimension(),
    }


@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest) -> EmbedResponse:
    if _model is None:
        raise HTTPException(status_code=503, detail="model not loaded")
    if not req.texts:
        return EmbedResponse(vectors=[], dim=_model.get_sentence_embedding_dimension())
    vecs = _model.encode(
        req.texts,
        batch_size=16,
        show_progress_bar=False,
        normalize_embeddings=True,
        convert_to_numpy=False,
    )
    return EmbedResponse(
        vectors=[v.tolist() for v in vecs],
        dim=_model.get_sentence_embedding_dimension(),
    )
```

- [ ] **Step 3: Local smoke test (without uvicorn fully running)**

```bash
cd embed/sidecar
uv venv .venv
uv pip install --python .venv/bin/python -r requirements.txt
.venv/bin/python -c "
from server import EmbedRequest
import asyncio, server as s
# Force the lifespan to load the model synchronously for a smoke test
async def go():
    async with s.lifespan(s.app):
        resp = s.embed(EmbedRequest(texts=['def hello(): pass', 'fix nil deref bug']))
        assert len(resp.vectors) == 2, resp
        assert resp.dim == 1024, resp.dim
        print(f'ok, dim={resp.dim}, vec[0][:5]={resp.vectors[0][:5]}')
asyncio.run(go())
"
```

Expected: `ok, dim=1024, vec[0][:5]=[...]`. Model download ~700 MB on first run.

- [ ] **Step 4: Commit**

```bash
git add embed/sidecar/server.py embed/sidecar/requirements.txt
git commit -m "embed: Phase B sidecar — FastAPI server for arctic-embed-l-v2"
```

---

## Task 3: Go SidecarEmbedder type (TDD)

**Files:**
- Create: `internal/embed/sidecar.go`
- Create: `internal/embed/sidecar_test.go`

Implements the `Embedder` interface from `internal/embed/embed.go:24`. Speaks HTTP to the sidecar server. Test via `httptest.Server` — no real Python sidecar needed for unit tests.

- [ ] **Step 1: Write the failing test**

Create `internal/embed/sidecar_test.go`:

```go
package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSidecarEmbed_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		var req sidecarEmbedReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out := sidecarEmbedResp{Dim: 4}
		for range req.Texts {
			out.Vectors = append(out.Vectors, []float32{0.1, 0.2, 0.3, 0.4})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	e := &SidecarEmbedder{Endpoint: srv.URL, HTTP: srv.Client()}
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("want 2 vectors, got %d", len(vecs))
	}
	if e.Dim() != 4 {
		t.Errorf("want Dim=4, got %d", e.Dim())
	}
}

func TestSidecarEmbed_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"model not loaded"}`))
	}))
	defer srv.Close()
	e := &SidecarEmbedder{Endpoint: srv.URL, HTTP: srv.Client()}
	_, err := e.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code; got %v", err)
	}
}

func TestSidecarHealth_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "dim": 1024})
	}))
	defer srv.Close()
	e := &SidecarEmbedder{Endpoint: srv.URL, HTTP: srv.Client()}
	if err := e.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/embed/ -run "TestSidecar" -v
```

Expected: FAIL — `undefined: SidecarEmbedder`.

- [ ] **Step 3: Implement sidecar.go**

Create `internal/embed/sidecar.go`:

```go
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// SidecarEmbedder talks HTTP to a Python sidecar process loading
// Snowflake/snowflake-arctic-embed-l-v2.0 (or any drop-in successor).
// It implements the Embedder interface.
type SidecarEmbedder struct {
	Endpoint string       // e.g. "http://localhost:8765"
	HTTP     *http.Client // optional; defaults to a shared client with 60s timeout

	mu  sync.Mutex
	dim int
}

type sidecarEmbedReq struct {
	Texts []string `json:"texts"`
}

type sidecarEmbedResp struct {
	Vectors [][]float32 `json:"vectors"`
	Dim     int         `json:"dim"`
}

func (e *SidecarEmbedder) endpoint() string {
	return strings.TrimRight(e.Endpoint, "/")
}

func (e *SidecarEmbedder) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return defaultOllamaHTTPClient // reuse the shared client from ollama.go
}

// Embed POSTs to {endpoint}/embed. Returns one Vector per input text.
func (e *SidecarEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	body, err := json.Marshal(sidecarEmbedReq{Texts: texts})
	if err != nil {
		return nil, err
	}
	url := e.endpoint() + "/embed"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOllamaRespSize))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("sidecar %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed sidecarEmbedResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode sidecar response: %w", err)
	}
	if len(parsed.Vectors) != len(texts) {
		return nil, fmt.Errorf("sidecar returned %d vectors for %d texts", len(parsed.Vectors), len(texts))
	}
	out := make([]Vector, len(parsed.Vectors))
	for i, v := range parsed.Vectors {
		out[i] = Vector(v)
	}
	e.mu.Lock()
	if e.dim == 0 && parsed.Dim > 0 {
		e.dim = parsed.Dim
	}
	e.mu.Unlock()
	return out, nil
}

// Dim returns the embedding dimension. Returns 0 until the first successful
// Embed or HealthCheck call. Safe for concurrent use.
func (e *SidecarEmbedder) Dim() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dim
}

// HealthCheck GETs {endpoint}/health and populates Dim() from the response.
// Returns nil iff the sidecar reports ready.
func (e *SidecarEmbedder) HealthCheck(ctx context.Context) error {
	url := e.endpoint() + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("sidecar health probe at %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sidecar %s returned %d", url, resp.StatusCode)
	}
	var parsed struct {
		Dim int `json:"dim"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err == nil && parsed.Dim > 0 {
		e.mu.Lock()
		e.dim = parsed.Dim
		e.mu.Unlock()
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/embed/ -run "TestSidecar" -v
```

Expected: 3 passed.

- [ ] **Step 5: Run the full embed test suite to confirm no regression**

```bash
go test ./internal/embed/...
```

Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add internal/embed/sidecar.go internal/embed/sidecar_test.go
git commit -m "embed: SidecarEmbedder type implementing Embedder via HTTP"
```

---

## Task 4: Bootstrap routing — `--embedder-backend sidecar`

**Files:**
- Modify: `internal/embed/bootstrap.go` — extend `SelectOptions` and `SelectEmbedder` to route to `SidecarEmbedder` when configured
- Modify: `internal/embed/bootstrap_test.go` — add tests for the new branch

- [ ] **Step 1: Extend SelectOptions**

Read `internal/embed/bootstrap.go`. The current `SelectOptions` struct has `Endpoint`, `ExplicitModel`, `AutoPull`, etc.

Add two new fields after `ExplicitModel`:

```go
// Backend selects which Embedder implementation to use. Empty defaults to
// "ollama". "sidecar" routes through SidecarEmbedder against
// SidecarEndpoint.
Backend string

// SidecarEndpoint is the http://host:port of the Python sidecar process
// for the "sidecar" backend. Defaults to http://localhost:8765 when
// Backend=="sidecar" and this is empty.
SidecarEndpoint string
```

- [ ] **Step 2: Branch in SelectEmbedder**

At the top of `SelectEmbedder` (before the Detect call), add:

```go
if opts.Backend == "sidecar" {
	endpoint := opts.SidecarEndpoint
	if endpoint == "" {
		endpoint = "http://localhost:8765"
	}
	se := &SidecarEmbedder{Endpoint: endpoint}
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := se.HealthCheck(hctx); err != nil {
		// Sidecar is the requested backend but isn't ready. Fall through
		// to the Ollama path so clustering doesn't silently skip; surface
		// the issue via the SkipReason if Ollama is also unavailable.
		fmt.Fprintf(os.Stderr, "sidecar unavailable (%v); falling back to Ollama\n", err)
	} else {
		return se, "sidecar:" + endpoint, nil
	}
}
```

Add imports for `context`, `time`, `os`, `fmt` if not already present. (The file already uses `context` and `fmt`.)

- [ ] **Step 3: Write failing tests**

Append to `internal/embed/bootstrap_test.go`:

```go
func TestSelectEmbedder_SidecarHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","dim":1024}`))
	}))
	defer srv.Close()
	e, model, reason := SelectEmbedder(context.Background(), SelectOptions{
		Backend:         "sidecar",
		SidecarEndpoint: srv.URL,
		NonInteractive:  true,
		NoPrompt:        true,
	}, nil)
	if reason != nil {
		t.Fatalf("unexpected SkipReason: %+v", reason)
	}
	if _, ok := e.(*SidecarEmbedder); !ok {
		t.Fatalf("want *SidecarEmbedder, got %T", e)
	}
	if !strings.HasPrefix(model, "sidecar:") {
		t.Errorf("want sidecar: prefix in model name, got %q", model)
	}
}

func TestSelectEmbedder_SidecarUnhealthy_FallsBack(t *testing.T) {
	// Sidecar refuses connections; SelectEmbedder should fall through to
	// the existing Ollama path. Here Ollama is also unreachable, so we
	// expect a SkipReason from the Ollama path (not from the sidecar one).
	srv := newOllamaStubFailing(t) // existing test helper from this file
	defer srv.Close()
	_, _, reason := SelectEmbedder(context.Background(), SelectOptions{
		Endpoint:        srv.URL,
		Backend:         "sidecar",
		SidecarEndpoint: "http://127.0.0.1:1", // guaranteed-unreachable
		NonInteractive:  true,
		NoPrompt:        true,
	}, nil)
	if reason == nil {
		t.Fatal("want SkipReason from fallback path, got nil")
	}
	// The SkipReason code should describe the Ollama failure, not the
	// sidecar failure — falling through implies Ollama is the new authority.
	if !strings.Contains(reason.Code, "ollama") && !strings.Contains(reason.Code, "no_model") {
		t.Errorf("expected Ollama-flavored SkipReason; got %q", reason.Code)
	}
}
```

(The `newOllamaStubFailing` helper may need to be added to the test file if it isn't already there. Look at the existing `TestSelectEmbedder_OllamaUnreachable` test and adapt.)

- [ ] **Step 4: Run tests**

```bash
go test ./internal/embed/ -v -run "TestSelectEmbedder_Sidecar"
```

Expected: 2 passed.

- [ ] **Step 5: Run the full embed test suite**

```bash
go test ./internal/embed/...
```

Expected: all pass (no regression in the existing Ollama tests).

- [ ] **Step 6: Commit**

```bash
git add internal/embed/bootstrap.go internal/embed/bootstrap_test.go
git commit -m "embed: SelectEmbedder routes to SidecarEmbedder for Backend=sidecar"
```

---

## Task 5: CLI flag wiring

**Files:**
- Modify: `cmd/spn/forks.go` — parse `--embedder-backend` flag
- Modify: `cmd/spoon/main.go` — same surface for the TUI entrypoint (verify which file/lines hold the equivalent flag-parsing code)

- [ ] **Step 1: Add flag to spn forks command**

Read `cmd/spn/forks.go`. Find the flag parser around lines 148-159 where `--embedder` and `--embedder-model` are handled. Add after `--embedder-model`:

```go
case "--embedder-backend":
	if i+1 >= len(args) {
		return agentio.NewError(agentio.CodeBadInput, "--embedder-backend requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	i++
	val := strings.ToLower(args[i])
	if val != "ollama" && val != "sidecar" {
		return agentio.NewError(agentio.CodeBadInput, "--embedder-backend must be 'ollama' or 'sidecar'", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	opts.Cluster.Backend = val
case "--sidecar-endpoint":
	if i+1 >= len(args) {
		return agentio.NewError(agentio.CodeBadInput, "--sidecar-endpoint requires a value", agentio.RemediationBadInput("forks", "list")).Emit(stderr)
	}
	i++
	opts.Cluster.SidecarEndpoint = args[i]
```

This requires `opts.Cluster.Backend` and `opts.Cluster.SidecarEndpoint` fields to exist in `forksops.ClusterOptions`. Add them in `internal/forksops/stream.go` near `ModelOverride`:

```go
// Backend selects the Embedder implementation. Empty defaults to "ollama".
Backend string

// SidecarEndpoint is the http://host:port of the Python sidecar process,
// used when Backend=="sidecar".
SidecarEndpoint string
```

Then wire those through to the `embed.SelectOptions` call inside `runForksClusterPipeline`.

- [ ] **Step 2: Add equivalent env var support**

In `internal/embed/bootstrap.go`, near the existing env var consumers, support `SPOON_EMBEDDER_BACKEND` and `SPOON_SIDECAR_ENDPOINT` as fallbacks for `opts.Backend` and `opts.SidecarEndpoint`. These are read inside `SelectEmbedder` itself if the fields are empty:

```go
if opts.Backend == "" {
	opts.Backend = strings.ToLower(os.Getenv("SPOON_EMBEDDER_BACKEND"))
}
if opts.SidecarEndpoint == "" {
	opts.SidecarEndpoint = os.Getenv("SPOON_SIDECAR_ENDPOINT")
}
```

(Add `os` and `strings` imports if missing — both are likely already imported.)

- [ ] **Step 3: Update help text**

Search for the existing flag help/usage block in `cmd/spn/forks.go` (or wherever it lives). Add lines for `--embedder-backend` and `--sidecar-endpoint`.

- [ ] **Step 4: Verify build + run**

```bash
go build ./cmd/spn
./spn forks list --help 2>&1 | grep -E "embedder-backend|sidecar-endpoint"
```

Expected: both new flags appear in the help text.

- [ ] **Step 5: Commit**

```bash
git add cmd/spn/forks.go internal/forksops/stream.go internal/embed/bootstrap.go
git commit -m "cli: --embedder-backend and --sidecar-endpoint flags"
```

---

## Task 6: TUI flag wiring (cmd/spoon)

**Files:**
- Modify: `cmd/spoon/main.go` (and possibly `internal/tui/*` if the option propagates through)

The TUI entry point at `cmd/spoon/main.go` builds `forksops.Options` (or its TUI equivalent) before launching the cluster pipeline. Mirror the CLI changes from Task 5.

- [ ] **Step 1: Find the TUI's options struct**

```bash
grep -n "Cluster.*Options\|ClusterOptions" cmd/spoon/main.go internal/tui/*.go
```

Locate where the TUI sets cluster options.

- [ ] **Step 2: Plumb Backend + SidecarEndpoint**

Add the same two flags (`--embedder-backend`, `--sidecar-endpoint`) to the TUI's argument parser. Set them on the `Cluster` options struct.

- [ ] **Step 3: Verify build**

```bash
go build ./cmd/spoon
```

Expected: successful build, no errors.

- [ ] **Step 4: Commit**

```bash
git add cmd/spoon/main.go internal/tui/
git commit -m "tui: --embedder-backend and --sidecar-endpoint flags"
```

---

## Task 7: Dockerfile for the sidecar

**Files:**
- Create: `embed/sidecar/Dockerfile`

A reproducible container image with the model weights pre-pulled. Multi-stage build to keep the runtime image lean (~3 GB compressed: torch CPU wheel + weights).

- [ ] **Step 1: Write the Dockerfile**

Create `embed/sidecar/Dockerfile`:

```dockerfile
# syntax=docker/dockerfile:1.7
FROM python:3.12-slim AS deps

ENV PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential git && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY requirements.txt .
RUN pip install --upgrade pip && pip install -r requirements.txt

# Pre-fetch the model weights into the image so first-request latency is low.
RUN python -c "from sentence_transformers import SentenceTransformer; \
    SentenceTransformer('Snowflake/snowflake-arctic-embed-l-v2.0', trust_remote_code=True)"

FROM python:3.12-slim AS runtime

ENV PYTHONUNBUFFERED=1 \
    SPOON_SIDECAR_DEVICE=cpu \
    HF_HOME=/root/.cache/huggingface

WORKDIR /app
COPY --from=deps /usr/local/lib/python3.12/site-packages /usr/local/lib/python3.12/site-packages
COPY --from=deps /usr/local/bin /usr/local/bin
COPY --from=deps /root/.cache/huggingface /root/.cache/huggingface
COPY server.py .

EXPOSE 8765
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
    CMD python -c "import urllib.request, sys; urllib.request.urlopen('http://localhost:8765/health', timeout=3).read()" || exit 1

CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8765"]
```

- [ ] **Step 2: Smoke-test the image (optional, requires Docker)**

```bash
cd embed/sidecar
docker build -t spoon-sidecar:dev .
docker run --rm -p 8765:8765 spoon-sidecar:dev &
sleep 30
curl -s http://localhost:8765/health
docker stop $(docker ps -q --filter ancestor=spoon-sidecar:dev) 2>/dev/null
```

Expected: health endpoint returns `{"status":"ok","model":"Snowflake/snowflake-arctic-embed-l-v2.0",...,"dim":1024}`.

Skip this step in CI environments without Docker. Build verification only:

```bash
docker build --target deps -t spoon-sidecar-deps:dev embed/sidecar/  # validates first stage
```

- [ ] **Step 3: Commit**

```bash
git add embed/sidecar/Dockerfile
git commit -m "embed/sidecar: Dockerfile with pre-pulled arctic-embed-l-v2 weights"
```

---

## Task 8: User documentation

**Files:**
- Create: `embed/sidecar/README.md`
- Create: `docs/embedders.md`

- [ ] **Step 1: Write the sidecar's quickstart README**

Create `embed/sidecar/README.md`:

```markdown
# Spoon Behavioral-Embeddings Sidecar

A Python service that serves `Snowflake/snowflake-arctic-embed-l-v2.0`
(Apache 2.0) as an HTTP embedder for spoon's fork-clustering pipeline.

## When to use it

The default spoon embedder is Ollama-served `nomic-embed-text` (8 K context,
274 MB model, drop-in zero-setup). The arctic sidecar produces fork-pair
similarity rankings that match human judgment ~0.12 tau better than
nomic-embed-text on a 53-pair hand-curated test
(see `experiments/started/behavioral-embeddings/RESULTS_PANEL.md`).

Trade-off: ~2 GB resident memory and a Python process to manage.

## Running

### Docker (recommended)

```sh
docker build -t spoon-sidecar embed/sidecar/
docker run --rm -d -p 8765:8765 --name spoon-sidecar spoon-sidecar
```

Then point spoon at it:

```sh
spn forks list --embedder-backend sidecar --sidecar-endpoint http://localhost:8765 owner/repo
```

### Local Python

```sh
cd embed/sidecar
uv venv .venv
uv pip install --python .venv/bin/python -r requirements.txt
.venv/bin/uvicorn server:app --host 0.0.0.0 --port 8765
```

First start downloads ~700 MB of weights to `~/.cache/huggingface/`.

## Endpoints

| method | path | request | response |
|---|---|---|---|
| `GET`  | `/health` | — | `{"status":"ok","model":"...","device":"cpu","dim":1024}` |
| `POST` | `/embed`  | `{"texts":[...]}` | `{"vectors":[[...],...],"dim":1024}` |

## Environment

| var | default | meaning |
|---|---|---|
| `SPOON_SIDECAR_MODEL`  | `Snowflake/snowflake-arctic-embed-l-v2.0` | HF model to load |
| `SPOON_SIDECAR_DEVICE` | `cpu` | `cuda` to use GPU if available |

## License

Apache 2.0 — see `LICENSE-arctic.md` for the model's redistribution terms.
```

- [ ] **Step 2: Write top-level embedder documentation**

Create `docs/embedders.md`:

```markdown
# Spoon Embedders

Spoon's fork-clustering pipeline uses text embeddings to compare forks.
Two embedder backends are supported.

## Default: Ollama (`nomic-embed-text`)

Zero setup if Ollama is installed and running. Pulled automatically via
`spn embed pull nomic-embed-text` or on first cluster run with
`SPOON_AUTO_PULL=1`.

```sh
spn forks list golang/go
```

## Optional: Behavioral-embeddings sidecar (`arctic-embed-l-v2`)

A higher-fidelity embedder (Δ = +0.12 Kendall's tau over nomic on the
behavioral-embeddings gate experiment). Requires a Python sidecar process.

```sh
docker run --rm -d -p 8765:8765 spoon-sidecar
spn forks list --embedder-backend sidecar golang/go
```

Or via env: `SPOON_EMBEDDER_BACKEND=sidecar SPOON_SIDECAR_ENDPOINT=http://localhost:8765 spn forks list golang/go`.

Falls back to Ollama if the sidecar is unreachable.

See `embed/sidecar/README.md` for sidecar setup details. See
`experiments/started/behavioral-embeddings/RESULTS_PANEL.md` for the
evaluation that motivated this choice.
```

- [ ] **Step 3: Commit**

```bash
git add embed/sidecar/README.md docs/embedders.md
git commit -m "docs: sidecar quickstart + embedder backend overview"
```

---

## Task 9: Performance benchmark

**Files:**
- Create: `embed/sidecar/bench.py` (standalone benchmark, gitignored output)

Validate the sidecar's throughput on the 200-PR fixture from the panel re-test. Goal: establish ground truth latency numbers for the README and the eventual ops/perf docs.

- [ ] **Step 1: Write the benchmark**

Create `embed/sidecar/bench.py`:

```python
"""Benchmark the spoon sidecar against features.json from the panel re-test.

Usage:
    .venv/bin/python bench.py --features ../experiments/started/behavioral-embeddings/features.json
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
```

- [ ] **Step 2: Run the benchmark**

Start the sidecar (Docker or local), then:

```bash
cd embed/sidecar
.venv/bin/python bench.py \
  --features ../../experiments/started/behavioral-embeddings/features.json \
  --batch-size 16
```

Expected output:
- ~20-50 records/sec on CPU
- ~200-500 records/sec on GPU
- Average latency 20-50 ms/record on CPU

- [ ] **Step 3: Add the benchmark output to the README**

Append measured numbers to `embed/sidecar/README.md` under a "Throughput" heading. Format:

```markdown
## Throughput

Measured on the 200-PR fixture from `experiments/started/behavioral-embeddings/features.json`:

| environment | batch | total time | records/sec | avg latency |
| --- | --- | --- | --- | --- |
| CPU (vinbonesjr) | 16 | <FILL> | <FILL> | <FILL> |
| Docker (CPU) | 16 | <FILL> | <FILL> | <FILL> |
```

Substitute `<FILL>` with measured numbers.

- [ ] **Step 4: Commit**

```bash
git add embed/sidecar/bench.py embed/sidecar/README.md
git commit -m "embed/sidecar: throughput benchmark on 200-PR fixture"
```

---

## Task 10: End-to-end smoke test + final review

**Files:**
- No new files; manual verification step.

- [ ] **Step 1: Start sidecar**

```bash
docker run --rm -d -p 8765:8765 --name spoon-sidecar spoon-sidecar
sleep 30  # wait for model load
curl -s http://localhost:8765/health
```

Expected: 200 OK with `{"status":"ok","dim":1024}`.

- [ ] **Step 2: Run spn forks against sidecar**

```bash
spn forks list --embedder-backend sidecar --sidecar-endpoint http://localhost:8765 --tier 2 IBM/mcp-context-forge | head -20
```

Expected: NDJSON output with `clusterId`, `noveltyScore`, etc. Same shape as the existing Ollama-backed run.

- [ ] **Step 3: Run with sidecar offline → fallback**

```bash
docker stop spoon-sidecar
spn forks list --embedder-backend sidecar --tier 2 IBM/mcp-context-forge | head -5
```

Expected: stderr warning "sidecar unavailable (...); falling back to Ollama" + NDJSON output continuing from the Ollama path.

- [ ] **Step 4: Run the full unit + integration test suite**

```bash
go test ./... 2>&1 | tail -20
```

Expected: all packages pass.

- [ ] **Step 5: Commit any documentation tweaks** (e.g., updated throughput numbers)

```bash
git add -u
git commit -m "embed/sidecar: round-trip smoke test passed; docs finalized"
```

- [ ] **Step 6: Update PR #13 description**

```bash
gh pr edit 13 --body "$(gh pr view 13 --json body --jq '.body' | sed 's/$/\n\n## Phase B: shipped\nSee \`docs\/superpowers\/plans\/2026-05-13-behavioral-embeddings-sidecar.md\` for the sidecar implementation. Model: Snowflake\/snowflake-arctic-embed-l-v2.0 (Apache 2.0). Verdict: Phase B authorized at Δ=+0.12 Kendall'\''s tau./')"
```

(Or edit the PR description manually via the GitHub UI.)

- [ ] **Step 7: Open a new PR for Phase B itself**

Phase B is significant work (~12 commits across 10 tasks). Open a separate PR rather than amending PR #13 (which is the experiment record).

```bash
git push -u origin behavioral-embeddings-validation
gh pr create --title "embed: Phase B behavioral-embeddings sidecar (arctic-embed-l-v2)" --body "$(cat <<'EOF'
## Summary

Ships the Phase B sidecar authorized by the panel re-test
(`experiments/started/behavioral-embeddings/RESULTS_PANEL.md`).

- Python FastAPI sidecar loading `Snowflake/snowflake-arctic-embed-l-v2.0` (Apache 2.0)
- Go-side `SidecarEmbedder` implementing the existing `Embedder` interface
- `--embedder-backend sidecar` CLI flag + `SPOON_EMBEDDER_BACKEND` env var
- Falls back to Ollama if the sidecar is unreachable
- Docker recipe with pre-pulled weights

## Result vs Phase A

Phase A (rejected): CodeBERTa/CodeExecutor underperformed nomic by Δ = −0.08 tau.
Phase B (authorized): arctic-embed-l-v2 outperforms nomic by Δ = +0.12 tau.

Methodology fix that turned the result around: lift the 5 KB per-modality
truncation cap (which had been a Phase A workaround for 512-token-window
models) and let each tokenizer's native context apply. The strongest passing
model was general-purpose, not code-specific.

## Test plan

- [x] Go unit tests for SidecarEmbedder (httptest mock)
- [x] Go unit tests for bootstrap routing
- [x] Sidecar smoke test (curl /health, curl /embed)
- [x] End-to-end `spn forks list --embedder-backend sidecar`
- [x] Fallback path verified (stop sidecar, observe Ollama fallback)
- [x] Throughput benchmark in `embed/sidecar/README.md`

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-Review

After writing this plan, checked against the spec at `docs/superpowers/specs/future/future-work-behavioral-embeddings.md`:

**Spec coverage:**
- Sidecar process management → Task 3 (Go) + Task 2 (Python)
- /embed and /health endpoints → Task 2
- `--embedder-backend sidecar` CLI flag → Task 5
- Fallback to Ollama when sidecar fails → Task 4
- Docker recipe → Task 7
- README documentation → Task 8
- Performance benchmark → Task 9
- License check → Task 1

**Spec deviations:**
- Model: `Snowflake/snowflake-arctic-embed-l-v2.0` instead of `microsoft/codeexecutor`. The panel re-test on 2026-05-13 chose the panel winner over the originally-proposed code-specific model. The substitution is justified in `RESULTS_PANEL.md`.
- Pooling/library: sentence-transformers instead of manual transformers + mean-pool. Arctic-embed-l-v2 requires CLS pooling and L2 normalization, which sentence-transformers handles correctly; manual pooling would replicate a bug.

**Placeholder scan:** No `TBD` / `implement later`. `<FILL>` markers in the throughput table are explicit fill-in-the-blanks for measured numbers.

**Type consistency:** `SidecarEmbedder` fields (`Endpoint`, `HTTP`, `Dim`) match between the test (Task 3 Step 1) and the impl (Task 3 Step 3). `SelectOptions.Backend` and `SelectOptions.SidecarEndpoint` are consistent between bootstrap.go (Task 4) and the CLI flag wiring (Task 5).

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-13-behavioral-embeddings-sidecar.md`. Two execution options:

1. **Subagent-Driven** — fresh subagent per task, two-stage review.
2. **Inline Execution** — execute tasks in this session with checkpoints.
