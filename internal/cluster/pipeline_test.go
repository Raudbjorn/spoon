package cluster

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/mdg"
)

// ─── Stubs ────────────────────────────────────────────────────────────────

// pipelineStubEmbedder is a minimal embed.Embedder for pipeline tests. Each
// input string is mapped to a deterministic 8-dim vector whose dominant axis
// is selected by the first byte — so callers can manufacture forks that
// cluster together by giving them paths starting with the same letter.
type pipelineStubEmbedder struct{}

func (pipelineStubEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	out := make([]embed.Vector, len(texts))
	for i, t := range texts {
		v := make(embed.Vector, 8)
		if t == "" {
			out[i] = v
			continue
		}
		v[int(t[0])%8] = 1.0
		out[i] = v
	}
	return out, nil
}

func (pipelineStubEmbedder) Dim() int { return 8 }

// makePipelineFork builds a minimal EnrichedFork that the pipeline will accept.
// The first byte of the first path drives the stub embedder's cluster axis.
func makePipelineFork(id string, ahead int, paths []string, heatScore float64) EnrichedFork {
	diffs := make([]forge.FileDiff, len(paths))
	for i, p := range paths {
		diffs[i] = forge.FileDiff{Path: p, Additions: 5, Deletions: 1}
	}
	hr := &heat.HeatResult{Score: heatScore, Tier: 2}
	owner, name := "o", id
	if slash := strings.IndexByte(id, '/'); slash > 0 {
		owner, name = id[:slash], id[slash+1:]
	}
	return EnrichedFork{
		T1: forge.T1Data{
			ID:            id,
			Owner:         owner,
			Name:          name,
			Stars:         10,
			PushedAt:      time.Now().Add(-24 * time.Hour),
			DefaultBranch: "main",
			Language:      "Go",
		},
		T2: &forge.T2Data{
			AheadCount: ahead,
			Diffs:      diffs,
			Commits: []forge.AheadCommit{
				{Message: "implement " + name + " plugin"},
				{Message: "wire " + name + " into the auth flow"},
			},
		},
		Heat: hr,
	}
}

func parentDataFixture() forge.ParentData {
	return forge.ParentData{
		FullName:      "up/stream",
		Description:   "Upstream test fixture",
		DefaultBranch: "main",
		PushedAt:      time.Now().Add(-2 * 24 * time.Hour),
	}
}

// ─── Tests ────────────────────────────────────────────────────────────────

func TestPipeline_ClustersAndHeuristicLabels(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// Three forks clustered together via the stub embedder (shared first byte).
	forks := []EnrichedFork{
		makePipelineFork("o/a1", 3, []string{"Alpha/x.go", "Alpha/y.go"}, 90),
		makePipelineFork("o/a2", 4, []string{"Alpha/z.go", "Alpha/w.go"}, 85),
		makePipelineFork("o/a3", 5, []string{"Alpha/m.go", "Alpha/n.go"}, 80),
	}

	opts := PipelineOptions{
		Enabled:        true,
		TopN:           10,
		Epsilon:        0.6,
		MinClusterSize: 3,
		Embedder:       pipelineStubEmbedder{},
	}
	inputs := PipelineInputs{
		Provider:      "github",
		UpstreamOwner: "up",
		UpstreamRepo:  "stream",
		Upstream:      parentDataFixture(),
		Forks:         forks,
	}

	var buf bytes.Buffer
	skip, err := RunPipeline(context.Background(), opts, inputs, &buf)
	if err != nil {
		t.Fatalf("RunPipeline: %v\nlog: %s", err, buf.String())
	}
	if skip != nil {
		t.Fatalf("expected no skip, got %+v\nlog: %s", skip, buf.String())
	}
	// At least one fork should land in a non-noise cluster with a non-empty
	// heuristic label written back.
	saw := false
	for _, ef := range forks {
		if ef.Heat.ClusterID == "" || ef.Heat.ClusterID == "noise" {
			continue
		}
		saw = true
		if ef.Heat.ClusterLabel == "" {
			t.Errorf("fork %s: expected heuristic ClusterLabel, got empty", ef.T1.ID)
		}
	}
	if !saw {
		t.Errorf("no fork landed in a non-noise cluster\nlog: %s", buf.String())
	}
}

