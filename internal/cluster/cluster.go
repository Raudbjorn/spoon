// Package cluster performs density-based clustering over fork embeddings and
// computes a per-fork novelty score.
//
// The algorithm is single-link agglomerative clustering implemented as
// connected components over a graph whose edges connect points within
// Options.Epsilon cosine distance. Components below Options.MinClusterSize
// collapse into a synthetic "noise" pseudo-cluster.
package cluster

import (
	"math"
	"sort"
	"strconv"
)

// Point is a fork embedding tagged with the fork's identity. Vec is expected
// to be L2-normalized; Cluster does not re-normalize it.
type Point struct {
	ForkID string
	Vec    []float32
}

// Cluster groups Points that fell within Epsilon of each other.
type Cluster struct {
	ID       string
	Members  []string
	Centroid []float32
	Label    string
}

// Assignment is the per-Point clustering result.
type Assignment struct {
	ForkID  string
	Cluster string
	Novelty float64
}

// Options tunes the clustering. Zero-value fields fall back to defaults.
type Options struct {
	Epsilon        float64
	MinClusterSize int
}

const (
	defaultEpsilon        = 0.35
	defaultMinClusterSize = 3
	noiseID               = "noise"
)

// Run performs single-link agglomerative clustering with a cosine distance
// cutoff (Epsilon). Groups smaller than MinClusterSize are merged into a
// single "noise" pseudo-cluster.
//
// (The plan and task spec call this function "Cluster", but Go does not
// permit a function and type in the same package to share a name. The type
// name `Cluster` is preserved — callers will write `cluster.Cluster` for the
// struct and `cluster.Run` for the entry point.)
//
// See package doc for the full algorithm, edge cases, and determinism
// guarantees. Run does NOT mutate its inputs.
func Run(points []Point, opts Options) ([]Cluster, []Assignment) {
	if len(points) == 0 {
		return nil, nil
	}

	eps := opts.Epsilon
	if eps <= 0 {
		eps = defaultEpsilon
	}
	minSize := opts.MinClusterSize
	if minSize <= 0 {
		minSize = defaultMinClusterSize
	}

	// Index permutation that visits points in lex order of ForkID. We work
	// over this ordering so output is deterministic regardless of input order.
	// SliceStable preserves insertion order for equal ForkIDs (defensive — the
	// pipeline guards against duplicate IDs upstream but this keeps Run robust
	// in isolation).
	order := make([]int, len(points))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return points[order[i]].ForkID < points[order[j]].ForkID
	})

	n := len(points)
	uf := newUnionFind(n)

	// Build the epsilon graph. We compare every pair; n is at most a few
	// hundred forks in practice (top-N is gated upstream).
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			d := cosineDistance(points[order[i]].Vec, points[order[j]].Vec)
			if d <= eps {
				uf.union(i, j)
			}
		}
	}

	// Bucket members by component, keyed by the smallest index (in lex order)
	// in each component. Iterating i = 0..n-1 ensures `rootsInOrder` is
	// already in the desired lex order of the smallest ForkID per component.
	componentKey := make([]int, n)
	for i := range componentKey {
		componentKey[i] = -1
	}
	buckets := make(map[int][]int)
	rootsInOrder := make([]int, 0)
	for i := 0; i < n; i++ {
		r := uf.find(i)
		k := componentKey[r]
		if k == -1 {
			componentKey[r] = i
			k = i
			rootsInOrder = append(rootsInOrder, k)
		}
		buckets[k] = append(buckets[k], i)
	}

	// Split into real clusters and noise. rootsInOrder is already in lex
	// order of the smallest ForkID in each component, because we processed
	// indices in lex order and only recorded a root the first time we saw it.
	clusters := make([]Cluster, 0)
	noiseMembers := make([]string, 0)
	assignments := make([]Assignment, n)
	clusterCounter := 0

	// First pass: identify real clusters in lex order, give them IDs, build
	// centroids. Build a lookup from index → assigned cluster ID for the
	// novelty pass.
	pointCluster := make([]string, n)
	clusterCentroid := make(map[string][]float32)

	for _, root := range rootsInOrder {
		members := buckets[root]
		if len(members) < minSize {
			continue
		}
		id := "c" + strconv.Itoa(clusterCounter)
		clusterCounter++

		memberIDs := make([]string, len(members))
		for i, idx := range members {
			memberIDs[i] = points[order[idx]].ForkID
			pointCluster[idx] = id
		}
		// members were appended in lex order of ForkID by construction.
		// Make the sort explicit so the contract holds regardless of any
		// future change to traversal order.
		sort.Strings(memberIDs)

		centroid := computeCentroid(points, order, members)
		clusterCentroid[id] = centroid

		clusters = append(clusters, Cluster{
			ID:       id,
			Members:  memberIDs,
			Centroid: centroid,
		})
	}

	// Second pass: anything not assigned is noise.
	for i := 0; i < n; i++ {
		if pointCluster[i] == "" {
			pointCluster[i] = noiseID
			noiseMembers = append(noiseMembers, points[order[i]].ForkID)
		}
	}

	if len(noiseMembers) > 0 {
		sort.Strings(noiseMembers)
		clusters = append(clusters, Cluster{
			ID:       noiseID,
			Members:  noiseMembers,
			Centroid: nil,
		})
	}

	// Build assignments in lex order of ForkID for determinism.
	for i := 0; i < n; i++ {
		idx := order[i]
		cid := pointCluster[i]
		var novelty float64
		if cid == noiseID {
			novelty = 1.0
		} else {
			d := cosineDistance(points[idx].Vec, clusterCentroid[cid])
			novelty = d / eps
			if novelty < 0 {
				novelty = 0
			}
			if novelty > 1 {
				novelty = 1
			}
		}
		assignments[i] = Assignment{
			ForkID:  points[idx].ForkID,
			Cluster: cid,
			Novelty: novelty,
		}
	}

	return clusters, assignments
}

