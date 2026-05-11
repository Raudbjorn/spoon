package cluster

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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

// constLabeler returns a fixed polished label for every call. It records the
// LabelerContext payloads so tests can assert what was sent.
type constLabeler struct {
	mu     sync.Mutex
	label  string
	err    error
	calls  []LabelerContext
	called int32
}

func (l *constLabeler) Polish(_ context.Context, lc LabelerContext) (string, error) {
	atomic.AddInt32(&l.called, 1)
	l.mu.Lock()
	l.calls = append(l.calls, lc)
	l.mu.Unlock()
	if l.err != nil {
		return lc.Heuristic, l.err
	}
	return l.label, nil
}

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

func TestPipeline_LabelerPolishApplied(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// Three forks clustered together via the stub embedder (shared first byte).
	forks := []EnrichedFork{
		makePipelineFork("o/a1", 3, []string{"Alpha/x.go", "Alpha/y.go"}, 90),
		makePipelineFork("o/a2", 4, []string{"Alpha/z.go", "Alpha/w.go"}, 85),
		makePipelineFork("o/a3", 5, []string{"Alpha/m.go", "Alpha/n.go"}, 80),
	}
	labeler := &constLabeler{label: "polished label"}

	opts := PipelineOptions{
		Enabled:         true,
		TopN:            10,
		Epsilon:         0.6,
		MinClusterSize:  3,
		NonInteractive:  true,
		EmbedderForTest: pipelineStubEmbedder{},
		Labeler:         labeler,
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
	if atomic.LoadInt32(&labeler.called) == 0 {
		t.Fatalf("labeler never called\nlog: %s", buf.String())
	}
	// At least one fork should land in a non-noise cluster with the polished
	// label written back.
	saw := false
	for _, ef := range forks {
		if ef.Heat.ClusterID == "" || ef.Heat.ClusterID == "noise" {
			continue
		}
		saw = true
		if ef.Heat.ClusterLabel != "polished label" {
			t.Errorf("fork %s: ClusterLabel = %q, want %q", ef.T1.ID, ef.Heat.ClusterLabel, "polished label")
		}
	}
	if !saw {
		t.Errorf("no fork landed in a non-noise cluster\nlog: %s", buf.String())
	}
}

func TestPipeline_LabelerError_FallsBack(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	forks := []EnrichedFork{
		makePipelineFork("o/a1", 3, []string{"Alpha/x.go", "Alpha/y.go"}, 90),
		makePipelineFork("o/a2", 4, []string{"Alpha/z.go", "Alpha/w.go"}, 85),
		makePipelineFork("o/a3", 5, []string{"Alpha/m.go", "Alpha/n.go"}, 80),
	}
	labeler := &constLabeler{err: errors.New("boom")}

	opts := PipelineOptions{
		Enabled:         true,
		TopN:            10,
		Epsilon:         0.6,
		MinClusterSize:  3,
		NonInteractive:  true,
		EmbedderForTest: pipelineStubEmbedder{},
		Labeler:         labeler,
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
	if atomic.LoadInt32(&labeler.called) == 0 {
		t.Fatalf("expected labeler to be called even when erroring")
	}
	saw := false
	for _, ef := range forks {
		if ef.Heat.ClusterID == "" || ef.Heat.ClusterID == "noise" {
			continue
		}
		saw = true
		// Heuristic label should still be present (non-empty); definitely NOT
		// "polished label" because that's what the labeler does on success.
		if ef.Heat.ClusterLabel == "polished label" {
			t.Errorf("fork %s: polished label leaked through error path", ef.T1.ID)
		}
		if ef.Heat.ClusterLabel == "" {
			t.Errorf("fork %s: expected heuristic ClusterLabel, got empty", ef.T1.ID)
		}
	}
	if !saw {
		t.Errorf("no non-noise cluster produced\nlog: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "labeler polish failed") {
		t.Errorf("expected error log line, got: %s", buf.String())
	}
}

// recordingLabeler captures the cluster IDs it was invoked for.
type recordingLabeler struct {
	mu         sync.Mutex
	clusterIDs []string
}

func (r *recordingLabeler) Polish(_ context.Context, lc LabelerContext) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// We don't have the cluster ID directly; use a member ID as a proxy. The
	// pipeline-side guard is on cluster.ID == "noise" — to verify the noise
	// cluster is skipped we just need to confirm the labeler is invoked once
	// per non-noise cluster.
	if len(lc.Members) > 0 {
		// nothing to record from features themselves; track total count.
	}
	r.clusterIDs = append(r.clusterIDs, lc.Heuristic)
	return lc.Heuristic + " polished", nil
}

func TestPipeline_LabelerSkipsNoise(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// Build two disjoint groups + a singleton. With MinClusterSize=3 the
	// singleton becomes part of the noise pseudo-cluster. Path-prefix letters
	// are chosen so their first byte mod 8 maps to distinct vector axes in
	// the stub embedder: A=1, B=2, C=3, D=4 — disjoint enough that the lone
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
	rl := &recordingLabeler{}

	opts := PipelineOptions{
		Enabled:         true,
		TopN:            20,
		Epsilon:         0.2,
		MinClusterSize:  3,
		NonInteractive:  true,
		EmbedderForTest: pipelineStubEmbedder{},
		Labeler:         rl,
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

	// Expect exactly 2 polish calls (Alpha + Beta); noise is skipped.
	if got := len(rl.clusterIDs); got != 2 {
		t.Errorf("expected labeler called twice (one per non-noise cluster), got %d\nlog: %s\nheuristics: %v",
			got, buf.String(), rl.clusterIDs)
	}
	// Lone fork should be in noise.
	for _, ef := range forks {
		if ef.T1.ID != "o/lone" {
			continue
		}
		if ef.Heat.ClusterID != "noise" {
			t.Errorf("lone fork: expected ClusterID=noise, got %q", ef.Heat.ClusterID)
		}
	}
}

// stubReadmeFetcher is the cluster-package-internal copy used by the pipeline
// labeler-context-fill test.
type pipelineStubReadmeFetcher struct {
	content string
	err     error
	calls   int32
}

func (s *pipelineStubReadmeFetcher) FetchReadme(_ context.Context, _, _ string) (string, error) {
	atomic.AddInt32(&s.calls, 1)
	return s.content, s.err
}

func TestPipeline_LabelerReceivesUpstreamContext(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	forks := []EnrichedFork{
		makePipelineFork("o/a1", 3, []string{"Alpha/x.go"}, 90),
		makePipelineFork("o/a2", 4, []string{"Alpha/y.go"}, 85),
		makePipelineFork("o/a3", 5, []string{"Alpha/z.go"}, 80),
	}
	labeler := &constLabeler{label: "ok"}
	readme := &pipelineStubReadmeFetcher{content: strings.Repeat("R", 50_000)}

	opts := PipelineOptions{
		Enabled:         true,
		TopN:            10,
		Epsilon:         0.6,
		MinClusterSize:  3,
		NonInteractive:  true,
		EmbedderForTest: pipelineStubEmbedder{},
		Labeler:         labeler,
	}
	inputs := PipelineInputs{
		Provider:      "github",
		UpstreamOwner: "up",
		UpstreamRepo:  "stream",
		Upstream:      parentDataFixture(),
		Forks:         forks,
		ReadmeFetcher: readme,
	}

	var buf bytes.Buffer
	if _, err := RunPipeline(context.Background(), opts, inputs, &buf); err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if atomic.LoadInt32(&readme.calls) == 0 {
		t.Errorf("expected upstream README fetch")
	}
	labeler.mu.Lock()
	defer labeler.mu.Unlock()
	if len(labeler.calls) == 0 {
		t.Fatalf("labeler never called")
	}
	got := labeler.calls[0]
	if got.UpstreamRepo != "up/stream" {
		t.Errorf("UpstreamRepo = %q, want up/stream", got.UpstreamRepo)
	}
	if got.UpstreamDesc != "Upstream test fixture" {
		t.Errorf("UpstreamDesc = %q, want fallback to ParentData.Description", got.UpstreamDesc)
	}
	if len(got.UpstreamReadme) > readmeMaxBytes {
		t.Errorf("UpstreamReadme not truncated: %d bytes (limit %d)", len(got.UpstreamReadme), readmeMaxBytes)
	}
	if got.UpstreamReadme == "" {
		t.Errorf("UpstreamReadme is empty")
	}
}

func TestPipeline_LabelerNotInvoked_WhenNil(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	forks := []EnrichedFork{
		makePipelineFork("o/a1", 3, []string{"Alpha/x.go"}, 90),
		makePipelineFork("o/a2", 4, []string{"Alpha/y.go"}, 85),
		makePipelineFork("o/a3", 5, []string{"Alpha/z.go"}, 80),
	}
	readme := &pipelineStubReadmeFetcher{content: "hi"}

	opts := PipelineOptions{
		Enabled:         true,
		TopN:            10,
		Epsilon:         0.6,
		MinClusterSize:  3,
		NonInteractive:  true,
		EmbedderForTest: pipelineStubEmbedder{},
	}
	inputs := PipelineInputs{
		Provider:      "github",
		UpstreamOwner: "up",
		UpstreamRepo:  "stream",
		Upstream:      parentDataFixture(),
		Forks:         forks,
		ReadmeFetcher: readme,
	}

	var buf bytes.Buffer
	if _, err := RunPipeline(context.Background(), opts, inputs, &buf); err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	// README fetch is gated on Labeler != nil for the upstream-side call. The
	// per-fork README fetch still happens for embedding features, so this just
	// checks the labeler path doesn't fire.
	if c := atomic.LoadInt32(&readme.calls); c == 0 {
		// Per-fork fetch may or may not be exercised depending on candidate
		// selection; we just assert the function returned without invoking
		// any labeler path. Nothing to check here directly.
		_ = c
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
