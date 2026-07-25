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
	t.Setenv("SPOON_NO_CONFIG", "1") // isolate from the host's spoon config
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
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

// makeClusterForks returns ten fake forks split across two axes so the
// cluster pipeline's minimum-candidate gate is satisfied.
func makeClusterForks(now time.Time, parentPushed time.Time) *fakeForge {
	ids := []string{"o/a1", "o/a2", "o/a3", "o/a4", "o/a5", "o/z1", "o/z2", "o/z3", "o/z4", "o/z5"}
	paths := map[string]string{}
	for _, id := range ids {
		if strings.Contains(id, "/a") {
			paths[id] = "Alpha/" + id[3:] + ".go"
		} else {
			paths[id] = "Zeta/" + id[3:] + ".go"
		}
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
	t.Setenv("SPOON_NO_CONFIG", "1") // isolate from the host's spoon config
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
	if len(lines) != 10 {
		t.Fatalf("expected 10 NDJSON lines, got %d:\n%s", len(lines), stdout.String())
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
	t.Setenv("SPOON_NO_CONFIG", "1") // isolate from the host's spoon config
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
		if _, ok := obj["networkRank"]; ok {
			t.Errorf("--no-cluster: networkRank should be omitted, got: %s", line)
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

func TestForkToJSON_EmitsVisibilityVisible(t *testing.T) {
	out := forkToJSON(forksops.Result{Fork: forge.T1Data{ID: "o/r", Owner: "o", Name: "r"}})
	visibility, ok := out["visibility"].(map[string]any)
	if !ok {
		t.Fatalf("visibility missing or wrong type: %T", out["visibility"])
	}
	if visibility["status"] != "visible" {
		t.Fatalf("visibility.status = %v, want visible", visibility["status"])
	}
	if _, ok := visibility["reasons"]; ok {
		t.Fatalf("visibility.reasons should be omitted for visible fork: %+v", visibility["reasons"])
	}
}

func TestForkToJSON_EmitsVisibilityDemoted(t *testing.T) {
	out := forkToJSON(forksops.Result{
		Fork: forge.T1Data{ID: "o/r", Owner: "o", Name: "r"},
		Heat: heat.HeatResult{Penalties: []string{"archived", "low_recency"}},
	})
	visibility := out["visibility"].(map[string]any)
	if visibility["status"] != "demoted" {
		t.Fatalf("visibility.status = %v, want demoted", visibility["status"])
	}
	reasons, ok := visibility["reasons"].([]string)
	if !ok {
		t.Fatalf("visibility.reasons missing or wrong type: %T", visibility["reasons"])
	}
	if len(reasons) != 2 || reasons[0] != "archived" || reasons[1] != "low_recency" {
		t.Fatalf("visibility.reasons = %#v, want archived, low_recency", reasons)
	}
}

func TestForkToJSON_EmitsVisibilityHidden(t *testing.T) {
	for _, penalty := range []string{"upstreamed", "no_ahead"} {
		out := forkToJSON(forksops.Result{
			Fork: forge.T1Data{ID: "o/r", Owner: "o", Name: "r"},
			Heat: heat.HeatResult{Penalties: []string{penalty}},
		})
		visibility := out["visibility"].(map[string]any)
		if visibility["status"] != "hidden" {
			t.Fatalf("penalty %s: visibility.status = %v, want hidden", penalty, visibility["status"])
		}
	}
}

func TestForkToJSON_EmitsDegradedStages(t *testing.T) {
	out := forkToJSON(forksops.Result{
		Fork: forge.T1Data{ID: "o/r", Owner: "o", Name: "r"},
		BudgetSkip: &forksops.StageSkip{
			Stage:  "compare",
			ForkID: "o/r",
			Reason: "rate reserve",
		},
		T3Skip: &forksops.StageSkip{
			Stage:  "contributors",
			ForkID: "o/r",
			Reason: "contributors o/r: stats timeout",
		},
		OwnerProfileSkip: &forksops.StageSkip{
			Stage:  "owner_profile",
			ForkID: "o/r",
			Reason: "owner cap",
		},
	})
	degraded, ok := out["degraded"].([]map[string]any)
	if !ok {
		t.Fatalf("degraded missing or wrong type: %T", out["degraded"])
	}
	if len(degraded) != 3 {
		t.Fatalf("degraded len = %d, want 3", len(degraded))
	}
	wantStages := []string{"compare", "contributors", "owner_profile"}
	wantReasons := []string{"rate reserve", "contributors o/r: stats timeout", "owner cap"}
	for i := range wantStages {
		if degraded[i]["stage"] != wantStages[i] || degraded[i]["reason"] != wantReasons[i] {
			t.Fatalf("degraded[%d] = %+v, want stage=%s reason=%s", i, degraded[i], wantStages[i], wantReasons[i])
		}
	}
}

func TestForkToJSON_EmitsNetworkRank(t *testing.T) {
	out := forkToJSON(forksops.Result{
		Fork: forge.T1Data{ID: "o/r", Owner: "o", Name: "r"},
		NetworkRank: &forksops.NetworkRank{
			Position:   3,
			Total:      147,
			Percentile: 0.986,
			Band:       forksops.RankBandTop5Pct,
		},
	})
	rank, ok := out["networkRank"].(map[string]any)
	if !ok {
		t.Fatalf("networkRank missing or wrong type: %T", out["networkRank"])
	}
	if rank["position"] != 3 || rank["total"] != 147 || rank["percentile"] != 0.986 || rank["band"] != "top_5pct" {
		t.Fatalf("networkRank = %+v", rank)
	}
}

func TestForkToJSON_EmitsMomentum(t *testing.T) {
	out := forkToJSON(forksops.Result{
		Fork: forge.T1Data{ID: "o/r", Owner: "o", Name: "r"},
		Momentum: forksops.MomentumInfo{
			Status:           forksops.MomentumRising,
			StarsDelta30d:    12,
			SubForksDelta30d: 3,
			ObservedDays:     30,
		},
	})
	momentum, ok := out["momentum"].(map[string]any)
	if !ok {
		t.Fatalf("momentum missing or wrong type: %T", out["momentum"])
	}
	if momentum["status"] != "rising" || momentum["starsDelta30d"] != 12 || momentum["subForksDelta30d"] != 3 || momentum["observedDays"] != 30 {
		t.Fatalf("momentum = %+v", momentum)
	}
}

func TestSpnForksList_csv_emitsHeaderAndRows(t *testing.T) {
	t.Setenv("SPOON_NO_CONFIG", "1")         // isolate from the host's spoon config
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // don't read the real embedder config
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
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
	t.Setenv("SPOON_NO_CONFIG", "1") // isolate from the host's spoon config
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
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

func TestSpnForksList_unknownEmbedderFlag_rejected(t *testing.T) {
	// The external-embedder flags were removed along with the external
	// services; passing one must fail fast with a structured bad_input error.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{
		"list", "up/stream",
		"--embedder", "http://127.0.0.1:1",
	}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d want 2\nstderr=%s", exit, stderr.String())
	}
	var env map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr is not a JSON error envelope: %v\n%s", err, stderr.String())
	}
	e, _ := env["error"].(map[string]any)
	if e == nil || e["code"] != "bad_input" {
		t.Errorf("expected bad_input error, got: %s", stderr.String())
	}
}

func TestSpnForksList_siblingSimModeConflict(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{
		"list", "up/stream",
		"--sibling-sim-mode", "fork_intent",
		"--no-sibling-sim",
	}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d want 2\nstderr=%s", exit, stderr.String())
	}
	var env map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr is not a JSON error envelope: %v\n%s", err, stderr.String())
	}
	e, _ := env["error"].(map[string]any)
	if e == nil || e["code"] != "bad_input" {
		t.Errorf("expected bad_input error, got: %s", stderr.String())
	}
	if msg, _ := e["message"].(string); !strings.Contains(msg, "conflicts with --no-sibling-sim") {
		t.Errorf("unexpected message: %s", stderr.String())
	}
}

