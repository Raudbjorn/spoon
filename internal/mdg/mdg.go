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
