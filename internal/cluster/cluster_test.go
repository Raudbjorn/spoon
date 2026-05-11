package cluster

import (
	"math"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// normalize returns an L2-normalized copy of v.
func normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	n = math.Sqrt(n)
	out := make([]float32, len(v))
	if n == 0 {
		return out
	}
	inv := float32(1 / n)
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// jitter returns a normalized vector close to base, perturbed by amount.
func jitter(base []float32, amount float64, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float32, len(base))
	for i, x := range base {
		out[i] = x + float32(r.NormFloat64()*amount)
	}
	return normalize(out)
}

func defaultOpts() Options {
	return Options{Epsilon: 0.35, MinClusterSize: 3}
}

func TestCluster_Empty(t *testing.T) {
	clusters, assignments := Run(nil, defaultOpts())
	if clusters != nil {
		t.Errorf("expected nil clusters, got %v", clusters)
	}
	if assignments != nil {
		t.Errorf("expected nil assignments, got %v", assignments)
	}
}

func TestCluster_Singleton(t *testing.T) {
	pts := []Point{
		{ForkID: "a/r", Vec: normalize([]float32{1, 0, 0})},
	}
	clusters, assignments := Run(pts, defaultOpts())
	if len(assignments) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(assignments))
	}
	if assignments[0].Cluster != "noise" {
		t.Errorf("expected noise cluster, got %q", assignments[0].Cluster)
	}
	if assignments[0].Novelty != 1.0 {
		t.Errorf("expected novelty 1.0, got %v", assignments[0].Novelty)
	}
	if len(clusters) != 1 || clusters[0].ID != "noise" {
		t.Errorf("expected single noise cluster, got %+v", clusters)
	}
}

func TestCluster_BelowMinSize(t *testing.T) {
	base := []float32{1, 0, 0}
	pts := []Point{
		{ForkID: "a/r1", Vec: jitter(base, 0.001, 1)},
		{ForkID: "a/r2", Vec: jitter(base, 0.001, 2)},
	}
	clusters, assignments := Run(pts, defaultOpts())
	for _, a := range assignments {
		if a.Cluster != "noise" {
			t.Errorf("%s should be noise, got %q", a.ForkID, a.Cluster)
		}
		if a.Novelty != 1.0 {
			t.Errorf("%s novelty should be 1.0, got %v", a.ForkID, a.Novelty)
		}
	}
	if len(clusters) != 1 || clusters[0].ID != "noise" || len(clusters[0].Members) != 2 {
		t.Errorf("expected single noise cluster with 2 members, got %+v", clusters)
	}
}

func TestCluster_OneClusterAllIdentical(t *testing.T) {
	v := normalize([]float32{1, 1, 0})
	pts := []Point{
		{ForkID: "a/a", Vec: v},
		{ForkID: "a/b", Vec: v},
		{ForkID: "a/c", Vec: v},
		{ForkID: "a/d", Vec: v},
		{ForkID: "a/e", Vec: v},
	}
	clusters, assignments := Run(pts, defaultOpts())
	if len(clusters) != 1 || clusters[0].ID != "c0" {
		t.Fatalf("expected single cluster c0, got %+v", clusters)
	}
	if len(clusters[0].Members) != 5 {
		t.Errorf("expected 5 members, got %d", len(clusters[0].Members))
	}
	for _, a := range assignments {
		if a.Cluster != "c0" {
			t.Errorf("%s should be c0, got %q", a.ForkID, a.Cluster)
		}
		if a.Novelty > 1e-5 {
			t.Errorf("%s novelty should be ~0, got %v", a.ForkID, a.Novelty)
		}
	}
}

func TestCluster_TwoSeparateClusters(t *testing.T) {
	base1 := []float32{1, 0, 0}
	base2 := []float32{0, 1, 0}
	pts := []Point{
		{ForkID: "x/three", Vec: jitter(base2, 0.01, 30)},
		{ForkID: "a/one", Vec: jitter(base1, 0.01, 10)},
		{ForkID: "b/two", Vec: jitter(base1, 0.01, 11)},
		{ForkID: "y/four", Vec: jitter(base2, 0.01, 31)},
		{ForkID: "c/three", Vec: jitter(base1, 0.01, 12)},
		{ForkID: "z/five", Vec: jitter(base2, 0.01, 32)},
	}
	clusters, assignments := Run(pts, defaultOpts())
	// Two real clusters expected (no noise).
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d: %+v", len(clusters), clusters)
	}
	if clusters[0].ID != "c0" || clusters[1].ID != "c1" {
		t.Errorf("expected IDs c0,c1, got %q,%q", clusters[0].ID, clusters[1].ID)
	}
	// c0 should be the cluster whose lex-smallest member ID is smallest overall.
	// "a/one" < "x/three", so c0 = the blob-1 cluster.
	wantC0 := []string{"a/one", "b/two", "c/three"}
	wantC1 := []string{"x/three", "y/four", "z/five"}
	if !reflect.DeepEqual(clusters[0].Members, wantC0) {
		t.Errorf("c0 members = %v, want %v", clusters[0].Members, wantC0)
	}
	if !reflect.DeepEqual(clusters[1].Members, wantC1) {
		t.Errorf("c1 members = %v, want %v", clusters[1].Members, wantC1)
	}
	// Per-assignment check.
	want := map[string]string{
		"a/one":   "c0",
		"b/two":   "c0",
		"c/three": "c0",
		"x/three": "c1",
		"y/four":  "c1",
		"z/five":  "c1",
	}
	for _, a := range assignments {
		if a.Cluster != want[a.ForkID] {
			t.Errorf("%s assigned to %q, want %q", a.ForkID, a.Cluster, want[a.ForkID])
		}
	}
}

