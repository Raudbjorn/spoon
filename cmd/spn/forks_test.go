// cmd/spn/forks_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
	"github.com/svnbjrn/spoon/internal/heat"
)

type fakeForge struct {
	parent forge.ParentData
	forks  []forge.T1Data
	t2     map[string]forge.T2Data
}

func (f *fakeForge) Auth(_ context.Context) (forge.AuthInfo, error) {
	return forge.AuthInfo{Tier: forge.AuthCLI, Concurrency: 2}, nil
}
func (f *fakeForge) Parent(_ context.Context, _, _ string) (forge.ParentData, error) {
	return f.parent, nil
}
func (f *fakeForge) ListForks(_ context.Context, _, _ string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg, len(f.forks))
	for _, fk := range f.forks {
		ch <- forge.ForkMsg{Fork: fk}
	}
	close(ch)
	return ch, nil
}
func (f *fakeForge) Branches(_ context.Context, fk forge.T1Data, _ int) ([]forge.BranchRef, error) {
	return []forge.BranchRef{{Name: fk.DefaultBranch}}, nil
}
func (f *fakeForge) Compare(_ context.Context, fk forge.T1Data, _ string) (forge.T2Data, error) {
	if t2, ok := f.t2[fk.ID]; ok {
		return t2, nil
	}
	return forge.T2Data{}, nil
}
func (f *fakeForge) Contributors(_ context.Context, _ forge.T1Data) (forge.T3Data, error) {
	return forge.T3Data{}, nil
}
func (f *fakeForge) Headroom() float64 { return 1.0 }

func TestSpnForksList_emitsNDJSON(t *testing.T) {
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
			forks: []forge.T1Data{
				{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()},
				{ID: "o/b", Owner: "o", Name: "b", PushedAt: time.Now()},
			},
		}, "o/r", nil
	}
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{"list", "o/r", "--tier", "1", "--no-cluster"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NDJSON lines, got %d:\n%s", len(lines), stdout.String())
	}
	for _, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("invalid JSON line %q: %v", line, err)
		}
	}
}

// stubEmbedder mirrors the one in internal/dump/cluster_pipeline_test.go.
// First byte of each input text drives a deterministic axis.
type stubEmbedder struct{ dim int }

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

// makeClusterForks returns a fakeForge with 6 forks split across two axes so
// the stub embedder forms two clusters.
func makeClusterForks(now time.Time, parentPushed time.Time) *fakeForge {
	ids := []string{"o/a1", "o/a2", "o/a3", "o/z1", "o/z2", "o/z3"}
	paths := map[string]string{
		"o/a1": "Alpha/x.go",
		"o/a2": "Alpha/y.go",
		"o/a3": "Alpha/z.go",
		"o/z1": "Zeta/p.go",
		"o/z2": "Zeta/q.go",
		"o/z3": "Zeta/r.go",
	}
	t2 := map[string]forge.T2Data{}
	forks := []forge.T1Data{}
	for _, id := range ids {
		parts := strings.SplitN(id, "/", 2)
		forks = append(forks, forge.T1Data{
			ID:            id,
			Owner:         parts[0],
			Name:          parts[1],
			PushedAt:      now,
			DefaultBranch: "main",
			Language:      "Go",
			Stars:         3,
		})
		t2[id] = forge.T2Data{
			AheadCount: 3,
			Diffs: []forge.FileDiff{
				{Path: paths[id], Additions: 5, Deletions: 1},
			},
		}
	}
	return &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: parentPushed},
		forks:  forks,
		t2:     t2,
	}
}