func TestPipeline_BuiltinEmbedderEndToEnd(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// No EmbedderForTest: the pipeline must run on the built-in lexical
	// embedder with zero configuration. Two groups with disjoint paths and
	// commit vocabulary plus one outlier.
	forks := []EnrichedFork{
		makePipelineFork("o/a1", 3, []string{"auth/oauth.go", "auth/token.go"}, 90),
		makePipelineFork("o/a2", 4, []string{"auth/oauth.go", "auth/session.go"}, 85),
		makePipelineFork("o/a3", 5, []string{"auth/token.go", "auth/middleware.go"}, 80),
		makePipelineFork("o/ci", 1, []string{"zci/workflow.yml"}, 50),
	}

	opts := PipelineOptions{
		Enabled:        true,
		TopN:           10,
		Epsilon:        0.55,
		MinClusterSize: 3,
	}
	inputs := PipelineInputs{
		Provider:      "github",
		UpstreamOwner: "up",
		UpstreamRepo:  "stream",
		Upstream:      parentDataFixture(),
		Forks:         forks,
	}

	var buf bytes.Buffer
	skip, err := RunPipeline(context.Background(), opts, inputs, &buf)
	if err != nil {
		t.Fatalf("RunPipeline: %v\nlog: %s", err, buf.String())
	}
	if skip != nil {
		t.Fatalf("builtin embedder must never skip, got %+v\nlog: %s", skip, buf.String())
	}
	// Every candidate fork must receive a cluster assignment (cluster or noise).
	for _, ef := range forks {
		if ef.Heat.ClusterID == "" {
			t.Errorf("fork %s: no cluster assignment\nlog: %s", ef.T1.ID, buf.String())
		}
	}
	// The three auth forks share paths and commit vocabulary; they should
	// cluster together.
	for _, ef := range forks[:3] {
		if ef.Heat.ClusterID == "noise" {
			t.Errorf("fork %s: expected non-noise cluster, got noise\nlog: %s", ef.T1.ID, buf.String())
		}
	}
}

func TestPipeline_NoiseAssignment(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// Build two disjoint groups + a singleton. With MinClusterSize=3 the
	// singleton becomes part of the noise pseudo-cluster. Path-prefix letters
	// are chosen so their first byte mod 8 maps to distinct vector axes in
	// the stub embedder: A=1, B=2, D=4 — disjoint enough that the lone
	// fork on D is far from Alpha (A) and Beta (B).
	forks := []EnrichedFork{
		// Cluster Alpha (3 members, shared first byte 'A')
		makePipelineFork("o/a1", 3, []string{"Alpha/x.go"}, 90),
		makePipelineFork("o/a2", 4, []string{"Alpha/y.go"}, 85),
		makePipelineFork("o/a3", 5, []string{"Alpha/z.go"}, 80),
		// Cluster Beta (3 members, shared first byte 'B')
		makePipelineFork("o/b1", 2, []string{"Beta/q.go"}, 75),
		makePipelineFork("o/b2", 3, []string{"Beta/r.go"}, 70),
		makePipelineFork("o/b3", 4, []string{"Beta/s.go"}, 65),
		// Lone wolf (cluster of 1 → noise). First letter 'D' = 68 mod 8 = 4.
		makePipelineFork("o/lone", 1, []string{"Delta/lone.go"}, 50),
	}

	opts := PipelineOptions{
		Enabled:        true,
		TopN:           20,
		Epsilon:        0.2,
		MinClusterSize: 3,
		Embedder:       pipelineStubEmbedder{},
	}
	inputs := PipelineInputs{
		Provider:      "github",
		UpstreamOwner: "up",
		UpstreamRepo:  "stream",
		Upstream:      parentDataFixture(),
		Forks:         forks,
	}

	var buf bytes.Buffer
	if _, err := RunPipeline(context.Background(), opts, inputs, &buf); err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}

	// Lone fork should be in noise; the two groups should form distinct
	// non-noise clusters.
	clusterIDs := make(map[string]struct{})
	for _, ef := range forks {
		if ef.T1.ID == "o/lone" {
			if ef.Heat.ClusterID != "noise" {
				t.Errorf("lone fork: expected ClusterID=noise, got %q", ef.Heat.ClusterID)
			}
			continue
		}
		if ef.Heat.ClusterID == "noise" || ef.Heat.ClusterID == "" {
			t.Errorf("fork %s: expected non-noise cluster, got %q", ef.T1.ID, ef.Heat.ClusterID)
			continue
		}
		clusterIDs[ef.Heat.ClusterID] = struct{}{}
	}
	if len(clusterIDs) != 2 {
		t.Errorf("expected 2 non-noise clusters, got %d\nlog: %s", len(clusterIDs), buf.String())
	}
}

func TestPipelineOptions_CentralityBackend(t *testing.T) {
	// Smoke test for the field. Real MDG behavior is covered by
	// internal/mdg/centrality_test.go. The dispatcher's silent-fallback path
	// is tested implicitly: any existing pipeline_test that omits the field
	// continues to pass because "" maps to the directory backend.
	opts := PipelineOptions{CentralityBackend: "mdg"}
	if opts.CentralityBackend != "mdg" {
		t.Fatalf("backend should round-trip; got %q", opts.CentralityBackend)
	}
}