func TestCluster_NoisePointAmongCluster(t *testing.T) {
	base := []float32{1, 0, 0}
	outlier := []float32{0, 0, 1}
	pts := []Point{
		{ForkID: "a/1", Vec: jitter(base, 0.005, 1)},
		{ForkID: "a/2", Vec: jitter(base, 0.005, 2)},
		{ForkID: "a/3", Vec: jitter(base, 0.005, 3)},
		{ForkID: "a/4", Vec: jitter(base, 0.005, 4)},
		{ForkID: "z/lone", Vec: jitter(outlier, 0.005, 99)},
	}
	clusters, assignments := Run(pts, defaultOpts())
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters (c0 + noise), got %d", len(clusters))
	}
	if clusters[0].ID != "c0" || len(clusters[0].Members) != 4 {
		t.Errorf("c0 should have 4 members, got %+v", clusters[0])
	}
	if clusters[1].ID != "noise" || len(clusters[1].Members) != 1 || clusters[1].Members[0] != "z/lone" {
		t.Errorf("noise should have [z/lone], got %+v", clusters[1])
	}
	var lone Assignment
	for _, a := range assignments {
		if a.ForkID == "z/lone" {
			lone = a
		}
	}
	if lone.Cluster != "noise" || lone.Novelty != 1.0 {
		t.Errorf("z/lone assignment = %+v", lone)
	}
}

func TestCluster_NoveltyEdge(t *testing.T) {
	// Cluster of identical points plus one point at distance ~epsilon/2 from
	// the centroid. We use single-link, so as long as the far point is within
	// epsilon of at least one cluster member it joins the cluster.
	//
	// Use 2D: identical members at [1,0], far point chosen so cosine
	// distance to centroid is epsilon/2 = 0.175. Want dot = 1 - 0.175 =
	// 0.825 ⇒ angle = arccos(0.825). Construct accordingly.
	eps := 0.35
	want := 1.0 - eps/2.0 // 0.825
	angle := math.Acos(want)
	far := []float32{float32(math.Cos(angle)), float32(math.Sin(angle))}

	near := []float32{1, 0}
	pts := []Point{
		{ForkID: "a/1", Vec: append([]float32(nil), near...)},
		{ForkID: "a/2", Vec: append([]float32(nil), near...)},
		{ForkID: "a/3", Vec: append([]float32(nil), near...)},
		{ForkID: "a/4", Vec: append([]float32(nil), near...)},
		{ForkID: "z/far", Vec: far},
	}
	clusters, assignments := Run(pts, Options{Epsilon: eps, MinClusterSize: 3})
	if len(clusters) != 1 || clusters[0].ID != "c0" {
		t.Fatalf("expected single cluster c0, got %+v", clusters)
	}
	if len(clusters[0].Members) != 5 {
		t.Fatalf("expected 5 members, got %d", len(clusters[0].Members))
	}

	novelty := map[string]float64{}
	for _, a := range assignments {
		novelty[a.ForkID] = a.Novelty
	}
	for _, id := range []string{"a/1", "a/2", "a/3", "a/4"} {
		if novelty[id] > 0.2 {
			t.Errorf("%s novelty = %v, want ~small", id, novelty[id])
		}
	}
	// The far point sits ~epsilon/2 from the *original* base, but the
	// centroid is pulled toward it by averaging. So novelty is somewhere
	// in the 0.3..0.5 band — assert "clearly distinct" rather than "exactly 0.5".
	if novelty["z/far"] < 0.25 || novelty["z/far"] > 0.6 {
		t.Errorf("z/far novelty = %v, want roughly 0.3..0.5", novelty["z/far"])
	}
}