func TestSpnForksList_clusterFieldsPopulated(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prev := providerFactory
	defer func() { providerFactory = prev }()

	now := time.Now()
	parentPushed := now.Add(-7 * 24 * time.Hour)
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return makeClusterForks(now, parentPushed), "up/stream", nil
	}

	// We must reach into doForksList to install the test embedder before
	// Stream runs. The cleanest path is to override the embedder via a
	// post-parse hook; since we don't have one, drive the pipeline through
	// the public option struct directly by calling forksops.Stream after
	// configuring it. But the test entry point we're asked to extend is
	// runForksWith. Use an env-keyed test hook installed below.
	embedderHookForTest = &stubEmbedder{dim: 8}
	defer func() { embedderHookForTest = nil }()

	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{
		"list", "up/stream",
		"--tier", "2",
		"--cluster-epsilon", "0.6",
		"--cluster-min-size", "3",
	}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("expected 6 NDJSON lines, got %d:\n%s", len(lines), stdout.String())
	}
	gotCluster := 0
	for _, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if cid, ok := obj["clusterId"].(string); ok && cid != "" && cid != "noise" {
			gotCluster++
			if _, hasCount := obj["clusterMemberCount"]; !hasCount {
				t.Errorf("expected clusterMemberCount for fork with clusterId=%s", cid)
			}
		}
	}
	if gotCluster == 0 {
		t.Errorf("expected at least one fork in a non-noise cluster\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func TestSpnForksList_noCluster_omitsClusterFields(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prev := providerFactory
	defer func() { providerFactory = prev }()

	now := time.Now()
	parentPushed := now.Add(-7 * 24 * time.Hour)
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return makeClusterForks(now, parentPushed), "up/stream", nil
	}

	// Even though we install a stub embedder, --no-cluster should bypass it.
	embedderHookForTest = &stubEmbedder{dim: 8}
	defer func() { embedderHookForTest = nil }()

	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{
		"list", "up/stream",
		"--tier", "2",
		"--no-cluster",
	}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if _, ok := obj["clusterId"]; ok {
			t.Errorf("--no-cluster: clusterId should be omitted, got: %s", line)
		}
		if _, ok := obj["clusterLabel"]; ok {
			t.Errorf("--no-cluster: clusterLabel should be omitted, got: %s", line)
		}
	}
	// No warning either.
	if strings.Contains(stderr.String(), "embedder_model_missing") {
		t.Errorf("--no-cluster: expected no embedder warning, got: %s", stderr.String())
	}
}

// TestForkToJSON_NoveltyGatedOnClusterID verifies that noveltyScore is
// emitted only when the fork was clustered (ClusterID != ""), and that a
// genuine zero novelty for a clustered fork still serializes as
// "noveltyScore": 0. Omission means "not computed".
func TestForkToJSON_NoveltyGatedOnClusterID(t *testing.T) {
	cases := []struct {
		name           string
		heat           heat.HeatResult
		wantNovelty    bool
		wantNoveltyVal float64
		wantClusterID  bool
	}{
		{
			name:           "clustered with zero novelty emits noveltyScore: 0",
			heat:           heat.HeatResult{ClusterID: "c0", NoveltyScore: 0},
			wantNovelty:    true,
			wantNoveltyVal: 0,
			wantClusterID:  true,
		},
		{
			name:           "clustered with positive novelty emits noveltyScore",
			heat:           heat.HeatResult{ClusterID: "c1", NoveltyScore: 0.42},
			wantNovelty:    true,
			wantNoveltyVal: 0.42,
			wantClusterID:  true,
		},
		{
			name:          "not clustered omits noveltyScore",
			heat:          heat.HeatResult{ClusterID: "", NoveltyScore: 0},
			wantNovelty:   false,
			wantClusterID: false,
		},
		{
			name:          "not clustered ignores stale novelty value",
			heat:          heat.HeatResult{ClusterID: "", NoveltyScore: 0.99},
			wantNovelty:   false,
			wantClusterID: false,
		},
		{
			name:           "noise cluster still emits noveltyScore (ClusterID is non-empty)",
			heat:           heat.HeatResult{ClusterID: "noise", NoveltyScore: 0.1},
			wantNovelty:    true,
			wantNoveltyVal: 0.1,
			wantClusterID:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := forksops.Result{
				Fork: forge.T1Data{ID: "o/r", Owner: "o", Name: "r"},
				Heat: tc.heat,
			}
			out := forkToJSON(r)
			b, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			s := string(b)
			_, hasNovelty := out["noveltyScore"]
			if hasNovelty != tc.wantNovelty {
				t.Errorf("noveltyScore present = %v, want %v (json=%s)", hasNovelty, tc.wantNovelty, s)
			}
			if tc.wantNovelty {
				got, ok := out["noveltyScore"].(float64)
				if !ok {
					t.Fatalf("noveltyScore not float64: %T (json=%s)", out["noveltyScore"], s)
				}
				if got != tc.wantNoveltyVal {
					t.Errorf("noveltyScore = %v, want %v (json=%s)", got, tc.wantNoveltyVal, s)
				}
			}
			_, hasClusterID := out["clusterId"]
			if hasClusterID != tc.wantClusterID {
				t.Errorf("clusterId present = %v, want %v (json=%s)", hasClusterID, tc.wantClusterID, s)
			}
		})
	}
}

