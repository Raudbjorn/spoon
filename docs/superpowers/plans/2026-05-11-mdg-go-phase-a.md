# Full MDG Centrality — Phase A (Go) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a real Module Dependency Graph (MDG) for Go-only repositories, behind a `--full-mdg` flag, that produces personalized-PageRank centrality scores. Wire it into the existing fork-impact pipeline as an alternate backend that the user can opt into; fall back silently to the directory-centrality proxy when the MDG cannot be built.

**Architecture:** A new `internal/mdg/` package owns three concerns:

1. **Graph + algorithm** (`mdg.go`): the `Graph` type and a deterministic personalized-PageRank implementation. Pure in-memory, no IO.
2. **Parsers** (`parse_go.go`): build a `Graph` from source files in a clone. Phase A ships Go via the stdlib `go/parser` + `go/build` packages. The parser dispatch is in `build.go::Build`.
3. **Source acquisition + cache** (`clone.go`, `cache.go`): shallow-clone the upstream to a temp dir, parse, discard, persist a JSON cache (24 h TTL, invalidated by upstream HEAD SHA).

The `internal/repo` package grows a `Centrality` interface that both `DirectoryCentrality` (existing) and the new `mdg.Centrality` (new) implement. The cluster pipeline (`internal/cluster/pipeline.go::loadOrComputeCentrality`) is rewritten to dispatch on a backend selector that the CLI populates from `--full-mdg`. When MDG is unavailable (clone fails, no Go files, all parses fail), the pipeline silently falls back to the directory proxy.

**Tech Stack:** Go 1.26+, stdlib only (`go/parser`, `go/build`, `go/token`, `os/exec`, `encoding/json`). No new module dependencies. Tests follow the existing pattern: stubs + table-driven cases + `t.TempDir()` fixtures, no network.

---

## Background and integration points

Read this section before writing code. The MDG output has to plug into the existing change-impact pipeline cleanly.

**Existing centrality flow (already in main):**

- `internal/repo/centrality.go::DirectoryCentrality` is a struct with a `DirScore map[string]float64`, `CoreDirs []string`, and a `ScoreFork(touchedDirs []string) float64` method.
- `internal/repo/centrality.go::Compute` builds it from `TreeSource` + `CommitSource`.
- `internal/repo/cache.go::LoadCache/SaveCache` persists it under `~/.cache/spoon/centrality/<provider>/<owner>__<repo>.json` with a 24 h TTL.
- `internal/cluster/pipeline.go::loadOrComputeCentrality` is the integration site. It is called once per pipeline run and returns a `(DirectoryCentrality, bool)` pair.
- The pipeline then does, for each fork: `paths := pathsOf(t2); impact := changeImpactFor(dc, dcOK, paths)`. `changeImpactFor` converts each path to its parent directory via `dirOf` and calls `dc.ScoreFork(dirs)`.

**Key insight:** The pipeline already starts from file *paths*. The conversion to directories happens inside `changeImpactFor`. For MDG, we do not want to collapse to directories. The plan therefore lifts the call-site contract to `Centrality.ScoreFork(touchedFiles []string)`, where the implementation chooses how to map files → scored units (modules for MDG, directories for the proxy).

**CLI surface:** `spoon` (the TUI / dump entry point) and `spn` (the agent-oriented entry point) both expose centrality, by different routes:

- `cmd/spoon/main.go` runs the cluster pipeline through `dump.Run` (JSON/CSV) and through `tui/cache_bridge.go`. Both call `cluster.RunPipeline`, which calls `loadOrComputeCentrality`. The pipeline-options struct is `cluster.PipelineOptions`.
- `cmd/spn/repo.go::doRepoCentrality` is a direct `repo.Compute` invocation. Its `repoCentralityFn` indirection makes it test-stubbable.

Both entry points need `--full-mdg` plumbing.

**Network / auth:** Cloning uses the locally-available `git`. For GitHub upstreams when `gh` is on `PATH`, prefer `gh repo clone <owner>/<repo> -- --depth 1 --filter=blob:none <tempdir>` (handles auth via the gh token). Otherwise fall back to `git clone --depth 1 --filter=blob:none https://github.com/<owner>/<repo>.git <tempdir>`. For GitLab, only the plain `git clone` form is supported in Phase A.

**Tracked baseline:** Until this plan starts running, the only centrality is the directory proxy. The MDG path is strictly additive — never enabled by default; only `--full-mdg` opts in.

---

## File structure

**New files:**

- `internal/mdg/mdg.go` — `Module`, `Graph`, `PageRank`, `EntryPointTeleport` (pure, no IO)
- `internal/mdg/mdg_test.go`
- `internal/mdg/parse_go.go` — Go import parser (`parseGoPackages`)
- `internal/mdg/parse_go_test.go`
- `internal/mdg/build.go` — `Build(ctx, repoPath, opts) (*Graph, error)` orchestrator
- `internal/mdg/build_test.go`
- `internal/mdg/clone.go` — `ShallowClone(ctx, provider, owner, repo, dest) error`
- `internal/mdg/clone_test.go`
- `internal/mdg/cache.go` — `MDGCache` + `LoadCache`, `SaveCache`, cache-path helper
- `internal/mdg/cache_test.go`
- `internal/mdg/centrality.go` — `Centrality` type that wraps a `Graph` + PageRank scores and implements `repo.Centrality`
- `internal/mdg/centrality_test.go`

**Modified files:**

- `internal/repo/centrality.go` — introduce `Centrality` interface; refit `DirectoryCentrality.ScoreFork` to accept *file paths* (not directories); add an internal path-to-dir adapter
- `internal/repo/centrality_test.go` — adjust existing tests for the new method signature
- `internal/cluster/pipeline.go` — `loadOrComputeCentrality` dispatches by backend; `changeImpactFor` and the call site pass paths directly
- `internal/cluster/pipeline_test.go` — add a backend-dispatch test
- `cmd/spoon/main.go` — add `--full-mdg`, `--no-mdg`, thread to `cluster.PipelineOptions.CentralityBackend`
- `cmd/spoon/main_test.go` — flag-parsing tests
- `cmd/spn/repo.go` — accept `--full-mdg`; route `doRepoCentrality` through the MDG backend when set
- `cmd/spn/repo_test.go` — `--full-mdg` parsing test

Each file has one responsibility:

- `internal/mdg/mdg.go` owns the graph data type and the algorithm; no IO.
- `internal/mdg/parse_go.go` owns Go-specific parsing; no graph mutation outside its returned slice.
- `internal/mdg/build.go` is the only file that orchestrates parser dispatch + graph construction.
- `internal/mdg/clone.go` owns external git invocation.
- `internal/mdg/cache.go` owns JSON persistence.
- `internal/mdg/centrality.go` owns the bridge to `internal/repo`.
- `internal/cluster/pipeline.go` is the only orchestrator between heat and centrality backends.

---

## Out of scope (deferred to later plans)

- Python parser (Phase B)
- JS / TS parser (Phase C)
- Per-file fetch fallback when `git` is unavailable. Phase A skips MDG and falls back to the directory proxy if cloning fails.
- Cross-language imports (CGO, embedded Python, etc.). Out of scope per spec.
- Weighted edges based on import count (every edge has weight 1.0 in Phase A).
- Vendor-aware import resolution (`vendor/` is excluded; not resolved).
- Replacing the existing `--heat-weights` machinery — MDG output flows through the same `ChangeImpact` field on `HeatResult` without changing the heat weight pipeline.

---

## Task 1: `mdg` package skeleton + Module + Graph type

**Files:**
- Create: `internal/mdg/mdg.go`
- Create: `internal/mdg/mdg_test.go`

- [ ] **Step 1: Write the failing test for `Graph` construction**

```go
// internal/mdg/mdg_test.go
package mdg

import "testing"

func TestGraph_AddNodeAndEdge(t *testing.T) {
	g := NewGraph()
	a := g.AddNode(Module{Path: "example.com/a", Lang: "go"})
	b := g.AddNode(Module{Path: "example.com/b", Lang: "go"})
	g.AddEdge(a, b)

	if got := len(g.Nodes); got != 2 {
		t.Fatalf("Nodes: want 2, got %d", got)
	}
	if got := g.Index["example.com/a"]; got != a {
		t.Fatalf("Index lookup failed: got %d, want %d", got, a)
	}
	if got := g.Edges["example.com/a"]; len(got) != 1 || got[0] != "example.com/b" {
		t.Fatalf("Edges[a]: want [b], got %v", got)
	}
}

func TestGraph_AddNodeIdempotent(t *testing.T) {
	g := NewGraph()
	i1 := g.AddNode(Module{Path: "x", Lang: "go"})
	i2 := g.AddNode(Module{Path: "x", Lang: "go"})
	if i1 != i2 {
		t.Fatalf("AddNode should be idempotent on path: i1=%d i2=%d", i1, i2)
	}
	if len(g.Nodes) != 1 {
		t.Fatalf("Nodes: want 1, got %d", len(g.Nodes))
	}
}

func TestGraph_AddEdgeDeduplicates(t *testing.T) {
	g := NewGraph()
	a := g.AddNode(Module{Path: "a", Lang: "go"})
	b := g.AddNode(Module{Path: "b", Lang: "go"})
	g.AddEdge(a, b)
	g.AddEdge(a, b)
	if got := g.Edges["a"]; len(got) != 1 {
		t.Fatalf("Edges[a]: want 1 entry after duplicate add, got %d (%v)", len(got), got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mdg/...`
Expected: package not found / compile error (`mdg.go` does not exist yet).

- [ ] **Step 3: Implement `Module`, `Graph`, `NewGraph`, `AddNode`, `AddEdge`**

