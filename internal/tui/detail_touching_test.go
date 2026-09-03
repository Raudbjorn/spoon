package tui

import (
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestDetailShowsTouchedFiles(t *testing.T) {
	m := newTestModel() // internal/tui/cluster_bridge_test.go:14, returns *Model
	m.forks = []ScoredFork{{Fork: forge.T1Data{ID: "o/hit"}, T2: &forge.T2Data{Performed: true, AheadCount: 1, Diffs: []forge.FileDiff{{Path: "cli/registry/antipatterns.mjs", Status: "modified", Additions: 7, Deletions: 1}}}}}
	m.cursor = 0
	m.applyFilter("path:**/antipatterns.mjs")
	body := m.detailBody()
	for _, want := range []string{"Touches **/antipatterns.mjs", "modified cli/registry/antipatterns.mjs (+7/-1)"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q:\n%s", want, body)
		}
	}
}