// TestPipelineOptions_CentralityHeadSHA pins the contract that the new
// MDG-cache pin field round-trips. The R1 plumbing guarantees that
// forge.ParentData.HeadSHA is forwarded to loadOrComputeMDG; this is the
// structural test that the field exists and is settable. The actual
// cache-hit behavior is covered by TestPipeline_CentralityHeadSHACacheHit.
func TestPipelineOptions_CentralityHeadSHA(t *testing.T) {
	opts := PipelineOptions{CentralityHeadSHA: "deadbeefcafe1234"}
	if opts.CentralityHeadSHA != "deadbeefcafe1234" {
		t.Fatalf("CentralityHeadSHA should round-trip; got %q", opts.CentralityHeadSHA)
	}
	// Empty value must remain a valid opt-out: loadOrComputeMDG skips the
	// cache entirely when the SHA is empty (see cache.go fast-path check).
	empty := PipelineOptions{}
	if empty.CentralityHeadSHA != "" {
		t.Fatalf("zero-value CentralityHeadSHA should be empty, got %q", empty.CentralityHeadSHA)
	}
}

// TestPipeline_CentralityHeadSHACacheHit exercises the full MDG-cache
// fast-path: pre-seed a cache entry with a known SHA, then run the
// pipeline with that same SHA on CentralityHeadSHA. The pipeline must
// surface a non-empty Centrality through ChangeImpact (via the cached
// adapter) without calling the network. With Refresh=true or a different
// SHA, the cache should be bypassed.
func TestPipeline_CentralityHeadSHACacheHit(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	sha := "abc1234def5678"
	owner, repo := "cachetest", "cached"

	// Pre-seed the MDG cache. The adapter serves ScoreFork by exact
	// touched-file match against the score table, so seed with the
	// exact path the T2 diff below will carry.
	if err := mdg.SaveMDGCache(mdg.MDGCache{
		SchemaVersion: mdg.CacheSchemaVersion,
		Provider:      "github",
		Owner:         owner,
		Repo:          repo,
		HeadSHA:       sha,
		ComputedAt:    time.Now(),
		Scores: map[string]float64{
			"cluster/pipeline.go": 0.5,
		},
	}); err != nil {
		t.Fatalf("seed MDG cache: %v", err)
	}

	// Build a single fork whose T2 diffs target the seeded path.
	fork := makePipelineFork(owner+"/"+repo, 3, []string{"cluster/pipeline.go"}, 80)

	inputs := PipelineInputs{
		Provider:      "github",
		UpstreamOwner: owner,
		UpstreamRepo:  repo,
		Upstream:      parentDataFixture(),
		Forks:         []EnrichedFork{fork},
		// No TreeSource → without the cache, the dispatcher would fall
		// back to the directory proxy and produce zero ChangeImpact.
	}
	opts := PipelineOptions{
		Enabled:           true,
		TopN:              1,
		CentralityBackend: "mdg",
		CentralityHeadSHA: sha,
		Embedder:          pipelineStubEmbedder{},
		EmbedderID:        "stub",
	}

	var log bytes.Buffer
	if _, err := RunPipeline(context.Background(), opts, inputs, &log); err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}

	// The cached adapter should have served the seeded score through to
	// ChangeImpact. Without a real file→module bridge we can't pin an
	// exact value, but the field must be > 0 because the cached adapter's
	// ScoreFork returned non-zero for at least one input — and 0 with no
	// inputs would mean the cache was bypassed. We assert > 0 to lock the
	// fast-path wire-up; the exact fidelity is documented degraded at
	// mdgCachedAdapter.
	if fork.Heat.ChangeImpact <= 0 {
		t.Fatalf("expected ChangeImpact > 0 from MDG cache, got %v (log: %s)",
			fork.Heat.ChangeImpact, log.String())
	}
}

func TestMDGCachedAdapter_ScoreFork(t *testing.T) {
	a := &mdgCachedAdapter{cache: mdg.MDGCache{
		Scores: map[string]float64{
			"example.com/m/internal/auth": 0.4,
			"example.com/m/internal/util": 0.2,
		},
	}}
	// Exact path match against the score table.
	got := a.ScoreFork([]string{"example.com/m/internal/auth"})
	if got <= 0 || got > 1 {
		t.Fatalf("ScoreFork(auth) out of (0,1]: %v", got)
	}
	// Unknown path → 0.
	if got := a.ScoreFork([]string{"unknown/path"}); got != 0 {
		t.Fatalf("ScoreFork(unknown) = %v, want 0", got)
	}
	// Empty input → 0.
	if got := a.ScoreFork(nil); got != 0 {
		t.Fatalf("ScoreFork(nil) = %v, want 0", got)
	}
}