// TestForkToJSON_EmitsComponents verifies that the v2 Components slice, when
// populated by the v2 scoring path, is emitted as a "components" array.
func TestForkToJSON_EmitsComponents(t *testing.T) {
	r := forksops.Result{
		Fork: forge.T1Data{ID: "o/r", Owner: "o", Name: "r"},
		Heat: heat.HeatResult{
			Components: []heat.Component{
				{Name: "novelty", Points: 2.1, Max: 5, Raw: 0.42},
				{Name: "recency", Points: 4.0, Max: 10, Raw: 30},
			},
		},
	}
	out := forkToJSON(r)
	comps, ok := out["components"].([]map[string]any)
	if !ok {
		t.Fatalf("components missing or wrong type: %T", out["components"])
	}
	if len(comps) != 2 {
		t.Fatalf("components len = %d, want 2", len(comps))
	}
	if comps[0]["name"] != "novelty" {
		t.Errorf("comps[0].name = %v, want novelty", comps[0]["name"])
	}
	if comps[0]["points"] != 2.1 {
		t.Errorf("comps[0].points = %v, want 2.1", comps[0]["points"])
	}
}

// TestForkToJSON_NoComponents verifies that an empty Components slice is
// omitted from the NDJSON output (v1 scoring path).
func TestForkToJSON_NoComponents(t *testing.T) {
	r := forksops.Result{
		Fork: forge.T1Data{ID: "o/r", Owner: "o", Name: "r"},
		Heat: heat.HeatResult{},
	}
	out := forkToJSON(r)
	if _, ok := out["components"]; ok {
		t.Errorf("components should be omitted when empty, got: %+v", out["components"])
	}
}

func TestSpnForksList_csv_emitsHeaderAndRows(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // don't read the real embedder config
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
			forks: []forge.T1Data{
				{ID: "o/a", Owner: "o", Name: "a", URL: "https://github.com/o/a", Stars: 5, PushedAt: time.Now()},
				{ID: "o/b", Owner: "o", Name: "b", URL: "https://github.com/o/b", Stars: 3, PushedAt: time.Now()},
			},
		}, "o/r", nil
	}
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{"list", "o/r", "--tier", "1", "--no-cluster", "--csv"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 1 header + 2 data rows, got %d:\n%s", len(lines), stdout.String())
	}
	wantHeader := "id,owner,name,url,stars,pushed_at,is_archived,sub_forks,releases,heat,tier,t2_ahead,t2_behind,t2_mna,t3_contributors,t3_commit_span_days,cluster_name,cluster_score"
	if lines[0] != wantHeader {
		t.Errorf("header mismatch:\n got: %s\nwant: %s", lines[0], wantHeader)
	}
}

func TestSpnForksList_csv_noNDJSONLeak(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
			forks:  []forge.T1Data{{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()}},
		}, "o/r", nil
	}
	var stdout, stderr bytes.Buffer
	runForksWith([]string{"list", "o/r", "--tier", "1", "--no-cluster", "--csv"}, &stdout, &stderr)
	// CSV output should not contain JSON-shaped lines.
	if strings.Contains(stdout.String(), `{"id":`) || strings.Contains(stdout.String(), `"id":`) {
		t.Errorf("CSV output contains JSON object: %s", stdout.String())
	}
}

func TestSpnForksList_embedderUnreachable_failsFast(t *testing.T) {
	// Isolate config so the real ~/.config/spoon/config.json can't change the
	// resolved backend; the default (ollama) backend + bad --embedder is the case.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	prev := providerFactory
	defer func() { providerFactory = prev }()
	// providerFactory must NOT be called: preflight should fail before enumeration.
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		t.Fatal("providerFactory should not be reached when the embedder endpoint is unreachable")
		return nil, "", nil
	}

	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{
		"list", "up/stream",
		"--tier", "2",
		"--embedder", "http://127.0.0.1:1",
	}, &stdout, &stderr)

	// New contract: an unreachable embedder endpoint fails fast (exit 2),
	// emitting a structured error and no stdout — not a warning-and-continue.
	if exit != 2 {
		t.Fatalf("exit=%d want 2\nstderr=%s", exit, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no stdout on preflight failure, got: %s", stdout.String())
	}
	var env map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr is not a JSON error envelope: %v\n%s", err, stderr.String())
	}
	e, _ := env["error"].(map[string]any)
	if e == nil || e["code"] != "bad_input" {
		t.Errorf("expected bad_input error, got: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "not reachable") {
		t.Errorf("error should explain the endpoint is unreachable: %s", stderr.String())
	}
}

func TestSpnForksList_noClusterSkipsPreflight(t *testing.T) {
	// --no-cluster bypasses the embedder preflight entirely.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	prev := providerFactory
	defer func() { providerFactory = prev }()
	now := time.Now()
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return makeClusterForks(now, now.Add(-7*24*time.Hour)), "up/stream", nil
	}
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{
		"list", "up/stream", "--tier", "1", "--no-cluster",
		"--embedder", "http://127.0.0.1:1", // unreachable, but ignored
	}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d want 0 (--no-cluster should skip preflight)\nstderr=%s", exit, stderr.String())
	}
}