func TestSpnForksList_topicLanesRequireTopicMode(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{
		"list", "up/stream",
		"--topic-lanes", "stars,updated",
	}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d want 2\nstderr=%s", exit, stderr.String())
	}
	var env map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr is not a JSON error envelope: %v\n%s", err, stderr.String())
	}
	e, _ := env["error"].(map[string]any)
	if e == nil || e["code"] != "bad_input" {
		t.Errorf("expected bad_input error, got: %s", stderr.String())
	}
	if msg, _ := e["message"].(string); !strings.Contains(msg, "only apply to topic:NAME mode") {
		t.Errorf("unexpected message: %s", stderr.String())
	}
}

func TestForkToJSON_EmitsPriorScore(t *testing.T) {
	// A prior match emits both fields.
	out := forkToJSON(forksops.Result{
		Fork:         forge.T1Data{ID: "o/r", Owner: "o", Name: "r"},
		PriorScore:   1,
		PriorReasons: []string{"path:internal/auth"},
	})
	if s, ok := out["priorScore"].(float64); !ok || s != 1 {
		t.Fatalf("priorScore = %v (%T), want 1", out["priorScore"], out["priorScore"])
	}
	reasons, ok := out["priorReasons"].([]string)
	if !ok || len(reasons) != 1 || reasons[0] != "path:internal/auth" {
		t.Fatalf("priorReasons = %#v, want [path:internal/auth]", out["priorReasons"])
	}

	// A deny-only match scores 0 but keeps its reason, so it still emits.
	deny := forkToJSON(forksops.Result{
		Fork:         forge.T1Data{ID: "o/r", Owner: "o", Name: "r"},
		PriorReasons: []string{"owner_deny:farmer"},
	})
	if s, ok := deny["priorScore"].(float64); !ok || s != 0 {
		t.Errorf("deny-only priorScore should emit as 0, got %v", deny["priorScore"])
	}
	if _, ok := deny["priorReasons"]; !ok {
		t.Errorf("deny-only priorReasons should be emitted")
	}

	// No priors ran → both fields omitted (omission = not computed).
	bare := forkToJSON(forksops.Result{Fork: forge.T1Data{ID: "o/r", Owner: "o", Name: "r"}})
	if _, ok := bare["priorScore"]; ok {
		t.Errorf("priorScore should be omitted when no priors ran: %v", bare["priorScore"])
	}
	if _, ok := bare["priorReasons"]; ok {
		t.Errorf("priorReasons should be omitted when no priors ran: %v", bare["priorReasons"])
	}
}