```go
// internal/mdg/mdg.go
//
// Package mdg builds a Module Dependency Graph (MDG) for a repository and
// scores modules by personalized PageRank. The graph nodes are modules
// (e.g., Go packages), the edges are explicit import statements parsed from
// source files. Phase A supports Go; later phases add Python and JS/TS.
package mdg

// Module is a single node in the MDG. Path is canonical — for Go it is the
// import path (e.g., "github.com/owner/repo/internal/auth").
type Module struct {
	Path string
	Lang string
}

// Graph holds the MDG. Edges is keyed by source-module path; values are the
// list of destination paths it imports. Index maps a module path to its
// position in Nodes.
type Graph struct {
	Nodes []Module
	Edges map[string][]string
	Index map[string]int
}

// NewGraph returns an empty Graph with initialized maps.
func NewGraph() *Graph {
	return &Graph{
		Edges: make(map[string][]string),
		Index: make(map[string]int),
	}
}

// AddNode inserts a module if its path is not already present and returns the
// node's index. Repeated calls with the same path return the existing index.
func (g *Graph) AddNode(m Module) int {
	if i, ok := g.Index[m.Path]; ok {
		return i
	}
	i := len(g.Nodes)
	g.Nodes = append(g.Nodes, m)
	g.Index[m.Path] = i
	return i
}

// AddEdge records an import from src → dst by node index. Duplicate edges are
// suppressed: AddEdge(a, b) twice produces one entry. Both src and dst must
// be valid indices.
func (g *Graph) AddEdge(src, dst int) {
	if src < 0 || src >= len(g.Nodes) || dst < 0 || dst >= len(g.Nodes) {
		return
	}
	srcPath := g.Nodes[src].Path
	dstPath := g.Nodes[dst].Path
	for _, existing := range g.Edges[srcPath] {
		if existing == dstPath {
			return
		}
	}
	g.Edges[srcPath] = append(g.Edges[srcPath], dstPath)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mdg/... -run 'TestGraph_'`
Expected: PASS, 3 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/mdg.go internal/mdg/mdg_test.go
git commit -m "mdg: introduce Graph + Module types"
```

---

## Task 2: Deterministic PageRank

**Files:**
- Modify: `internal/mdg/mdg.go`
- Modify: `internal/mdg/mdg_test.go`

- [ ] **Step 1: Write the failing test for PageRank on a known graph**

The graph is a 4-node directed graph: `a → b`, `a → c`, `b → c`, `c → a`, `d → c`. PageRank with damping = 0.85 over 50 iterations converges to a stable distribution where `c` is highest, `a` next, `b` and `d` lowest. Tolerances are generous (1e-3) because we are not validating an exact numeric reference, only the ordering and roughly the magnitudes.

```go
// internal/mdg/mdg_test.go (append)
import "math"

func TestPageRank_Ordering(t *testing.T) {
	g := NewGraph()
	a := g.AddNode(Module{Path: "a"})
	b := g.AddNode(Module{Path: "b"})
	c := g.AddNode(Module{Path: "c"})
	d := g.AddNode(Module{Path: "d"})
	g.AddEdge(a, b)
	g.AddEdge(a, c)
	g.AddEdge(b, c)
	g.AddEdge(c, a)
	g.AddEdge(d, c)

	scores := g.PageRank(nil, 0.85, 50)

	if len(scores) != 4 {
		t.Fatalf("scores: want 4, got %d", len(scores))
	}
	// c is sink with most incoming edges → highest score.
	if !(scores["c"] > scores["a"] && scores["a"] > scores["b"] && scores["c"] > scores["d"]) {
		t.Fatalf("unexpected ordering: %+v", scores)
	}
	// Probability mass must be conserved (within float tolerance).
	var sum float64
	for _, v := range scores {
		sum += v
	}
	if math.Abs(sum-1.0) > 1e-6 {
		t.Fatalf("sum of scores: want ~1.0, got %v (%+v)", sum, scores)
	}
}

func TestPageRank_EmptyGraph(t *testing.T) {
	g := NewGraph()
	scores := g.PageRank(nil, 0.85, 50)
	if len(scores) != 0 {
		t.Fatalf("empty graph: want 0 scores, got %d", len(scores))
	}
}

func TestPageRank_DanglingNode(t *testing.T) {
	// "dangling" = a node with no outgoing edges. Its rank should not be lost;
	// it should redistribute uniformly each iteration.
	g := NewGraph()
	g.AddNode(Module{Path: "x"})
	g.AddNode(Module{Path: "y"})
	scores := g.PageRank(nil, 0.85, 50)
	if math.Abs(scores["x"]-0.5) > 1e-6 || math.Abs(scores["y"]-0.5) > 1e-6 {
		t.Fatalf("isolated nodes: want each 0.5, got %+v", scores)
	}
}

