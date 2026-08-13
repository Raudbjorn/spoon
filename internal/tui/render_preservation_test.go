package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestMainRenderingPreservesInputAndEmptyStates(t *testing.T) {
	m := NewModel(nil, forge.AuthInfo{}, "", false)
	m.width, m.height = 120, 30
	m.authMsg = "Not authenticated fixture"
	m.input, m.inputCursor = "owner/repo", len([]rune("owner/repo"))
	m.inputErr = "invalid repository"
	for _, want := range []string{"Not authenticated fixture", "owner/repo", "invalid repository"} {
		if rendered := m.View(); !strings.Contains(rendered, want) {
			t.Fatalf("input view lost %q:\n%s", want, rendered)
		}
	}
	m.loading = true
	m.loadMsg = "Loading fork network"
	rendered := m.View()
	if !strings.Contains(rendered, m.loadMsg) {
		t.Fatalf("loading input lost its status message:\n%s", rendered)
	}
	if strings.Contains(rendered, "Enter search") {
		t.Fatalf("loading input must suppress the ready action:\n%s", rendered)
	}

	empty := newClusterTestModel(nil)
	if rendered := empty.View(); !strings.Contains(rendered, "No forks found") {
		t.Fatalf("empty table view lost its state:\n%s", rendered)
	}
}

func TestMainTableRenderingPreservesOperationalStatus(t *testing.T) {
	m := filterModel()
	m.width, m.height = 120, 30
	m.provider = &tierFakeForge{headroom: 0.5}
	m.auth = forge.AuthInfo{RateLimit: 10}
	m.enriching, m.enrichDone, m.enrichTotal = true, 1, 4
	m.forks[0].TierSkipped = true
	m.setMaxTier(2)
	m.forks[0].Marked = true
	m.clipMsg, m.clipMsgTime = "Loaded 4 forks from cache", time.Now()
	rendered := m.View()
	for _, want := range []string{"Loaded 4 forks from cache", "T2: 1/4", "T<=2", "API: 5/10", "1 marked"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("table operational state lost %q:\n%s", want, rendered)
		}
	}

	m.clipMsg = ""
	m.rankQuery, m.rankApplied, m.rankMethod, m.rankPending = "terminal", "terminal", "lexical", true
	if rendered = m.View(); !strings.Contains(rendered, "Ranking by") {
		t.Fatalf("rank progress/footer state missing:\n%s", rendered)
	}
	m.rankPending = false
	m.errMsg, m.errMsgTime = "ranking failed", time.Now()
	if rendered = m.View(); !strings.Contains(rendered, "ranking failed") {
		t.Fatalf("ranking error state missing:\n%s", rendered)
	}
}