func TestForkToJSON_EmitsProfile(t *testing.T) {
	// A hidden/upstreamed result derives profile "hidden" with its reasons.
	hidden := forkToJSON(forksops.Result{
		Fork:       forge.T1Data{ID: "o/r", Owner: "o", Name: "r"},
		Visibility: forksops.VisibilityDecision{Status: forksops.VisibilityHidden, Reasons: []string{"upstreamed"}},
	})
	if hidden["profile"] != "hidden" {
		t.Fatalf("profile = %v, want hidden", hidden["profile"])
	}
	reasons, ok := hidden["profileReasons"].([]string)
	if !ok || len(reasons) != 1 || reasons[0] != "upstreamed" {
		t.Fatalf("profileReasons = %#v, want [upstreamed]", hidden["profileReasons"])
	}

	// A plain enriched fork is "standard" with no reasons.
	plain := forkToJSON(forksops.Result{Fork: forge.T1Data{ID: "o/r", Owner: "o", Name: "r"}})
	if plain["profile"] != "standard" {
		t.Fatalf("profile = %v, want standard", plain["profile"])
	}
	if _, ok := plain["profileReasons"]; ok {
		t.Errorf("profileReasons should be omitted for the standard floor: %v", plain["profileReasons"])
	}
}

func TestSpnForksList_priorsFlag_rejectsMissingFile(t *testing.T) {
	t.Setenv("SPOON_NO_CONFIG", "1")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{
		"list", "o/r",
		"--priors", t.TempDir() + "/does-not-exist.json",
	}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d want 2\nstderr=%s", exit, stderr.String())
	}
	var env map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr is not a JSON error envelope: %v\n%s", err, stderr.String())
	}
	e, _ := env["error"].(map[string]any)
	if e == nil || e["code"] != "bad_input" {
		t.Errorf("expected bad_input error, got: %s", stderr.String())
	}
}

func TestSpnForksList_defaultOutputHasNoPriorFields(t *testing.T) {
	t.Setenv("SPOON_NO_CONFIG", "1")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
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
			t.Fatalf("invalid JSON line %q: %v", line, err)
		}
		if _, ok := obj["priorScore"]; ok {
			t.Errorf("default output must not contain priorScore: %s", line)
		}
		if _, ok := obj["priorReasons"]; ok {
			t.Errorf("default output must not contain priorReasons: %s", line)
		}
		// profile is the one net-new always-on field and must be present.
		if _, ok := obj["profile"]; !ok {
			t.Errorf("every record should carry a profile: %s", line)
		}
	}
}

// splitRepoArg splits the owner/repo key used by search, eval, repo and threads
// commands. The parsing is pure and deterministic; callers must treat empty
// owner or repo as malformed (and reject it) to avoid routing queries to the
// wrong store rows (#87).
func TestSplitRepoArg(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		owner string
		repo  string
	}{
		{"standard owner and repo", "owner/repo", "owner", "repo"},
		{"multiple slashes", "a/b/c", "a", "b/c"}, // SplitN keeps the remainder as the repo name
		{"leading slash", "/repo", "", "repo"},
		{"trailing slash", "owner/", "owner", ""},
		{"no slash", "noslash", "", ""},
		{"empty string", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo := splitRepoArg(tc.in)
			if owner != tc.owner || repo != tc.repo {
				t.Fatalf("splitRepoArg(%q) = (%q, %q), want (%q, %q)", tc.in, owner, repo, tc.owner, tc.repo)
			}
		})
	}
}