// cosineDistance returns 1 - dot(a, b). For L2-normalized vectors this is in
// [0, 2]. Vectors of different length or zero length return +Inf so they form
// no edges in the epsilon graph.
func cosineDistance(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return math.Inf(1)
	}
	var dot float64
	for i, x := range a {
		dot += float64(x) * float64(b[i])
	}
	return 1 - dot
}

// computeCentroid averages member vectors then L2-normalizes. Assumes all
// members in a single component share a vector length (they must, otherwise
// no edges would have connected them).
func computeCentroid(points []Point, order, members []int) []float32 {
	if len(members) == 0 {
		return nil
	}
	dim := len(points[order[members[0]]].Vec)
	if dim == 0 {
		return nil
	}
	sum := make([]float64, dim)
	count := 0
	for _, m := range members {
		v := points[order[m]].Vec
		if len(v) != dim {
			// Defensive: shouldn't happen since differing-length vectors
			// can't share a component, but skip rather than panic.
			continue
		}
		for i, x := range v {
			sum[i] += float64(x)
		}
		count++
	}
	if count == 0 {
		return nil
	}
	out := make([]float32, dim)
	for i := range sum {
		out[i] = float32(sum[i] / float64(count))
	}
	// L2-normalize
	var norm float64
	for _, x := range out {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		inv := float32(1.0 / norm)
		for i := range out {
			out[i] *= inv
		}
	}
	return out
}

// unionFind is a small union-find with union-by-rank and path compression.
type unionFind struct {
	parent []int
	rank   []int
}

func newUnionFind(n int) *unionFind {
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	return &unionFind{parent: parent, rank: make([]int, n)}
}

func (u *unionFind) find(x int) int {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if u.rank[ra] < u.rank[rb] {
		ra, rb = rb, ra
	}
	u.parent[rb] = ra
	if u.rank[ra] == u.rank[rb] {
		u.rank[ra]++
	}
}
