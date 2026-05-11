package dump

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// ─── Stubs ────────────────────────────────────────────────────────────────

// stubEmbedder returns a deterministic vector per input text. The first byte
// of the text steers the vector so clustering can group similar inputs.
type stubEmbedder struct {
	dim int
}

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	dim := s.dim
	if dim == 0 {
		dim = 8
	}
	out := make([]embed.Vector, len(texts))
	for i, t := range texts {
		v := make(embed.Vector, dim)
		if t == "" {
			out[i] = v
			continue
		}
		// First byte selects a "cluster axis", remaining bytes add noise.
		seed := byte(0)
		if len(t) > 0 {
			seed = t[0]
		}
		axis := int(seed) % dim
		v[axis] = 1.0
		out[i] = v
	}
	return out, nil
}

func (s *stubEmbedder) Dim() int {
	if s.dim == 0 {
		return 8
	}
	return s.dim
}

// erroringEmbedder always returns an error.
type erroringEmbedder struct{}

func (erroringEmbedder) Embed(_ context.Context, _ []string) ([]embed.Vector, error) {
	return nil, errors.New("embed boom")
}

func (erroringEmbedder) Dim() int { return 0 }

// stubTreeSource returns a canned path list.
type stubTreeSource struct {
	paths []string
	err   error
}

func (s *stubTreeSource) Tree(_ context.Context, _, _ string) ([]string, error) {
	return s.paths, s.err
}

// stubCommitSource returns canned commit messages.
type stubCommitSource struct {
	msgs []string
	err  error
}

func (s *stubCommitSource) CommitMessages(_ context.Context, _, _ string, _ int) ([]string, error) {
	return s.msgs, s.err
}

// stubReadmeFetcher returns canned README content.
type stubReadmeFetcher struct {
	content string
}

