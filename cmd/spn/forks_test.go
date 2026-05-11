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

func TestSpnForksList_embedderUnreachable_emitsWarning(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prev := providerFactory
	defer func() { providerFactory = prev }()

	now := time.Now()
	parentPushed := now.Add(-7 * 24 * time.Hour)
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return makeClusterForks(now, parentPushed), "up/stream", nil
	}

	// No embedder hook → real SelectEmbedder runs, points at a guaranteed-
	// unreachable endpoint, returns ollama_unreachable skip.
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{
		"list", "up/stream",
		"--tier", "2",
		"--embedder", "http://127.0.0.1:1",
		"--cluster-epsilon", "0.6",
		"--cluster-min-size", "3",
	}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}

	// Cluster fields should be absent.
	for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if _, ok := obj["clusterId"]; ok {
			t.Errorf("expected no clusterId when embedder unreachable, got: %s", line)
		}
	}

	// stderr should contain exactly one warning envelope. Find the first JSON
	// object on a line and inspect it.
	var warning map[string]any
	for _, line := range strings.Split(stderr.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if _, ok := obj["warning"]; ok {
			warning = obj
			break
		}
	}
	if warning == nil {
		t.Fatalf("expected a warning envelope on stderr, got:\n%s", stderr.String())
	}
	w, _ := warning["warning"].(map[string]any)
	if w == nil {
		t.Fatalf("warning envelope shape unexpected: %+v", warning)
	}
	code, _ := w["code"].(string)
	if code == "" {
		t.Errorf("warning missing code; got %+v", w)
	}
	if _, ok := w["remediation"].(string); !ok {
		t.Errorf("warning missing remediation; got %+v", w)
	}
}