func TestCluster_Determinism(t *testing.T) {
	base1 := []float32{1, 0, 0, 0}
	base2 := []float32{0, 1, 0, 0}
	base3 := []float32{0, 0, 1, 0}
	mkPts := func() []Point {
		return []Point{
			{ForkID: "a/1", Vec: jitter(base1, 0.01, 100)},
			{ForkID: "a/2", Vec: jitter(base1, 0.01, 101)},
			{ForkID: "a/3", Vec: jitter(base1, 0.01, 102)},
			{ForkID: "b/1", Vec: jitter(base2, 0.01, 200)},
			{ForkID: "b/2", Vec: jitter(base2, 0.01, 201)},
			{ForkID: "b/3", Vec: jitter(base2, 0.01, 202)},
			{ForkID: "c/lone", Vec: jitter(base3, 0.01, 300)},
		}
	}
	pts1 := mkPts()
	pts2 := mkPts()
	// Shuffle pts2 with a fixed seed.
	r := rand.New(rand.NewSource(42))
	r.Shuffle(len(pts2), func(i, j int) { pts2[i], pts2[j] = pts2[j], pts2[i] })

	c1, a1 := Run(pts1, defaultOpts())
	c2, a2 := Run(pts2, defaultOpts())

	if !reflect.DeepEqual(c1, c2) {
		t.Errorf("clusters differ between runs:\n%+v\nvs\n%+v", c1, c2)
	}
	if !reflect.DeepEqual(a1, a2) {
		t.Errorf("assignments differ between runs:\n%+v\nvs\n%+v", a1, a2)
	}
}

func TestCluster_DifferentLengthVectors(t *testing.T) {
	base4 := []float32{1, 0, 0, 0}
	base8 := make([]float32, 8)
	base8[0] = 1
	pts := []Point{
		{ForkID: "a/1", Vec: jitter(base4, 0.005, 1)},
		{ForkID: "a/2", Vec: jitter(base4, 0.005, 2)},
		{ForkID: "a/3", Vec: jitter(base4, 0.005, 3)},
		{ForkID: "z/1", Vec: jitter(base8, 0.005, 11)},
		{ForkID: "z/2", Vec: jitter(base8, 0.005, 12)},
	}
	clusters, assignments := Run(pts, defaultOpts())
	// Expect c0 = {a/1, a/2, a/3} and noise = {z/1, z/2} — the 8-dim points
	// have no edges to the 4-dim points and there are only 2 of them so they
	// fail MinClusterSize on their own.
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d: %+v", len(clusters), clusters)
	}
	if clusters[0].ID != "c0" || !reflect.DeepEqual(clusters[0].Members, []string{"a/1", "a/2", "a/3"}) {
		t.Errorf("c0 = %+v", clusters[0])
	}
	if clusters[1].ID != "noise" || !reflect.DeepEqual(clusters[1].Members, []string{"z/1", "z/2"}) {
		t.Errorf("noise = %+v", clusters[1])
	}
	for _, a := range assignments {
		if a.ForkID[:1] == "z" && a.Cluster != "noise" {
			t.Errorf("%s should be noise, got %q", a.ForkID, a.Cluster)
		}
	}
}

func TestCluster_EpsilonDefault(t *testing.T) {
	// With epsilon=0.35 by default, two points at cosine distance 0.3 should
	// be joined; with min size 3, two points alone are noise. Three closely
	// spaced points should form a cluster.
	base := []float32{1, 0, 0}
	pts := []Point{
		{ForkID: "a/1", Vec: jitter(base, 0.01, 1)},
		{ForkID: "a/2", Vec: jitter(base, 0.01, 2)},
		{ForkID: "a/3", Vec: jitter(base, 0.01, 3)},
	}
	clusters, _ := Run(pts, Options{})
	if len(clusters) != 1 || clusters[0].ID != "c0" || len(clusters[0].Members) != 3 {
		t.Fatalf("expected single c0 of 3 with default opts, got %+v", clusters)
	}
}

func TestCluster_NonMutation(t *testing.T) {
	base := []float32{1, 0, 0}
	pts := []Point{
		{ForkID: "b/2", Vec: jitter(base, 0.01, 2)},
		{ForkID: "a/1", Vec: jitter(base, 0.01, 1)},
		{ForkID: "c/3", Vec: jitter(base, 0.01, 3)},
	}
	// Deep copy for comparison.
	orig := make([]Point, len(pts))
	for i, p := range pts {
		orig[i] = Point{ForkID: p.ForkID, Vec: append([]float32(nil), p.Vec...)}
	}
	_, _ = Run(pts, defaultOpts())
	if !reflect.DeepEqual(pts, orig) {
		t.Errorf("Cluster mutated its inputs:\nwant %+v\ngot  %+v", orig, pts)
	}
}

// Sanity guard: assignments come back sorted by ForkID (this is implicit in
// the "deterministic given identical inputs" contract but worth a check).
func TestCluster_AssignmentsSortedByForkID(t *testing.T) {
	base := []float32{1, 0, 0}
	pts := []Point{
		{ForkID: "c/3", Vec: jitter(base, 0.01, 3)},
		{ForkID: "a/1", Vec: jitter(base, 0.01, 1)},
		{ForkID: "b/2", Vec: jitter(base, 0.01, 2)},
	}
	_, assignments := Run(pts, defaultOpts())
	ids := make([]string, len(assignments))
	for i, a := range assignments {
		ids[i] = a.ForkID
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("assignments not sorted by ForkID: %v", ids)
	}
}
