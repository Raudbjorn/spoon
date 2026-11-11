package cluster

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/heat"
)

// TestPipelineRejectsPreValidationWebDiffCache ensures published schema3 labels
// and assignments are rebuilt, while the resulting current cache remains reusable.
func TestPipelineRejectsPreValidationWebDiffCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	forks := []EnrichedFork{
		makePipelineFork("o/a1", 3, []string{"Alpha/x.go"}, 50),
		makePipelineFork("o/a2", 4, []string{"Alpha/y.go"}, 50),
		makePipelineFork("o/a3", 5, []string{"Alpha/z.go"}, 50),
	}
	opts := PipelineOptions{
		Enabled: true, MinimumCandidates: 1, TopN: len(forks), Epsilon: 0.6,
		MinClusterSize: 3, Embedder: pipelineStubEmbedder{}, EmbedderID: "schema-test",
	}
	cfg := Config{
		EmbedderID: opts.EmbedderID, PreprocessID: "v1", Weights: [4]float32{0.3, 0.3, 0.2, 0.2},
		WeakSignalsID: "v1", Epsilon: opts.Epsilon, MinClusterSize: opts.MinClusterSize,
		TopN: opts.TopN, MinimumCandidates: opts.MinimumCandidates,
	}
	fingerprint, err := cfg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	const staleID, staleLabel = "pre-validation", "label from partial web patch"
	var members []string
	var assignments []Assignment
	for _, fork := range forks {
		members = append(members, fork.T1.ID)
		assignments = append(assignments, Assignment{ForkID: fork.T1.ID, Cluster: staleID, Novelty: 0.9})
	}
	// Schema3 was published alongside SQL v8. Pin that version so this test
	// catches stale artifacts even when the SQL migration already ran earlier.
	if err := SaveCache(ClusterCache{
		SchemaVersion: 3, ComputedAt: time.Now().UTC(), EmbedderModel: opts.EmbedderID,
		ConfigFingerprint: fingerprint, Provider: "github", Owner: "up", Repo: "stream",
		Clusters: []Cluster{{ID: staleID, Label: staleLabel, Members: members}}, Assignments: assignments,
	}); err != nil {
		t.Fatal(err)
	}
	inputs := PipelineInputs{Provider: "github", UpstreamOwner: "up", UpstreamRepo: "stream",
		Upstream: parentDataFixture(), Forks: forks}
	var log bytes.Buffer
	if skip, err := RunPipeline(t.Context(), opts, inputs, &log); err != nil || skip != nil {
		t.Fatalf("rebuild cache: skip=%+v err=%v log=%s", skip, err, log.String())
	}
	if strings.Contains(log.String(), "cache hit:") {
		t.Fatalf("reused schema3 cache instead of rebuilding: %s", log.String())
	}
	want := make([]heat.HeatResult, len(forks))
	for i, fork := range forks {
		if fork.Heat.ClusterID == staleID || fork.Heat.ClusterLabel == staleLabel {
			t.Fatalf("applied stale assignment/label to %s: %+v", fork.T1.ID, fork.Heat)
		}
		if fork.Heat.ClusterID == "" || fork.Heat.ClusterLabel == "" {
			t.Fatalf("missing rebuilt assignment/label for %s: %+v", fork.T1.ID, fork.Heat)
		}
		want[i] = *fork.Heat
		*fork.Heat = heat.HeatResult{Score: 50, Tier: 2}
	}
	if cached, ok := LoadCache("github", "up", "stream", "", opts.EmbedderID, fingerprint); !ok || cached.SchemaVersion != SchemaVersion {
		t.Fatalf("rebuilt cache is not reusable: ok=%v cache=%+v", ok, cached)
	}
	log.Reset()
	if skip, err := RunPipeline(t.Context(), opts, inputs, &log); err != nil || skip != nil {
		t.Fatalf("reuse rebuilt cache: skip=%+v err=%v log=%s", skip, err, log.String())
	}
	if !strings.Contains(log.String(), "cache hit:") {
		t.Fatalf("current cache did not take normal reuse path: %s", log.String())
	}
	for i, fork := range forks {
		if fork.Heat.ClusterID != want[i].ClusterID || fork.Heat.ClusterLabel != want[i].ClusterLabel {
			t.Errorf("current cache changed assignment/label for %s: got=%+v want=%+v", fork.T1.ID, fork.Heat, want[i])
		}
	}
}