func (s *stubReadmeFetcher) FetchReadme(_ context.Context, _, _ string) (string, error) {
	return s.content, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────

// makeFork builds an EnrichedFork with the given ID, ahead count, and per-path
// payload. The first byte of the first path drives the stub embedder's cluster
// axis, so callers can craft similar/different forks deterministically.
func makeFork(id string, ahead int, paths []string, heatScore float64) EnrichedFork {
	diffs := make([]forge.FileDiff, len(paths))
	for i, p := range paths {
		diffs[i] = forge.FileDiff{Path: p, Additions: 5, Deletions: 1}
	}
	hr := &heat.HeatResult{Score: heatScore, Tier: 2}
	return EnrichedFork{
		T1: forge.T1Data{
			ID:            id,
			Owner:         strings.SplitN(id, "/", 2)[0],
			Name:          strings.SplitN(id, "/", 2)[1],
			Stars:         10,
			PushedAt:      time.Now().Add(-24 * time.Hour),
			DefaultBranch: "main",
			Language:      "Go",
		},
		T2: &forge.T2Data{
			AheadCount: ahead,
			Diffs:      diffs,
		},
		Heat: hr,
	}
}

// uniqueParentDataFixture returns a stable ParentData used by the tests.
func parentFixture() forge.ParentData {
	return forge.ParentData{
		FullName:      "up/stream",
		DefaultBranch: "main",
		PushedAt:      time.Now().Add(-2 * 24 * time.Hour),
	}
}

// ─── Tests ────────────────────────────────────────────────────────────────

func TestRunClusterPipeline_Disabled(t *testing.T) {
	ef := makeFork("a/a", 1, []string{"cmd/a.go", "cmd/b.go"}, 50)
	inputs := ClusterInputs{
		Provider:      "github",
		UpstreamOwner: "up",
		UpstreamRepo:  "stream",
		Upstream:      parentFixture(),
		Forks:         []EnrichedFork{ef},
	}
	var buf bytes.Buffer
	reason, err := runClusterPipeline(context.Background(), ClusterOptions{Enabled: false}, inputs, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason == "" {
		t.Errorf("expected non-empty skip reason for disabled pipeline")
	}
	if ef.Heat.ClusterID != "" {
		t.Errorf("expected ClusterID empty when disabled, got %q", ef.Heat.ClusterID)
	}
}

func TestRunClusterPipeline_HappyPath(t *testing.T) {
	// Build 6 forks: 3 with paths starting "A...", 3 starting "Z...". The
	// stub embedder will place them on different axes, producing two
	// clusters (or one + noise depending on epsilon).
	forks := []EnrichedFork{
		makeFork("o/a1", 3, []string{"Alpha/x.go", "Alpha/y.go"}, 90),
		makeFork("o/a2", 4, []string{"Alpha/z.go", "Alpha/w.go"}, 85),
		makeFork("o/a3", 5, []string{"Alpha/m.go", "Alpha/n.go"}, 80),
		makeFork("o/z1", 2, []string{"Zeta/q.go", "Zeta/r.go"}, 70),
		makeFork("o/z2", 3, []string{"Zeta/s.go", "Zeta/t.go"}, 65),
		makeFork("o/z3", 4, []string{"Zeta/u.go", "Zeta/v.go"}, 60),
	}
	inputs := ClusterInputs{
		Provider:      "github",
		UpstreamOwner: "up",
		UpstreamRepo:  "stream",
		Upstream:      parentFixture(),
		Forks:         forks,
		TreeSource: &stubTreeSource{paths: []string{
			"Alpha/x.go", "Alpha/y.go", "Zeta/q.go", "Zeta/r.go",
		}},
		CommitSource:  &stubCommitSource{msgs: []string{"add alpha", "fix zeta"}},
		ReadmeFetcher: &stubReadmeFetcher{content: "Hello"},
	}
	opts := ClusterOptions{
		Enabled:        true,
		TopN:           10,
		Epsilon:        0.6, // generous so the stub vectors cluster
		MinClusterSize: 3,
		NonInteractive: true,
		// Use t.TempDir-equivalent indirection: tests assume isolation from
		// the user's real ~/.cache. We don't write the cache in the test
		// because TreeSource is stubbed and SaveCache is called — that's OK
		// because it writes to XDG_CACHE_HOME which we control below.
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	opts.embedderForTest = &stubEmbedder{dim: 8}

	var buf bytes.Buffer
	reason, err := runClusterPipeline(context.Background(), opts, inputs, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != "" {
		t.Errorf("expected empty skip reason on happy path, got %q (log: %s)", reason, buf.String())
	}

	// At least one fork should be in a non-noise cluster.
	gotCluster := false
	for _, ef := range forks {
		if ef.Heat.ClusterID != "" && ef.Heat.ClusterID != "noise" {
			gotCluster = true
			if ef.Heat.ClusterMemberCount < 1 {
				t.Errorf("fork %s: ClusterMemberCount = 0, want > 0", ef.T1.ID)
			}
		}
		// NoveltyScore must be in [0,1].
		if ef.Heat.NoveltyScore < 0 || ef.Heat.NoveltyScore > 1 {
			t.Errorf("fork %s: NoveltyScore %v out of [0,1]", ef.T1.ID, ef.Heat.NoveltyScore)
		}
	}
	if !gotCluster {
		t.Errorf("no fork landed in a non-noise cluster (epsilon may be off)\nlog:\n%s", buf.String())
	}
}

func TestRunClusterPipeline_EmbedderUnreachable(t *testing.T) {
	// Point at a definitely-dead host so Detect fails the 500ms probe.
	ef := makeFork("o/x", 1, []string{"a.go"}, 50)
	inputs := ClusterInputs{
		Provider:      "github",
		UpstreamOwner: "up",
		UpstreamRepo:  "stream",
		Upstream:      parentFixture(),
		Forks:         []EnrichedFork{ef},
	}
	opts := ClusterOptions{
		Enabled:        true,
		TopN:           10,
		NonInteractive: true,
		Endpoint:       "http://127.0.0.1:1", // unreachable
	}

	var buf bytes.Buffer
	reason, err := runClusterPipeline(context.Background(), opts, inputs, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason == "" {
		t.Errorf("expected non-empty skip reason when embedder unreachable")
	}
	if ef.Heat.ClusterID != "" {
		t.Errorf("expected no ClusterID when embedder unreachable, got %q", ef.Heat.ClusterID)
	}
	if !strings.Contains(buf.String(), "skipping") {
		t.Errorf("expected skip log line, got: %s", buf.String())
	}
}

func TestRunClusterPipeline_CentralityFailsButStillClusters(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	forks := []EnrichedFork{
		makeFork("o/a", 1, []string{"cmd/a.go"}, 90),
		makeFork("o/b", 1, []string{"cmd/b.go"}, 80),
		makeFork("o/c", 1, []string{"cmd/c.go"}, 70),
	}
	inputs := ClusterInputs{
		Provider:      "github",
		UpstreamOwner: "up",
		UpstreamRepo:  "stream",
		Upstream:      parentFixture(),
		Forks:         forks,
		TreeSource:    &stubTreeSource{err: errors.New("rate limited")},
		CommitSource:  &stubCommitSource{},
		ReadmeFetcher: &stubReadmeFetcher{},
	}
	opts := ClusterOptions{
		Enabled:         true,
		TopN:            10,
		Epsilon:         0.6,
		MinClusterSize:  3,
		NonInteractive:  true,
		embedderForTest: &stubEmbedder{dim: 8},
	}

	var buf bytes.Buffer
	reason, err := runClusterPipeline(context.Background(), opts, inputs, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != "" {
		t.Errorf("expected empty skip reason; got %q\nlog: %s", reason, buf.String())
	}
	if !strings.Contains(buf.String(), "centrality unavailable") {
		t.Errorf("expected centrality warning in log; got %s", buf.String())
	}
}

func TestRunClusterPipeline_AllForksNoAhead(t *testing.T) {
	forks := []EnrichedFork{
		makeFork("o/a", 0, []string{"a.go"}, 50),
		makeFork("o/b", 0, []string{"b.go"}, 40),
	}
	inputs := ClusterInputs{
		Provider:      "github",
		UpstreamOwner: "up",
		UpstreamRepo:  "stream",
		Upstream:      parentFixture(),
		Forks:         forks,
	}
	opts := ClusterOptions{
		Enabled:         true,
		TopN:            10,
		Epsilon:         0.6,
		MinClusterSize:  2,
		NonInteractive:  true,
		embedderForTest: &stubEmbedder{dim: 8},
	}

	var buf bytes.Buffer
	reason, err := runClusterPipeline(context.Background(), opts, inputs, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != "no eligible forks" {
		t.Errorf("expected 'no eligible forks' reason, got %q", reason)
	}
	for _, ef := range forks {
		if ef.Heat.ClusterID != "" {
			t.Errorf("fork %s: expected no ClusterID, got %q", ef.T1.ID, ef.Heat.ClusterID)
		}
	}
}

func TestRunClusterPipeline_EmbedderErrorDuringEmbed(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	forks := []EnrichedFork{
		makeFork("o/a", 1, []string{"a.go"}, 90),
		makeFork("o/b", 1, []string{"b.go"}, 80),
		makeFork("o/c", 1, []string{"c.go"}, 70),
	}
	inputs := ClusterInputs{
		Provider:      "github",
		UpstreamOwner: "up",
		UpstreamRepo:  "stream",
		Upstream:      parentFixture(),
		Forks:         forks,
	}
	opts := ClusterOptions{
		Enabled:         true,
		TopN:            10,
		Epsilon:         0.6,
		MinClusterSize:  3,
		NonInteractive:  true,
		embedderForTest: erroringEmbedder{},
	}

	var buf bytes.Buffer
	reason, err := runClusterPipeline(context.Background(), opts, inputs, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(reason, "embedder failed") {
		t.Errorf("expected 'embedder failed' reason, got %q", reason)
	}
	for _, ef := range forks {
		if ef.Heat.ClusterID != "" {
			t.Errorf("fork %s: expected no ClusterID after embed failure", ef.T1.ID)
		}
	}
}

func TestSelectClusterCandidates(t *testing.T) {
	a := makeFork("o/a", 5, []string{"a.go"}, 90)
	b := makeFork("o/b", 0, []string{"b.go"}, 80) // no ahead → drop
	c := makeFork("o/c", 3, []string{"c.go"}, 70)
	c.T1.IsArchived = true // archived → drop
	d := makeFork("o/d", 2, []string{"d.go"}, 60)
	e := makeFork("o/e", 1, []string{"e.go"}, 95) // highest heat

	out := selectClusterCandidates([]EnrichedFork{a, b, c, d, e}, 10)
	if len(out) != 3 {
		t.Fatalf("expected 3 candidates after filtering, got %d", len(out))
	}
	want := []string{"o/e", "o/a", "o/d"}
	for i, ef := range out {
		if ef.T1.ID != want[i] {
			t.Errorf("position %d: got %s, want %s", i, ef.T1.ID, want[i])
		}
	}
}

func TestSelectClusterCandidates_TopN(t *testing.T) {
	forks := []EnrichedFork{
		makeFork("o/a", 1, []string{"a.go"}, 90),
		makeFork("o/b", 1, []string{"b.go"}, 80),
		makeFork("o/c", 1, []string{"c.go"}, 70),
	}
	out := selectClusterCandidates(forks, 2)
	if len(out) != 2 {
		t.Fatalf("expected topN=2 to cap to 2 candidates, got %d", len(out))
	}
}
