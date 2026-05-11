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
