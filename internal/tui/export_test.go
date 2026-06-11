package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// A run that couldn't enrich every fork must mark the export degraded and OMIT
// divergence for un-enriched forks — never report it as zero (the silent
// all-zeros failure mode).
func TestDoExport_degradedOmitsDivergenceForUnenriched(t *testing.T) {
	m := &Model{
		parent: &forge.ParentData{FullName: "o/r", DefaultBranch: "main"},
		auth:   forge.AuthInfo{Provider: forge.ProviderGitHub, Host: "github.com"},
	}
	now := time.Now()
	forks := []ScoredFork{
		{
			Fork:     forge.T1Data{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now},
			Enriched: true,
			T2:       &forge.T2Data{AheadCount: 5, Diffs: []forge.FileDiff{{Path: "x.go", Additions: 10}}},
		},
		{
			Fork:          forge.T1Data{ID: "o/b", Owner: "o", Name: "b", DefaultBranch: "main", PushedAt: now},
			BudgetSkipped: true, // hit the reserve floor; never compared
		},
	}

	path := filepath.Join(t.TempDir(), "out.json")
	msg := m.doExport(forks, path)()
	if dm, ok := msg.(exportDoneMsg); !ok || dm.err != nil {
		t.Fatalf("export failed: %#v", msg)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}

	if !data.Degraded {
		t.Error("degraded should be true when a fork was left un-enriched")
	}
	if data.EnrichedCount != 1 || data.TotalCount != 2 {
		t.Errorf("counts: enriched=%d total=%d, want 1/2", data.EnrichedCount, data.TotalCount)
	}

	byName := map[string]ExportFork{}
	for _, f := range data.Forks {
		byName[f.FullName] = f
	}
	if a := byName["o/a"]; a.Divergence == nil || !a.Enriched {
		t.Errorf("o/a should be enriched with divergence; got enriched=%v div=%v", a.Enriched, a.Divergence)
	}
	if b := byName["o/b"]; b.Divergence != nil {
		t.Error("o/b (un-enriched) must OMIT divergence, not report it as zero")
	}
	if byName["o/b"].Enriched {
		t.Error("o/b should be marked enriched=false")
	}
}
