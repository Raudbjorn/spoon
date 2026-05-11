package mdg

import (
	"math"
	"testing"
)

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
