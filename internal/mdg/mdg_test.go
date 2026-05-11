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