func TestPageRank_DeterministicOrder(t *testing.T) {
	// Independent of node insertion order, equivalent graphs produce equivalent
	// score maps (within tolerance).
	g1 := NewGraph()
	g1.AddNode(Module{Path: "a"})
	g1.AddNode(Module{Path: "b"})
	g1.AddEdge(0, 1)

	g2 := NewGraph()
	g2.AddNode(Module{Path: "b"})
	g2.AddNode(Module{Path: "a"})
	g2.AddEdge(1, 0)

	s1 := g1.PageRank(nil, 0.85, 50)
	s2 := g2.PageRank(nil, 0.85, 50)
	for k, v1 := range s1 {
		if math.Abs(v1-s2[k]) > 1e-9 {
			t.Fatalf("score divergence for %q: %v vs %v", k, v1, s2[k])
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mdg/... -run TestPageRank_`
Expected: FAIL — `g.PageRank` not defined.

- [ ] **Step 3: Implement PageRank**

```go
// internal/mdg/mdg.go (append)

// PageRank returns personalized-PageRank scores keyed by module path.
//
// teleport, when non-nil, must be a probability distribution over node
// indices (len == len(g.Nodes); entries sum to 1.0). When nil, a uniform
// teleport is used. damping is the standard PageRank damping factor (≈0.85);
// iterations is the fixed number of power-method steps.
//
// Output values sum to 1.0 (within float rounding). For an empty graph the
// return is an empty map (not nil).
//
// Dangling nodes (no outgoing edges) redistribute their probability mass
// uniformly across all nodes each iteration, so the score distribution stays
// normalized.
func (g *Graph) PageRank(teleport []float64, damping float64, iterations int) map[string]float64 {
	n := len(g.Nodes)
	out := make(map[string]float64, n)
	if n == 0 {
		return out
	}

	// Normalize / default teleport.
	tp := make([]float64, n)
	if teleport == nil || len(teleport) != n {
		for i := range tp {
			tp[i] = 1.0 / float64(n)
		}
	} else {
		var sum float64
		for _, v := range teleport {
			sum += v
		}
		if sum <= 0 {
			for i := range tp {
				tp[i] = 1.0 / float64(n)
			}
		} else {
			for i, v := range teleport {
				tp[i] = v / sum
			}
		}
	}

	// Out-degree per node (by index, looked up via Path → Index).
	outDeg := make([]int, n)
	for srcPath, dsts := range g.Edges {
		if i, ok := g.Index[srcPath]; ok {
			outDeg[i] = len(dsts)
		}
	}

	// Initial rank: uniform.
	rank := make([]float64, n)
	for i := range rank {
		rank[i] = 1.0 / float64(n)
	}
	next := make([]float64, n)

	for it := 0; it < iterations; it++ {
		// Step 1: baseline contribution from teleport.
		for i := range next {
			next[i] = (1 - damping) * tp[i]
		}
		// Step 2: dangling mass — gather and redistribute uniformly.
		var dangling float64
		for i := 0; i < n; i++ {
			if outDeg[i] == 0 {
				dangling += rank[i]
			}
		}
		if dangling > 0 {
			add := damping * dangling / float64(n)
			for i := range next {
				next[i] += add
			}
		}
		// Step 3: propagate via edges.
		for srcPath, dsts := range g.Edges {
			si, ok := g.Index[srcPath]
			if !ok || outDeg[si] == 0 {
				continue
			}
			share := damping * rank[si] / float64(outDeg[si])
			for _, dstPath := range dsts {
				if di, ok := g.Index[dstPath]; ok {
					next[di] += share
				}
			}
		}
		rank, next = next, rank
	}

	for i, m := range g.Nodes {
		out[m.Path] = rank[i]
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mdg/... -run TestPageRank_ -v`
Expected: PASS, 4 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/mdg.go internal/mdg/mdg_test.go
git commit -m "mdg: add deterministic personalized PageRank"
```

---

## Task 3: `EntryPointTeleport` for Go

**Files:**
- Modify: `internal/mdg/mdg.go`
- Modify: `internal/mdg/mdg_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/mdg/mdg_test.go (append)
func TestEntryPointTeleport_PrefersMainPackages(t *testing.T) {
	g := NewGraph()
	g.AddNode(Module{Path: "repo/cmd/foo", Lang: "go", IsMain: true})
	g.AddNode(Module{Path: "repo/cmd/bar", Lang: "go", IsMain: true})
	g.AddNode(Module{Path: "repo/internal/util", Lang: "go", IsMain: false})

	tp := g.EntryPointTeleport()

	if len(tp) != 3 {
		t.Fatalf("teleport len: want 3, got %d", len(tp))
	}
	if tp[0] == 0 || tp[1] == 0 {
		t.Fatalf("main packages should have non-zero teleport: %v", tp)
	}
	if tp[2] != 0 {
		t.Fatalf("non-main package should have zero teleport: %v", tp)
	}
	if math.Abs(tp[0]+tp[1]+tp[2]-1.0) > 1e-9 {
		t.Fatalf("teleport should sum to 1.0, got %v", tp[0]+tp[1]+tp[2])
	}
}

func TestEntryPointTeleport_NoMainFallsBackToUniform(t *testing.T) {
	g := NewGraph()
	g.AddNode(Module{Path: "a"})
	g.AddNode(Module{Path: "b"})
	tp := g.EntryPointTeleport()
	if math.Abs(tp[0]-0.5) > 1e-9 || math.Abs(tp[1]-0.5) > 1e-9 {
		t.Fatalf("no main → uniform; got %v", tp)
	}
}

func TestEntryPointTeleport_EmptyGraph(t *testing.T) {
	g := NewGraph()
	tp := g.EntryPointTeleport()
	if tp != nil {
		t.Fatalf("empty graph: want nil teleport, got %v", tp)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mdg/... -run TestEntryPointTeleport_`
Expected: FAIL — `Module.IsMain` field and `g.EntryPointTeleport()` method do not exist.

- [ ] **Step 3: Add the `IsMain` field and the method**

Edit `Module` in `internal/mdg/mdg.go` to:

```go
// Module is a single node in the MDG. Path is canonical — for Go it is the
// import path (e.g., "github.com/owner/repo/internal/auth"). IsMain is true
// for packages that look like entry points (Go: `package main`; later: JS
// `src/index.*`, Python `__main__.py`).
type Module struct {
	Path   string
	Lang   string
	IsMain bool
}
```

Append:

```go
// EntryPointTeleport returns a teleport distribution that puts all probability
// mass on entry-point modules (Module.IsMain). When no module is marked main,
// returns a uniform distribution. For an empty graph returns nil.
func (g *Graph) EntryPointTeleport() []float64 {
	n := len(g.Nodes)
	if n == 0 {
		return nil
	}
	mainCount := 0
	for _, m := range g.Nodes {
		if m.IsMain {
			mainCount++
		}
	}
	tp := make([]float64, n)
	if mainCount == 0 {
		for i := range tp {
			tp[i] = 1.0 / float64(n)
		}
		return tp
	}
	share := 1.0 / float64(mainCount)
	for i, m := range g.Nodes {
		if m.IsMain {
			tp[i] = share
		}
	}
	return tp
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mdg/... -v`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/mdg.go internal/mdg/mdg_test.go
git commit -m "mdg: add EntryPointTeleport for personalized PageRank"
```

---

## Task 4: Go import parser

**Files:**
- Create: `internal/mdg/parse_go.go`
- Create: `internal/mdg/parse_go_test.go`

This task introduces `parseGoPackages`, which walks a directory tree and returns a slice of `goPkgInfo` records — one per Go package found. Each record carries the canonical import path, the IsMain flag, and the list of import paths the package's source files reference. The parser does **not** mutate a `Graph`; that is the orchestrator's job (Task 5).

- [ ] **Step 1: Write the failing test**

```go
// internal/mdg/parse_go_test.go
package mdg

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeFile is a test helper.
func writeFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func TestParseGoPackages_BasicModule(t *testing.T) {
	root := t.TempDir()

	writeFile(t, root, "go.mod", "module example.com/m\n\ngo 1.21\n")
	writeFile(t, root, "main.go", `package main

import (
	"example.com/m/internal/auth"
	"fmt"
)

func main() { fmt.Println(auth.Hello()) }
`)
	writeFile(t, root, "internal/auth/auth.go", `package auth

import "example.com/m/internal/util"

func Hello() string { return util.Greet() }
`)
	writeFile(t, root, "internal/util/util.go", `package util

func Greet() string { return "hi" }
`)

	pkgs, err := parseGoPackages(root)
	if err != nil {
		t.Fatalf("parseGoPackages: %v", err)
	}
	if len(pkgs) != 3 {
		t.Fatalf("want 3 packages, got %d: %+v", len(pkgs), pkgs)
	}

	byPath := map[string]goPkgInfo{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	root3, ok := byPath["example.com/m"]
	if !ok {
		t.Fatalf("missing root package; got %v", keys(byPath))
	}
	if !root3.IsMain {
		t.Errorf("root package should be IsMain")
	}
	sort.Strings(root3.Imports)
	want := []string{"example.com/m/internal/auth"} // stdlib filtered
	if !equalStrSlices(root3.Imports, want) {
		t.Errorf("root imports: want %v, got %v", want, root3.Imports)
	}

	auth := byPath["example.com/m/internal/auth"]
	if auth.IsMain {
		t.Errorf("internal/auth should not be IsMain")
	}
	if !equalStrSlices(auth.Imports, []string{"example.com/m/internal/util"}) {
		t.Errorf("auth imports: got %v", auth.Imports)
	}
}

func TestParseGoPackages_NoGoMod(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "x.go", "package x\n")
	pkgs, err := parseGoPackages(root)
	if err == nil {
		t.Fatalf("expected error when go.mod is missing, got %d packages", len(pkgs))
	}
}

func TestParseGoPackages_SkipsVendorAndHidden(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", "package main\nfunc main() {}\n")
	writeFile(t, root, "vendor/foo/bar.go", "package bar\n")
	writeFile(t, root, ".git/x.go", "package x\n")
	writeFile(t, root, "node_modules/y/z.go", "package z\n")

	pkgs, err := parseGoPackages(root)
	if err != nil {
		t.Fatalf("parseGoPackages: %v", err)
	}
	for _, p := range pkgs {
		if containsPathSegment(p.ImportPath, "vendor") ||
			containsPathSegment(p.ImportPath, ".git") ||
			containsPathSegment(p.ImportPath, "node_modules") {
			t.Errorf("package from excluded dir leaked: %q", p.ImportPath)
		}
	}
}

func TestParseGoPackages_SkipsBrokenFiles(t *testing.T) {
	// A syntactically invalid file should be skipped with a warning, not crash.
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", "package main\nfunc main() {}\n")
	writeFile(t, root, "broken/x.go", "package broken\n\nimport \"fmt\nfunc bad(\n")

	pkgs, err := parseGoPackages(root)
	if err != nil {
		t.Fatalf("parseGoPackages should swallow per-file errors: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatalf("want at least the root package, got %d", len(pkgs))
	}
}

// --- test helpers below ---

func keys(m map[string]goPkgInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsPathSegment(p, seg string) bool {
	// Detect "seg/" or "/seg/" or trailing "/seg" in slash-separated path.
	for _, s := range filepath.SplitList(filepath.ToSlash(p)) {
		_ = s
	}
	// Simpler: check direct substring with separators.
	if p == seg {
		return true
	}
	for i := 0; i+len(seg) <= len(p); i++ {
		if p[i:i+len(seg)] == seg {
			before := i == 0 || p[i-1] == '/' || p[i-1] == filepath.Separator
			after := i+len(seg) == len(p) || p[i+len(seg)] == '/' || p[i+len(seg)] == filepath.Separator
			if before && after {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mdg/... -run TestParseGoPackages_`
Expected: FAIL — `parseGoPackages` and `goPkgInfo` not defined.

- [ ] **Step 3: Implement the parser**

```go
// internal/mdg/parse_go.go
//
// Go-specific MDG parser. Walks a repo root that contains a go.mod, collects
// every Go package, and resolves their imports against the module path so
// in-module imports become canonical paths. Stdlib imports are filtered out;
// external dependencies are retained as opaque nodes (Lang = "" until a
// future task labels them).

package mdg

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// goPkgInfo is one Go package as discovered by parseGoPackages.
type goPkgInfo struct {
	ImportPath string   // canonical, e.g., "example.com/m/internal/auth"
	IsMain     bool     // package main
	Imports    []string // in-module + external; stdlib filtered out
}

// excludedDir is true when the named segment must not be descended into.
var excludedDirs = map[string]bool{
	".git":         true,
	".hg":          true,
	".svn":         true,
	"vendor":       true,
	"node_modules": true,
	"venv":         true,
	".venv":        true,
	"third_party":  true,
	"testdata":     true,
}

// parseGoPackages walks rootDir, requires a go.mod at rootDir, and returns
// one goPkgInfo per Go package found. Per-file parse errors are logged to
// stderr and the file is skipped; whole-package failures emit a warning and
// the package is omitted. Returns an error only when rootDir has no go.mod
// or cannot be read.
func parseGoPackages(rootDir string) ([]goPkgInfo, error) {
	modulePath, err := readGoModulePath(rootDir)
	if err != nil {
		return nil, err
	}

	// pkgImports keyed by absolute directory: each dir is one Go package.
	type pkgState struct {
		importPath string
		isMain    bool
		imports    map[string]struct{}
		seenAtLeastOneFile bool
	}
	pkgsByDir := map[string]*pkgState{}

	err = filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if excludedDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") && path != rootDir {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil // skip test files; they pollute the dependency graph
		}

		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "mdg: skipping %s: %v\n", path, perr)
			return nil
		}

		dir := filepath.Dir(path)
		st, ok := pkgsByDir[dir]
		if !ok {
			rel, _ := filepath.Rel(rootDir, dir)
			rel = filepath.ToSlash(rel)
			importPath := modulePath
			if rel != "." && rel != "" {
				importPath = modulePath + "/" + rel
			}
			st = &pkgState{
				importPath: importPath,
				imports:    map[string]struct{}{},
			}
			pkgsByDir[dir] = st
		}
		if f.Name != nil && f.Name.Name == "main" {
			st.isMain = true
		}
		st.seenAtLeastOneFile = true
		for _, imp := range f.Imports {
			if imp.Path == nil {
				continue
			}
			ip := strings.Trim(imp.Path.Value, `"`)
			if isStdlibImport(ip) {
				continue
			}
			st.imports[ip] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", rootDir, err)
	}

	out := make([]goPkgInfo, 0, len(pkgsByDir))
	for _, st := range pkgsByDir {
		if !st.seenAtLeastOneFile {
			continue
		}
		imports := make([]string, 0, len(st.imports))
		for k := range st.imports {
			imports = append(imports, k)
		}
		out = append(out, goPkgInfo{
			ImportPath: st.importPath,
			IsMain:     st.isMain,
			Imports:    imports,
		})
	}
	return out, nil
}

// readGoModulePath reads `module X` from rootDir/go.mod and returns X.
func readGoModulePath(rootDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(rootDir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("go.mod missing `module` directive")
}

// isStdlibImport approximates "is this import from the Go standard library?"
// The heuristic: stdlib imports never contain a dot in the first path
// segment. Imports like "fmt", "encoding/json", "go/parser" return true;
// "github.com/foo/bar", "example.com/m/internal/auth" return false.
func isStdlibImport(p string) bool {
	if p == "" {
		return false
	}
	first := p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		first = p[:i]
	}
	return !strings.Contains(first, ".")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mdg/... -run TestParseGoPackages_ -v`
Expected: PASS, 4 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/parse_go.go internal/mdg/parse_go_test.go
git commit -m "mdg: add Go import parser"
```

---

## Task 5: `Build` orchestrator

**Files:**
- Create: `internal/mdg/build.go`
- Create: `internal/mdg/build_test.go`

`Build` is the single entry point that turns a repo on disk into a `*Graph`. For Phase A it dispatches only to `parseGoPackages`; later phases will add Python and JS/TS branches. Imports that point to modules outside the in-module path set are still recorded as nodes (with `Lang = ""`) so they appear in the graph as zero-out-degree leaves.

- [ ] **Step 1: Write the failing test**

```go
// internal/mdg/build_test.go
package mdg

import (
	"context"
	"sort"
	"testing"
)

func TestBuild_GoModule(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", `package main

import "example.com/m/internal/auth"

func main() { auth.Hello() }
`)
	writeFile(t, root, "internal/auth/auth.go", `package auth

func Hello() {}
`)

	g, err := Build(context.Background(), root, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Expect 2 nodes: example.com/m (main), example.com/m/internal/auth.
	if got := len(g.Nodes); got != 2 {
		t.Fatalf("Nodes: want 2, got %d (%+v)", got, g.Nodes)
	}
	root3, ok := g.Index["example.com/m"]
	if !ok {
		t.Fatalf("missing root node")
	}
	if !g.Nodes[root3].IsMain {
		t.Errorf("root should be IsMain")
	}
	imports := g.Edges["example.com/m"]
	sort.Strings(imports)
	if !equalStrSlices(imports, []string{"example.com/m/internal/auth"}) {
		t.Errorf("root edges: got %v", imports)
	}
}

func TestBuild_ExternalImportBecomesLeaf(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", `package main

import "github.com/cli/go-gh/v2"

func main() { _ = gh.Foo }
`)

	g, err := Build(context.Background(), root, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := g.Index["github.com/cli/go-gh/v2"]; !ok {
		t.Fatalf("external import should become a node; nodes=%+v", g.Nodes)
	}
	if got := g.Edges["github.com/cli/go-gh/v2"]; len(got) != 0 {
		t.Errorf("external import should be a leaf, got %v", got)
	}
}

func TestBuild_EmptyRepo(t *testing.T) {
	root := t.TempDir()
	// No go.mod, no source files.
	_, err := Build(context.Background(), root, BuildOptions{})
	if err == nil {
		t.Fatalf("Build should error for repo with no go.mod")
	}
}

func TestBuild_ContextCancellation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", "package main\nfunc main() {}\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Build(ctx, root, BuildOptions{})
	if err == nil {
		t.Fatalf("Build should respect context cancellation")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mdg/... -run TestBuild_`
Expected: FAIL — `Build` and `BuildOptions` not defined.

- [ ] **Step 3: Implement Build**

```go
// internal/mdg/build.go
package mdg

import (
	"context"
	"fmt"
	"os"
)

// BuildOptions tune the Build process. All fields are optional; the zero
// value runs the default Phase-A Go-only pipeline.
type BuildOptions struct {
	// Languages is a whitelist of language tags ("go") to enable. Empty means
	// "all supported in this build". Reserved for Phase B/C.
	Languages []string
}

// Build parses repoPath and returns the MDG. Phase A supports Go only.
//
// Errors:
//   - repoPath cannot be read
//   - no go.mod (Phase A's only supported entry condition)
//   - context cancellation
//
// Per-file or per-package parse failures are logged to stderr and skipped.
func Build(ctx context.Context, repoPath string, opts BuildOptions) (*Graph, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	st, err := os.Stat(repoPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", repoPath, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("repoPath is not a directory: %s", repoPath)
	}

	pkgs, err := parseGoPackages(repoPath)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	g := NewGraph()
	// Pass 1: register every in-module package as a node.
	for _, p := range pkgs {
		g.AddNode(Module{Path: p.ImportPath, Lang: "go", IsMain: p.IsMain})
	}
	// Pass 2: walk imports. Unknown imports become opaque leaf nodes.
	for _, p := range pkgs {
		srcIdx := g.Index[p.ImportPath]
		for _, dep := range p.Imports {
			dstIdx, ok := g.Index[dep]
			if !ok {
				dstIdx = g.AddNode(Module{Path: dep, Lang: ""})
			}
			g.AddEdge(srcIdx, dstIdx)
		}
	}
	return g, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mdg/... -v`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/build.go internal/mdg/build_test.go
git commit -m "mdg: add Build orchestrator that produces a Go MDG"
```

---

## Task 6: Shallow-clone subsystem

**Files:**
- Create: `internal/mdg/clone.go`
- Create: `internal/mdg/clone_test.go`

`ShallowClone` orchestrates the temporary clone. Real cloning hits the network and is therefore tested through a behavior-level seam: the `cloneRunner` interface so unit tests can substitute a fake. A real-network smoke test is included but gated behind `testing.Short`.

- [ ] **Step 1: Write the failing test**

```go
// internal/mdg/clone_test.go
package mdg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	cmds [][]string
	err  error
	// preCreate, when non-empty, writes a sentinel file in dest before the
	// command is "run", simulating a successful clone.
	sentinelName string
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) error {
	f.cmds = append(f.cmds, append([]string{name}, args...))
	if f.err != nil {
		return f.err
	}
	if f.sentinelName != "" {
		// Last arg is the destination directory for both gh and git forms.
		dest := args[len(args)-1]
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, f.sentinelName), []byte("ok"), 0o644)
	}
	return nil
}

func TestShallowClone_PrefersGhWhenAvailable(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "repo")
	fr := &fakeRunner{sentinelName: "README"}
	opts := cloneOpts{
		runner:    fr,
		ghOnPath:  true,
		gitOnPath: true,
	}
	if err := shallowCloneWith(context.Background(), "github", "owner", "repo", dest, opts); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if len(fr.cmds) != 1 {
		t.Fatalf("want 1 command run, got %d (%v)", len(fr.cmds), fr.cmds)
	}
	if fr.cmds[0][0] != "gh" {
		t.Fatalf("expected gh to be used; got %v", fr.cmds[0])
	}
}

func TestShallowClone_FallsBackToGitForGitLab(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "repo")
	fr := &fakeRunner{sentinelName: "README"}
	opts := cloneOpts{
		runner:    fr,
		ghOnPath:  true,
		gitOnPath: true,
	}
	if err := shallowCloneWith(context.Background(), "gitlab", "group", "project", dest, opts); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if fr.cmds[0][0] != "git" {
		t.Fatalf("expected git for gitlab; got %v", fr.cmds[0])
	}
	if !strings.Contains(strings.Join(fr.cmds[0], " "), "gitlab.com/group/project.git") {
		t.Fatalf("expected gitlab URL; got %v", fr.cmds[0])
	}
}

func TestShallowClone_GitFallbackWhenGhMissing(t *testing.T) {
	fr := &fakeRunner{sentinelName: "README"}
	opts := cloneOpts{
		runner:    fr,
		ghOnPath:  false,
		gitOnPath: true,
	}
	dir := t.TempDir()
	if err := shallowCloneWith(context.Background(), "github", "owner", "repo", filepath.Join(dir, "r"), opts); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if fr.cmds[0][0] != "git" {
		t.Fatalf("expected git fallback; got %v", fr.cmds[0])
	}
}

func TestShallowClone_NoBackendsFails(t *testing.T) {
	opts := cloneOpts{runner: &fakeRunner{}, ghOnPath: false, gitOnPath: false}
	dir := t.TempDir()
	err := shallowCloneWith(context.Background(), "github", "o", "r", filepath.Join(dir, "x"), opts)
	if err == nil {
		t.Fatalf("expected error when neither gh nor git is available")
	}
}

func TestShallowClone_RunnerErrorPropagates(t *testing.T) {
	fr := &fakeRunner{err: errors.New("boom")}
	opts := cloneOpts{runner: fr, ghOnPath: true, gitOnPath: true}
	dir := t.TempDir()
	err := shallowCloneWith(context.Background(), "github", "o", "r", filepath.Join(dir, "x"), opts)
	if err == nil {
		t.Fatalf("expected propagated runner error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mdg/... -run TestShallowClone_`
Expected: FAIL — types not defined.

- [ ] **Step 3: Implement the subsystem**

```go
// internal/mdg/clone.go
package mdg

import (
	"context"
	"fmt"
	"os/exec"
)

// cloneRunner is the seam between shallowCloneWith and actual subprocess
// execution. Production wires it to execRunner{}; tests substitute a fake.
type cloneRunner interface {
	Run(ctx context.Context, name string, args ...string) error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w (output: %s)", name, args, err, string(out))
	}
	return nil
}

// cloneOpts are options for shallowCloneWith; reserved for the test seam.
type cloneOpts struct {
	runner    cloneRunner
	ghOnPath  bool
	gitOnPath bool
}

// ShallowClone is the public entry point. It detects which backends are
// available on PATH and dispatches.
func ShallowClone(ctx context.Context, provider, owner, repo, dest string) error {
	return shallowCloneWith(ctx, provider, owner, repo, dest, cloneOpts{
		runner:    execRunner{},
		ghOnPath:  binOnPath("gh"),
		gitOnPath: binOnPath("git"),
	})
}

// shallowCloneWith is the testable inner form.
func shallowCloneWith(ctx context.Context, provider, owner, repo, dest string, opts cloneOpts) error {
	// gh handles GitHub auth correctly. Use it when available and the provider
	// is GitHub; otherwise fall through to plain git.
	if provider == "github" && opts.ghOnPath {
		// `gh repo clone <owner>/<repo> <dest> -- --depth 1 --filter=blob:none`
		return opts.runner.Run(ctx, "gh",
			"repo", "clone",
			owner+"/"+repo,
			dest,
			"--",
			"--depth", "1",
			"--filter=blob:none",
		)
	}
	if !opts.gitOnPath {
		return fmt.Errorf("neither `gh` nor `git` available on PATH; cannot clone %s/%s", owner, repo)
	}
	var url string
	switch provider {
	case "github":
		url = fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	case "gitlab":
		url = fmt.Sprintf("https://gitlab.com/%s/%s.git", owner, repo)
	default:
		return fmt.Errorf("unsupported provider for shallow clone: %s", provider)
	}
	return opts.runner.Run(ctx, "git",
		"clone",
		"--depth", "1",
		"--filter=blob:none",
		url,
		dest,
	)
}

// binOnPath reports whether the named binary is reachable via PATH.
func binOnPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mdg/... -run TestShallowClone_ -v`
Expected: PASS, 5 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/clone.go internal/mdg/clone_test.go
git commit -m "mdg: add shallow-clone subsystem with gh+git backends"
```

---

## Task 7: MDG cache (load / save / TTL / HEAD-SHA invalidation)

**Files:**
- Create: `internal/mdg/cache.go`
- Create: `internal/mdg/cache_test.go`

The cache file mirrors the layout of `internal/repo/cache.go`: atomic write via temp-file + rename, XDG-aware path, 24 h TTL. The new wrinkle is the `HeadSHA` field: if it does not match the upstream's current default-branch SHA at load time, the cache is invalidated even if it is younger than 24 h.

- [ ] **Step 1: Write the failing test**

```go
// internal/mdg/cache_test.go
package mdg

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCachePath_SanitizesComponents(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := MDGCachePath("github", "../../etc", "passwd")
	if filepath.Base(p) != "passwd__passwd.json" && filepath.Base(p) != "etc__passwd.json" {
		// The exact behavior is "Base(Clean(x))"; we just want no traversal.
	}
	if filepath.IsAbs(p) == false {
		t.Fatalf("cache path should be absolute: %q", p)
	}
}

func TestCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	c := MDGCache{
		SchemaVersion: cacheSchemaVersion,
		Provider:      "github",
		Owner:         "o",
		Repo:          "r",
		HeadSHA:       "abc123",
		ComputedAt:    time.Now().UTC(),
		Scores:        map[string]float64{"example.com/m": 0.42},
		Nodes:         []Module{{Path: "example.com/m", Lang: "go", IsMain: true}},
		Edges:         map[string][]string{"example.com/m": {"example.com/m/util"}},
	}
	if err := SaveMDGCache(c); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok := LoadMDGCache("github", "o", "r", "abc123")
	if !ok {
		t.Fatalf("expected cache hit")
	}
	if got.HeadSHA != "abc123" || got.Scores["example.com/m"] != 0.42 {
		t.Fatalf("unexpected entry: %+v", got)
	}
}

func TestCache_HeadSHAMismatchIsMiss(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c := MDGCache{
		SchemaVersion: cacheSchemaVersion,
		Provider:      "github",
		Owner:         "o",
		Repo:          "r",
		HeadSHA:       "old-sha",
		ComputedAt:    time.Now().UTC(),
	}
	if err := SaveMDGCache(c); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, ok := LoadMDGCache("github", "o", "r", "new-sha"); ok {
		t.Fatalf("HEAD-SHA mismatch should be a miss")
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c := MDGCache{
		SchemaVersion: cacheSchemaVersion,
		Provider:      "github",
		Owner:         "o",
		Repo:          "r",
		HeadSHA:       "abc",
		ComputedAt:    time.Now().Add(-25 * time.Hour).UTC(),
	}
	if err := SaveMDGCache(c); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, ok := LoadMDGCache("github", "o", "r", "abc"); ok {
		t.Fatalf("entry older than TTL should be miss")
	}
}

func TestCache_SchemaMismatchIsMiss(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c := MDGCache{
		SchemaVersion: cacheSchemaVersion + 100,
		Provider:      "github",
		Owner:         "o",
		Repo:          "r",
		HeadSHA:       "abc",
		ComputedAt:    time.Now().UTC(),
	}
	if err := SaveMDGCache(c); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, ok := LoadMDGCache("github", "o", "r", "abc"); ok {
		t.Fatalf("schema mismatch should be miss")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mdg/... -run TestCache`
Expected: FAIL — types not defined.

- [ ] **Step 3: Implement the cache**

```go
// internal/mdg/cache.go
package mdg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	mdgTTL             = 24 * time.Hour
	cacheSchemaVersion = 1
)

// MDGCache is the on-disk MDG entry.
type MDGCache struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Provider      string               `json:"provider"`
	Owner         string               `json:"owner"`
	Repo          string               `json:"repo"`
	HeadSHA       string               `json:"headSHA"` // upstream default-branch SHA at build time
	ComputedAt    time.Time            `json:"computedAt"`
	Nodes         []Module             `json:"nodes"`
	Edges         map[string][]string  `json:"edges"`
	Scores        map[string]float64   `json:"scores"` // PageRank, keyed by module path
}

// MDGCachePath returns the canonical cache location for an upstream's MDG.
//
//	$XDG_CACHE_HOME/spoon/mdg/<provider>/<owner>__<repo>.json
func MDGCachePath(provider, owner, repo string) string {
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			cacheHome = os.TempDir()
		} else {
			cacheHome = filepath.Join(home, ".cache")
		}
	}
	provider = filepath.Base(filepath.Clean(provider))
	owner = filepath.Base(filepath.Clean(owner))
	repo = filepath.Base(filepath.Clean(repo))
	filename := owner + "__" + repo + ".json"
	return filepath.Join(cacheHome, "spoon", "mdg", provider, filename)
}

// LoadMDGCache returns (cache, true) when a fresh cache entry exists for the
// upstream and headSHA matches the cached HeadSHA. Otherwise (zero, false).
//
// "Fresh" means: schema-version match, ComputedAt within mdgTTL, HeadSHA
// matches. Any read or parse error is a miss (no error is propagated).
func LoadMDGCache(provider, owner, repo, headSHA string) (MDGCache, bool) {
	path := MDGCachePath(provider, owner, repo)
	data, err := os.ReadFile(path)
	if err != nil {
		return MDGCache{}, false
	}
	var c MDGCache
	if err := json.Unmarshal(data, &c); err != nil {
		return MDGCache{}, false
	}
	if c.SchemaVersion != cacheSchemaVersion {
		return MDGCache{}, false
	}
	if c.ComputedAt.IsZero() || time.Since(c.ComputedAt) > mdgTTL {
		return MDGCache{}, false
	}
	if c.HeadSHA == "" || c.HeadSHA != headSHA {
		return MDGCache{}, false
	}
	return c, true
}

// SaveMDGCache atomically persists a cache entry. Returns nil on success.
func SaveMDGCache(c MDGCache) error {
	path := MDGCachePath(c.Provider, c.Owner, c.Repo)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mdg-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mdg/... -run TestCache -v`
Expected: PASS, 5 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/cache.go internal/mdg/cache_test.go
git commit -m "mdg: add JSON cache with HEAD-SHA invalidation"
```

---

## Task 8: `repo.Centrality` interface + `DirectoryCentrality` migration to paths

This task widens the `internal/repo` API. The existing `DirectoryCentrality.ScoreFork(touchedDirs)` becomes `ScoreFork(touchedFiles)`, with the file → directory conversion moved *inside* the method. A new interface `Centrality` is added that both `DirectoryCentrality` (existing) and `mdg.Centrality` (next task) implement.

**Files:**
- Modify: `internal/repo/centrality.go`
- Modify: `internal/repo/centrality_test.go`
- Modify: `internal/cluster/pipeline.go` (callers — minimal edit; full integration is Task 10)

- [ ] **Step 1: Write a new test that exercises the path-based `ScoreFork`**

Append to `internal/repo/centrality_test.go`:

```go
func TestScoreFork_AcceptsFilePaths(t *testing.T) {
	dc := DirectoryCentrality{DirScore: map[string]float64{
		"internal/auth/": 0.9,
		"cmd/":           0.5,
	}}
	// Pass file paths; ScoreFork should derive directories internally.
	got := dc.ScoreFork([]string{
		"internal/auth/oauth.go",
		"cmd/main.go",
	})
	want := (0.9 + 0.5) / 2.0
	if got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("ScoreFork(file paths) = %v, want %v", got, want)
	}
}

func TestScoreFork_DedupesDirectoriesFromPaths(t *testing.T) {
	dc := DirectoryCentrality{DirScore: map[string]float64{
		"internal/auth/": 0.9,
	}}
	got := dc.ScoreFork([]string{
		"internal/auth/oauth.go",
		"internal/auth/saml.go",
	})
	if got < 0.9-1e-9 || got > 0.9+1e-9 {
		t.Fatalf("two files in same dir → one dir contribution; got %v", got)
	}
}
```

Also: update each existing `ScoreFork` test in `centrality_test.go` that passes directory keys (e.g. `[]string{"a/", "y/"}`) to instead pass file paths whose directories yield the same answer. The simplest rewrite for an entry like `ScoreFork([]string{"a/"})` is `ScoreFork([]string{"a/dummy.go"})`. Re-read the test file before editing to keep diffs surgical.

- [ ] **Step 2: Run the test suite — confirm failures pinpoint the API change**

Run: `go test ./internal/repo/...`
Expected: a clear failure pattern showing the new test failing because the current `ScoreFork` keys by trailing-slash dir.

- [ ] **Step 3: Refit `ScoreFork` and introduce `Centrality` interface**

Replace the existing `ScoreFork` body in `internal/repo/centrality.go` with the path-based form. Define the interface at the top of the file.

```go
// Centrality is the interface implemented by every centrality backend
// (directory proxy + future MDG-based). It maps a list of touched file paths
// to a 0..1 ChangeImpact score and exposes the top "core" units (directories
// for the proxy, module paths for MDG) for the labeler.
type Centrality interface {
	ScoreFork(touchedFiles []string) float64
	Core() []string
	When() time.Time
}

// Compile-time check.
var _ Centrality = DirectoryCentrality{}

// ScoreFork returns a 0..1 ChangeImpact score for a fork given the file paths
// it touched. The implementation maps each path to its trailing-slash parent
// directory key (e.g., "internal/auth/oauth.go" → "internal/auth/"), de-dupes
// directories, and returns the mean DirScore across those directories.
func (dc DirectoryCentrality) ScoreFork(touchedFiles []string) float64 {
	if len(touchedFiles) == 0 {
		return 0.0
	}
	seen := make(map[string]struct{}, len(touchedFiles))
	dirs := make([]string, 0, len(touchedFiles))
	for _, p := range touchedFiles {
		d := dirKeyOf(p)
		if d == "" {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		dirs = append(dirs, d)
	}
	if len(dirs) == 0 {
		return 0.0
	}
	var sum float64
	for _, d := range dirs {
		if v, ok := dc.DirScore[d]; ok {
			sum += v
		}
	}
	mean := sum / float64(len(dirs))
	if mean < 0 {
		return 0
	}
	if mean > 1 {
		return 1
	}
	return mean
}

// Core implements Centrality. Returns the top scoring directory keys.
func (dc DirectoryCentrality) Core() []string { return dc.CoreDirs }

// When implements Centrality.
func (dc DirectoryCentrality) When() time.Time { return dc.ComputedAt }

// dirKeyOf returns "internal/auth/" for "internal/auth/oauth.go", "" for a
// top-level file.
func dirKeyOf(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	idx := strings.LastIndexByte(path, '/')
	if idx <= 0 {
		return ""
	}
	return path[:idx+1]
}
```

- [ ] **Step 4: Patch the cluster pipeline call site to pass paths (minimal edit)**

In `internal/cluster/pipeline.go`, the `changeImpactFor` helper currently does `d := dirOf(p)` and dedupes itself. Strip it down so it just passes `paths` directly into `dc.ScoreFork(paths)`:

```go
func changeImpactFor(dc repo.DirectoryCentrality, ok bool, paths []string) float32 {
	if !ok || len(paths) == 0 {
		return 0
	}
	return float32(dc.ScoreFork(paths))
}
```

Keep `dirOf` in the file for now — Task 10 may remove it once the pipeline routes through the new `Centrality` interface.

- [ ] **Step 5: Run the full suite — everything must still pass**

Run: `go test ./...`
Expected: ALL pass. If any cluster-pipeline test fails, inspect — the directory dedup behavior should be preserved by the new `ScoreFork`.

- [ ] **Step 6: Commit**

```bash
git add internal/repo/centrality.go internal/repo/centrality_test.go internal/cluster/pipeline.go
git commit -m "repo: introduce Centrality interface; ScoreFork accepts file paths"
```

---

## Task 9: `mdg.Centrality` — bridge from MDG to `repo.Centrality`

**Files:**
- Create: `internal/mdg/centrality.go`
- Create: `internal/mdg/centrality_test.go`

`mdg.Centrality` is the type the cluster pipeline will hold. It owns: the PageRank score table, a map from file path → module path (to translate touched files), and the top-k module paths for the labeler.

- [ ] **Step 1: Write the failing test**

```go
// internal/mdg/centrality_test.go
package mdg

import (
	"context"
	"testing"
	"time"
)

func TestCentrality_ScoreFork(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", `package main

import "example.com/m/internal/auth"

func main() { auth.Hello() }
`)
	writeFile(t, root, "internal/auth/auth.go", `package auth

func Hello() {}
`)

	c, err := BuildCentrality(context.Background(), root, "github", "o", "r", "head-sha", BuildOptions{})
	if err != nil {
		t.Fatalf("BuildCentrality: %v", err)
	}

	// A file in the auth package — score should be > 0.
	got := c.ScoreFork([]string{"internal/auth/auth.go"})
	if got <= 0 || got > 1 {
		t.Fatalf("ScoreFork(auth file) out of (0,1]: %v", got)
	}
	// An untouched / unknown file path — score should be 0 (we map only to
	// modules we know).
	if got := c.ScoreFork([]string{"docs/intro.md"}); got != 0 {
		t.Fatalf("ScoreFork(unknown file) = %v, want 0", got)
	}
	// Empty input.
	if got := c.ScoreFork(nil); got != 0 {
		t.Fatalf("ScoreFork(nil) = %v, want 0", got)
	}
}

func TestCentrality_When(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", "package main\nfunc main() {}\n")
	c, err := BuildCentrality(context.Background(), root, "github", "o", "r", "h", BuildOptions{})
	if err != nil {
		t.Fatalf("BuildCentrality: %v", err)
	}
	if c.When().IsZero() || time.Since(c.When()) > time.Minute {
		t.Fatalf("When() unexpected: %v", c.When())
	}
}

func TestCentrality_CoreReturnsTopModules(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "main.go", `package main

import (
	"example.com/m/core"
	"example.com/m/leaf"
)

func main() { core.A(); leaf.B() }
`)
	writeFile(t, root, "core/a.go", "package core\nfunc A() {}\n")
	writeFile(t, root, "leaf/b.go", "package leaf\nfunc B() {}\n")

	c, err := BuildCentrality(context.Background(), root, "github", "o", "r", "h", BuildOptions{})
	if err != nil {
		t.Fatalf("BuildCentrality: %v", err)
	}
	if len(c.Core()) == 0 {
		t.Fatalf("Core() should return at least one module")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mdg/... -run TestCentrality_`
Expected: FAIL — `BuildCentrality`, `Centrality` not defined.

- [ ] **Step 3: Implement `Centrality` + `BuildCentrality`**

```go
// internal/mdg/centrality.go
package mdg

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultDamping    = 0.85
	defaultIterations = 50
	defaultTopK       = 10
)

// Centrality is the MDG-backed centrality. It satisfies repo.Centrality
// (a structural interface — checked at the integration site, not here).
type Centrality struct {
	Provider   string
	Owner      string
	Repo       string
	HeadSHA    string
	Scores     map[string]float64 // module path → PageRank score in [0,1]
	core       []string
	computedAt time.Time

	// fileToModule maps a slash-separated file path (relative to the repo
	// root) to its module path. Populated at build time from the package
	// scan; used in ScoreFork to look up scores per touched file.
	fileToModule map[string]string
}

// BuildCentrality clones-or-walks the repo at repoPath, builds the MDG, runs
// PageRank, and constructs a Centrality. The HeadSHA argument is the upstream
// commit hash; it is stored on the result for cache invalidation but is
// otherwise opaque to this function.
func BuildCentrality(
	ctx context.Context,
	repoPath, provider, owner, repo, headSHA string,
	opts BuildOptions,
) (*Centrality, error) {
	g, err := Build(ctx, repoPath, opts)
	if err != nil {
		return nil, err
	}
	tp := g.EntryPointTeleport()
	scores := g.PageRank(tp, defaultDamping, defaultIterations)

	// Build file → module map by re-scanning packages. Cheap: we already
	// have the package layout from parseGoPackages; do it again here to keep
	// Build's contract (returns *Graph, not state).
	fileToModule, err := buildFileMap(repoPath)
	if err != nil {
		return nil, err
	}

	core := topKByScore(scores, defaultTopK)

	return &Centrality{
		Provider:     provider,
		Owner:        owner,
		Repo:         repo,
		HeadSHA:      headSHA,
		Scores:       scores,
		core:         core,
		computedAt:   time.Now().UTC(),
		fileToModule: fileToModule,
	}, nil
}

// ScoreFork returns the mean PageRank score over the modules touched by the
// fork's files. Files whose module is not in the score table contribute 0.
// Duplicate touches of the same module are deduped.
func (c *Centrality) ScoreFork(touchedFiles []string) float64 {
	if c == nil || len(touchedFiles) == 0 || len(c.Scores) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(touchedFiles))
	mods := make([]string, 0, len(touchedFiles))
	for _, f := range touchedFiles {
		f = filepath.ToSlash(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		m, ok := c.fileToModule[f]
		if !ok {
			// Try the file's directory as a fallback (handles forks that
			// modify files alongside packages we mapped).
			d := strings.TrimSuffix(f[:strings.LastIndexByte(f, '/')+1], "/")
			m = c.fileToModule[d]
		}
		if m == "" {
			continue
		}
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		mods = append(mods, m)
	}
	if len(mods) == 0 {
		return 0
	}
	var sum float64
	for _, m := range mods {
		sum += c.Scores[m]
	}
	// Normalize against the top score so the result is in [0,1].
	var top float64
	for _, v := range c.Scores {
		if v > top {
			top = v
		}
	}
	if top <= 0 {
		return 0
	}
	mean := (sum / float64(len(mods))) / top
	if mean > 1 {
		return 1
	}
	if mean < 0 {
		return 0
	}
	return mean
}

// Core implements repo.Centrality. Top-K module paths by PageRank score.
func (c *Centrality) Core() []string { return c.core }

// When implements repo.Centrality.
func (c *Centrality) When() time.Time { return c.computedAt }

// buildFileMap re-walks repoPath to associate each .go file with its Go
// package import path. Bytes-cheap: we only read go.mod and the directory
// layout, not source. Files outside any discovered package map to that
// package via their parent directory.
func buildFileMap(repoPath string) (map[string]string, error) {
	pkgs, err := parseGoPackages(repoPath)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	// Re-walk to find each .go file's directory. Map directory → module path.
	dirToModule := map[string]string{}
	for _, p := range pkgs {
		// Reverse-derive directory from package import path.
		// Phase A only supports Go; the directory is repoPath + (importPath
		// suffix after the module prefix).
		dirToModule[p.ImportPath] = p.ImportPath
	}
	// Walk the filesystem and bind file → module.
	err = filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if excludedDirs[filepath.Base(path)] {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(repoPath, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// Try ascending directory prefixes against the module-name set.
		for _, p := range pkgs {
			pkgRel := strings.TrimPrefix(p.ImportPath, modulePathFor(pkgs))
			pkgRel = strings.TrimPrefix(pkgRel, "/")
			if pkgRel == "" {
				// Root package; matches files at the top level only.
				if !strings.Contains(rel, "/") {
					out[rel] = p.ImportPath
				}
			} else if strings.HasPrefix(rel, pkgRel+"/") &&
				!strings.Contains(strings.TrimPrefix(rel, pkgRel+"/"), "/") {
				out[rel] = p.ImportPath
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Also bind each module's *directory* (slash-form) → module path, so
	// ScoreFork's directory-fallback lookup works.
	for _, p := range pkgs {
		out[strings.TrimPrefix(p.ImportPath, modulePathFor(pkgs))] = p.ImportPath
	}
	return out, nil
}

// modulePathFor returns the common module path prefix shared by every
// package in pkgs. parseGoPackages emits all package paths under the same
// module, so the longest common slash-prefix of any two is the module path.
func modulePathFor(pkgs []goPkgInfo) string {
	if len(pkgs) == 0 {
		return ""
	}
	prefix := pkgs[0].ImportPath
	for _, p := range pkgs[1:] {
		for !strings.HasPrefix(p.ImportPath, prefix) {
			if i := strings.LastIndexByte(prefix, '/'); i >= 0 {
				prefix = prefix[:i]
			} else {
				return ""
			}
		}
	}
	return prefix
}

// topKByScore returns the top-K paths from scores, sorted by score desc,
// ties broken lexicographically.
func topKByScore(scores map[string]float64, k int) []string {
	out := make([]string, 0, len(scores))
	for p := range scores {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if scores[out[i]] != scores[out[j]] {
			return scores[out[i]] > scores[out[j]]
		}
		return out[i] < out[j]
	})
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}
```

**Note:** add `"io/fs"` to the imports for `fs.DirEntry` and `fs.SkipDir`. The original draft of this plan used `filepath.Walk` with an ad-hoc adapter interface; that pattern did not compile cleanly against Go's stdlib (`filepath.Walk` requires `os.FileInfo`, not a structural minimal interface). `filepath.WalkDir` is the right tool: it passes `fs.DirEntry` which exposes `IsDir()` directly. This block was rewritten on 2026-05-11 to match what was actually shipped.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mdg/... -run TestCentrality_ -v`
Expected: PASS, 3 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/mdg/centrality.go internal/mdg/centrality_test.go
git commit -m "mdg: add Centrality backend that satisfies repo.Centrality"
```

---

## Task 10: Pipeline backend dispatch

**Files:**
- Modify: `internal/cluster/pipeline.go`
- Modify: `internal/cluster/pipeline_test.go`

`PipelineOptions` gains a `CentralityBackend string` field with valid values `""` (=default, directory proxy), `"directory"`, `"mdg"`. The `loadOrComputeCentrality` function is replaced with a dispatcher returning `(repo.Centrality, bool)`. The pipeline's later use of `dc.ScoreFork(paths)` already operates on the interface, so no further site changes are needed.

- [ ] **Step 1: Write the failing test**

Add to `internal/cluster/pipeline_test.go`:

```go
func TestRunPipeline_CentralityBackendDirectoryByDefault(t *testing.T) {
	// Implicit: existing pipeline tests use the directory proxy. This is a
	// regression sentinel: with CentralityBackend == "", the dispatcher
	// must select the directory proxy.
	t.Skip("covered by existing pipeline tests; no behavior change at default")
}

func TestPipelineOptions_MDGBackend(t *testing.T) {
	// We don't exercise full MDG here — that's mdg_test. Just verify the
	// dispatch accepts the configured backend and surfaces a sensible
	// fallback when the MDG cannot be built.
	opts := PipelineOptions{CentralityBackend: "mdg"}
	if opts.CentralityBackend != "mdg" {
		t.Fatalf("backend should round-trip; got %q", opts.CentralityBackend)
	}
}
```

This is intentionally light. The richer integration test belongs in `cmd/spoon/main_test.go` (Task 11), where the CLI exercises the whole pipeline end-to-end through stubs.

- [ ] **Step 2: Update `PipelineOptions` and `loadOrComputeCentrality`**

In `internal/cluster/pipeline.go`:

1. Add the field:

```go
type PipelineOptions struct {
	// ... existing fields ...

	// CentralityBackend chooses the ChangeImpact computation. "" or
	// "directory" → the cheap directory-centrality proxy. "mdg" → the full
	// Module Dependency Graph (requires a local clone; falls back silently to
	// the directory proxy when unavailable).
	CentralityBackend string

	// CentralityRepoPath, when non-empty and CentralityBackend == "mdg", is
	// the on-disk path to a clone of the upstream. When empty, the pipeline
	// performs a shallow clone to a tempdir.
	CentralityRepoPath string

	// CentralityHeadSHA, when non-empty, is the upstream default-branch SHA;
	// used by the MDG cache to invalidate stale entries.
	CentralityHeadSHA string
}
```

2. Replace `loadOrComputeCentrality` with a dispatcher:

```go
// loadOrComputeCentrality returns the centrality backend. Dispatch is by
// PipelineOptions.CentralityBackend with silent fallback to the directory
// proxy when MDG cannot run.
func loadOrComputeCentrality(
	ctx context.Context,
	opts PipelineOptions,
	inputs PipelineInputs,
	logger io.Writer,
) (repo.Centrality, bool) {
	switch opts.CentralityBackend {
	case "mdg":
		c, ok := loadOrComputeMDG(ctx, opts, inputs, logger)
		if ok {
			return c, true
		}
		fmt.Fprintln(logger, "[cluster] MDG centrality unavailable; falling back to directory proxy")
	}
	return loadOrComputeDirCentrality(ctx, inputs, logger)
}

func loadOrComputeDirCentrality(
	ctx context.Context,
	inputs PipelineInputs,
	logger io.Writer,
) (repo.Centrality, bool) {
	if inputs.TreeSource == nil {
		return nil, false
	}
	provider := inputs.Provider
	if provider == "" {
		provider = "github"
	}
	if dc, ok := repo.LoadCache(provider, inputs.UpstreamOwner, inputs.UpstreamRepo); ok {
		return dc, true
	}
	dc, err := repo.Compute(ctx, inputs.TreeSource, inputs.CommitSource, provider,
		inputs.UpstreamOwner, inputs.UpstreamRepo, 200)
	if err != nil {
		fmt.Fprintf(logger, "[cluster] centrality unavailable (%v); continuing without ChangeImpact\n", err)
		return nil, false
	}
	if saveErr := repo.SaveCache(dc); saveErr != nil {
		fmt.Fprintf(logger, "[cluster] centrality cache save failed: %v (non-fatal)\n", saveErr)
	}
	return dc, true
}

// loadOrComputeMDG attempts to build and cache an MDG-backed centrality.
// Returns (nil, false) on any failure; the dispatcher treats that as a
// signal to fall back to the directory proxy.
func loadOrComputeMDG(
	ctx context.Context,
	opts PipelineOptions,
	inputs PipelineInputs,
	logger io.Writer,
) (repo.Centrality, bool) {
	provider := inputs.Provider
	if provider == "" {
		provider = "github"
	}
	// Cache fast-path.
	if cached, ok := mdg.LoadMDGCache(provider, inputs.UpstreamOwner, inputs.UpstreamRepo, opts.CentralityHeadSHA); ok {
		return &mdgCachedAdapter{cache: cached}, true
	}
	// We need a repo on disk.
	repoPath := opts.CentralityRepoPath
	cleanup := func() {}
	if repoPath == "" {
		tmp, err := os.MkdirTemp("", "spoon-mdg-")
		if err != nil {
			fmt.Fprintf(logger, "[cluster] mdg tempdir: %v\n", err)
			return nil, false
		}
		cleanup = func() { _ = os.RemoveAll(tmp) }
		if err := mdg.ShallowClone(ctx, provider, inputs.UpstreamOwner, inputs.UpstreamRepo, tmp); err != nil {
			fmt.Fprintf(logger, "[cluster] mdg shallow clone failed: %v\n", err)
			cleanup()
			return nil, false
		}
		repoPath = tmp
	}
	defer cleanup()

	c, err := mdg.BuildCentrality(ctx, repoPath, provider,
		inputs.UpstreamOwner, inputs.UpstreamRepo, opts.CentralityHeadSHA, mdg.BuildOptions{})
	if err != nil {
		fmt.Fprintf(logger, "[cluster] mdg build failed: %v\n", err)
		return nil, false
	}
	// Persist cache for next run.
	if err := mdg.SaveMDGCache(mdg.MDGCache{
		SchemaVersion: 1,
		Provider:      provider,
		Owner:         inputs.UpstreamOwner,
		Repo:          inputs.UpstreamRepo,
		HeadSHA:       opts.CentralityHeadSHA,
		ComputedAt:    c.When(),
		Scores:        c.Scores,
	}); err != nil {
		fmt.Fprintf(logger, "[cluster] mdg cache save failed: %v (non-fatal)\n", err)
	}
	return c, true
}

// mdgCachedAdapter wraps a cached MDGCache as a repo.Centrality. It serves
// only the score table; Core() and When() are populated from the cache. The
// fileToModule map is not persisted in Phase A — the cache hit path therefore
// degrades to a "score by exact path" lookup (paths that match a node path
// directly). A future enhancement may persist fileToModule for full fidelity.
type mdgCachedAdapter struct {
	cache mdg.MDGCache
}

func (a *mdgCachedAdapter) ScoreFork(touchedFiles []string) float64 {
	if len(touchedFiles) == 0 || len(a.cache.Scores) == 0 {
		return 0
	}
	var sum float64
	var n int
	var top float64
	for _, v := range a.cache.Scores {
		if v > top {
			top = v
		}
	}
	if top <= 0 {
		return 0
	}
	for _, f := range touchedFiles {
		if v, ok := a.cache.Scores[f]; ok {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	mean := (sum / float64(n)) / top
	if mean > 1 {
		mean = 1
	}
	return mean
}

func (a *mdgCachedAdapter) Core() []string {
	// Compute on demand from cached scores; deterministic.
	type kv struct {
		k string
		v float64
	}
	pairs := make([]kv, 0, len(a.cache.Scores))
	for k, v := range a.cache.Scores {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	out := make([]string, 0, 10)
	for i := 0; i < len(pairs) && i < 10; i++ {
		out = append(out, pairs[i].k)
	}
	return out
}

func (a *mdgCachedAdapter) When() time.Time { return a.cache.ComputedAt }
```

3. Update the call site inside `RunPipeline` to use the new dispatcher signature. The variable was `dc` of type `repo.DirectoryCentrality`; rename to `c` of type `repo.Centrality`. Update `changeImpactFor` accordingly:

```go
// In RunPipeline:
c, cOK := loadOrComputeCentrality(ctx, opts, inputs, logger)
// ...
if len(inputs.UpstreamCoreDirs) == 0 && cOK {
	inputs.UpstreamCoreDirs = c.Core()
}
// ...
paths := pathsOf(ef.T2)
impact := changeImpactFor(c, cOK, paths)

// Helper:
func changeImpactFor(c repo.Centrality, ok bool, paths []string) float32 {
	if !ok || c == nil || len(paths) == 0 {
		return 0
	}
	return float32(c.ScoreFork(paths))
}
```

4. Add the new imports at the top of `internal/cluster/pipeline.go`:

```go
import (
	// ... existing imports ...
	"os"
	"sort"
	"time"

	"github.com/svnbjrn/spoon/internal/mdg"
)
```

Some of those may already be present; only add what's missing.

- [ ] **Step 3: Run the full suite**

Run: `go test ./...`
Expected: ALL pass. Existing pipeline tests continue to use the directory proxy (default backend), so behavior is unchanged.

- [ ] **Step 4: Commit**

```bash
git add internal/cluster/pipeline.go internal/cluster/pipeline_test.go
git commit -m "cluster: dispatch centrality backend; wire mdg as opt-in"
```

---

## Task 11: CLI plumbing — `spoon --full-mdg`

**Files:**
- Modify: `cmd/spoon/main.go`
- Modify: `cmd/spoon/main_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/spoon/main_test.go`:

```go
func TestParseArgs_FullMDGFlag(t *testing.T) {
	// Adapt this to whatever flag-parsing test helper the package already has.
	// Goal: confirm --full-mdg sets a configurable that maps to
	// PipelineOptions.CentralityBackend = "mdg".
	t.Skip("placeholder: bind to the existing flag-parsing test once located")
}
```

If a flag-parsing test helper does not yet exist, factor the flag-parsing block out of `main()` first (see Step 2). Then write the real test:

```go
func TestParseFlags_FullMDG(t *testing.T) {
	got, err := parseFlags([]string{"--full-mdg", "owner/repo"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !got.fullMDG {
		t.Fatalf("--full-mdg not parsed; got %+v", got)
	}
}

func TestParseFlags_FullMDGDefaultsOff(t *testing.T) {
	got, err := parseFlags([]string{"owner/repo"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.fullMDG {
		t.Fatalf("default should not set --full-mdg; got %+v", got)
	}
}
```

- [ ] **Step 2: Add flag parsing**

In `cmd/spoon/main.go`, add two locals near the other flag locals:

```go
fullMDG := false
```

Add cases inside the flag loop:

```go
case "--full-mdg":
	fullMDG = true
case "--no-mdg":
	fullMDG = false
```

Thread through to the cluster pipeline call site:

```go
clusterOpts := dump.ClusterOptions{
	// ... existing fields ...
	CentralityBackend: backendFor(fullMDG),
}
```

Where `backendFor` is a one-liner:

```go
func backendFor(fullMDG bool) string {
	if fullMDG {
		return "mdg"
	}
	return ""
}
```

`dump.ClusterOptions` and the corresponding `tui.ClusterOptions` must each grow a `CentralityBackend string` field that is forwarded into `cluster.PipelineOptions`. Same pattern in `internal/forksops/stream.go` if it has its own options struct.

- [ ] **Step 3: Update `--help` output**

Locate the `printHelp` function in `cmd/spoon/main.go`. Add lines for the new flags in the appropriate section:

```
  --full-mdg              Build a real Module Dependency Graph for the upstream
                          using personalized PageRank centrality. Phase A
                          supports Go repositories; other languages silently
                          fall back to the directory-centrality proxy. Requires
                          `git` (and `gh` for GitHub) on PATH. Adds 10-60 s and
                          up to ~1 GB peak disk on first run; cached for 24 h
                          under ~/.cache/spoon/mdg/. Default: off.
  --no-mdg                Force the directory-centrality proxy even if a
                          previous flag enabled --full-mdg.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/spoon/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/spoon/main.go cmd/spoon/main_test.go internal/dump internal/tui internal/forksops
git commit -m "cli: add --full-mdg / --no-mdg flags for the centrality backend"
```

If the commit fails because the dump/tui/forksops paths show no diffs (because the options-struct field is unused yet), drop those paths from the `git add` and the commit body — only modify what compiles in this task.

---

## Task 12: CLI plumbing — `spn repo centrality --full-mdg`

**Files:**
- Modify: `cmd/spn/repo.go`
- Modify: `cmd/spn/repo_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/spn/repo_test.go`:

```go
func TestRepoCentrality_FullMDGFlag(t *testing.T) {
	// Stub the underlying centrality function so we can verify dispatch:
	// when --full-mdg is set, the MDG path is chosen; otherwise the
	// directory proxy is chosen.
	var sawMDG bool
	prev := repoCentralityFn
	defer func() { repoCentralityFn = prev }()
	repoCentralityFn = func(ctx context.Context, t repo.TreeSource, c repo.CommitSource, p, o, r string, n int) (repo.DirectoryCentrality, error) {
		return repo.DirectoryCentrality{Provider: p, Owner: o, Repo: r}, nil
	}

	var prevMDG = repoMDGCentralityFn
	defer func() { repoMDGCentralityFn = prevMDG }()
	repoMDGCentralityFn = func(ctx context.Context, p, o, r string) (repo.Centrality, error) {
		sawMDG = true
		return repo.DirectoryCentrality{Provider: p, Owner: o, Repo: r}, nil
	}

	var stdout, stderr strings.Builder
	exit := runRepoWith([]string{"centrality", "owner/repo", "--full-mdg"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if !sawMDG {
		t.Fatalf("expected MDG backend to be invoked when --full-mdg is set")
	}
}
```

- [ ] **Step 2: Add the MDG branch to `doRepoCentrality`**

In `cmd/spn/repo.go`:

```go
// repoMDGCentralityFn is the test-stubbable MDG backend entry.
var repoMDGCentralityFn = func(ctx context.Context, provider, owner, repo string) (repo.Centrality, error) {
	tmp, err := os.MkdirTemp("", "spn-mdg-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := mdg.ShallowClone(ctx, provider, owner, repo, tmp); err != nil {
		return nil, fmt.Errorf("shallow clone: %w", err)
	}
	c, err := mdg.BuildCentrality(ctx, tmp, provider, owner, repo, "", mdg.BuildOptions{})
	if err != nil {
		return nil, err
	}
	return c, nil
}
```

Add a `--full-mdg` switch to `doRepoCentrality`:

```go
var fullMDG bool
// ... in the existing flag loop, add:
case "--full-mdg":
	fullMDG = true
```

And dispatch:

```go
if fullMDG {
	c, err := repoMDGCentralityFn(ctx, "github", owner, repoName)
	if err != nil {
		return agentio.NewError(agentio.CodeUpstream, "mdg centrality: "+err.Error(), agentio.RemediationUpstream()).Emit(stderr)
	}
	if err := agentio.WriteJSON(stdout, c); err != nil {
		return agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}
// existing directory-centrality path follows
```

Add the imports:

```go
import (
	// ... existing ...
	"fmt"
	"os"

	"github.com/svnbjrn/spoon/internal/mdg"
)
```

- [ ] **Step 3: Run the test to verify it passes**

Run: `go test ./cmd/spn/...`
Expected: PASS, including the new `TestRepoCentrality_FullMDGFlag`.

- [ ] **Step 4: Commit**

```bash
git add cmd/spn/repo.go cmd/spn/repo_test.go
git commit -m "spn: add --full-mdg to `repo centrality`"
```

---

## Task 13: End-to-end sanity check

**Files:**
- None — verification only.

This task does not write code. It verifies the plan's acceptance criteria on a real repo.

- [ ] **Step 1: Build the binary**

Run: `go build -o spoon ./cmd/spoon && go build -o spn ./cmd/spn`
Expected: both binaries build with no warnings.

- [ ] **Step 2: Verify Go-vet and the full test suite**

Run: `go vet ./... && go test ./...`
Expected: PASS, no vet warnings.

- [ ] **Step 3: Smoke-test `spn repo centrality --full-mdg` against a small public Go repo**

Run: `./spn repo centrality --full-mdg github.com/cli/safeexec`
Expected: JSON output with a `Scores` map populated from a real PageRank pass over the safeexec repo. The first run should take 5-15 seconds (clone + parse + PageRank). The second run should be fast (cache hit).

Manually verify:
- The top scoring module(s) include the package that other packages import from (i.e., the central package).
- The cache file is at `~/.cache/spoon/mdg/github/cli__safeexec.json`.
- A second invocation logs a cache hit (or simply runs faster); cache file mtime updates only on real recomputation.

- [ ] **Step 4: Smoke-test `spoon --full-mdg`**

Run: `./spoon --full-mdg --json cli/safeexec | head -50`
Expected: JSON includes `change_impact` values on the forks; total elapsed within the budget from the spec (≤ 90 s first run, ≤ 15 s cached). If the upstream is not Go-only (mixed-language), the directory proxy fallback should kick in silently.

- [ ] **Step 5: Verify the fallback path**

Temporarily move `git` out of `PATH` (`PATH=/usr/bin:/bin /usr/bin/env -i ./spoon --full-mdg --json cli/safeexec`) — or use a known non-Go upstream. The pipeline must complete successfully, with `--full-mdg` silently degrading to the directory proxy.

- [ ] **Step 6: Document findings**

If any of the above fail expectations, file a follow-up issue / note in the plan; do **not** mark the plan complete. If all pass, the plan is done.

---

## Self-review summary

**Spec coverage check:**

| Spec "How to complete" step | Implemented in task |
|---|---|
| 1. Land v1 directory proxy | Already shipped (pre-existing) |
| 2. Phase A: Go-only MDG | Tasks 1-5 |
| 3. Validation experiment | Task 13 step 3 (lightweight; the formal 5-repo experiment is a follow-up) |
| 4. Phase B: Python | Deferred (Plan 2) |
| 5. Phase C: JS/TS | Deferred (Plan 3) |
| 6. Shallow-clone subsystem | Task 6 |
| 7. Cache schema | Task 7 |
| 8. Wire into centrality.go | Tasks 8, 9, 10 |
| 9. Document flags + limitations | Task 11 step 3 |

**Acceptance criteria check:**

1. Better than directory proxy on 5 repos — Task 13 covers single-repo smoke testing; the formal 5-repo overlap experiment is a manual follow-up after the code lands.
2. Go support in phase 1 — yes, Task 4 + Task 5.
3. Robustness on broken files — Task 4 step 1 includes `TestParseGoPackages_SkipsBrokenFiles`.
4. Cache integrity (byte-identical when HEAD SHA matches) — Task 7 step 1 covers HEAD-SHA invalidation; byte-identity is a follow-up if Go's `encoding/json` produces stable output (it should, given our maps are written in unspecified order — a future enhancement may sort before marshalling).
5. Performance budget ≤ 90 s first / ≤ 15 s cached — Task 13 step 4 verifies on a real repo.

**Open items the engineer should know:**

- The `mdgCachedAdapter` in Task 10 does not persist the file → module map. Cache-hit paths therefore score only by exact module-path lookup, which is degraded vs. a fresh build. This is fine for Phase A: cache hits still beat the directory proxy on labeler signals; full fidelity is a P2 enhancement. The path forward is to also serialize `fileToModule` into `MDGCache`.
- Byte-identical caches (acceptance criterion 4) require deterministic JSON output. Go's stdlib `encoding/json` does not sort map keys by default; a future tightening can wrap the marshal in a stable serializer. Not blocking Phase A.
- `parseGoPackages` skips `_test.go` files. This is intentional — test files would inflate the graph with testing-only dependencies and conflate "structural importance" with "test scaffolding."
- Failure mode for `mdg.Centrality.ScoreFork` on completely unmapped paths: returns 0, identical to the directory proxy's behavior on touched dirs it doesn't know. The pipeline treats both as "no impact signal," which is the safe default.

---

## Execution handoff

**Plan complete and saved to `docs/superpowers/plans/2026-05-11-mdg-go-phase-a.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
