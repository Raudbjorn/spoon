package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// stubScorer scores by looking for a marker substring, so the expected ranking is
// unambiguous. A non-nil err makes it fail, so the degrade path is testable.
type stubScorer struct {
	marker string
	method string
	err    error
}

func (s stubScorer) Method() string {
	if s.method == "" {
		return "stub"
	}
	return s.method
}

func (s stubScorer) Rerank(_ context.Context, _ string, docs []string) ([]float64, error) {
	if s.err != nil {
		return nil, s.err
	}
	scores := make([]float64, len(docs))
	for i, doc := range docs {
		if strings.Contains(doc, s.marker) {
			scores[i] = 0.9
		} else {
			scores[i] = 0.1
		}
	}
	return scores, nil
}

// forkWithCommits builds a ScoredFork whose digest carries the given commit
// subject, so the scorer has something to judge.
func forkWithCommits(id string, heatScore float64, subject string) ScoredFork {
	return ScoredFork{
		Fork: forge.T1Data{ID: id, Owner: "o", Name: id},
		Heat: heat.HeatResult{Score: heatScore},
		T2: &forge.T2Data{
			Performed:  true,
			AheadCount: 1,
			Commits:    []forge.AheadCommit{{SHA: "abc", Message: subject}},
			Diffs:      []forge.FileDiff{{Path: "internal/thing.go", Additions: 1}},
		},
	}
}

func newRankModel(scorer embed.QueryScorer, forks ...ScoredFork) Model {
	m := NewModel(nil, forge.AuthInfo{}, "", false).WithQueryScorer(scorer)
	m.forks = forks
	m.view = viewTable
	m.sortCol = "heat"
	return m
}

// TestRankRanksByRelevanceNotHeat is the point of the feature: the fork the
// query is about must come first even when another fork is hotter.
func TestRankRanksByRelevanceNotHeat(t *testing.T) {
	m := newRankModel(stubScorer{marker: "wayland", method: "voyage"},
		forkWithCommits("hot", 99, "bump dependencies"),
		forkWithCommits("relevant", 1, "add wayland compositor support"),
	)
	// Heat order first, so a passing result cannot be the input order.
	m.reapplySort()
	if m.forks[0].Fork.ID != "hot" {
		t.Fatalf("pre-filter order = %s, want the hottest fork first", m.forks[0].Fork.ID)
	}

	cmd := m.startRankScoring("wayland support")
	if cmd == nil {
		t.Fatal("startRankScoring returned no command")
	}
	msg, ok := cmd().(rankResultMsg)
	if !ok {
		t.Fatalf("command produced %T, want rankResultMsg", cmd())
	}
	if msg.err != nil {
		t.Fatalf("scoring failed: %v", msg.err)
	}
	updated, _ := m.handleRankResult(msg)
	m = *(updated.(*Model))

	if m.forks[0].Fork.ID != "relevant" {
		t.Errorf("ranked order = %s first, want \"relevant\"", m.forks[0].Fork.ID)
	}
	if m.sortCol != relevanceSortCol {
		t.Errorf("sortCol = %q, want %q", m.sortCol, relevanceSortCol)
	}
	// The footer must name the scorer: a Voyage ranking and a lexical one are not
	// the same judgment, and the user should not have to guess which they see.
	if footer := m.rankFooter(); !strings.Contains(footer, "voyage") || !strings.Contains(footer, "wayland support") {
		t.Errorf("footer = %q, want it to name the query and the scorer", footer)
	}
}

// TestRankClearRestoresHeatOrder: Esc must undo the ranking, not leave the
// table sorted by a column the user can no longer see the basis for.
func TestRankClearRestoresHeatOrder(t *testing.T) {
	m := newRankModel(stubScorer{marker: "wayland"},
		forkWithCommits("hot", 99, "bump dependencies"),
		forkWithCommits("relevant", 1, "add wayland compositor support"),
	)
	msg := m.startRankScoring("wayland")().(rankResultMsg)
	updated, _ := m.handleRankResult(msg)
	m = *(updated.(*Model))
	if m.forks[0].Fork.ID != "relevant" {
		t.Fatalf("ranking did not apply")
	}

	m.clearRank()
	if m.sortCol != "heat" {
		t.Errorf("sortCol = %q after clearing, want heat", m.sortCol)
	}
	if m.forks[0].Fork.ID != "hot" {
		t.Errorf("order after clearing = %s first, want the heat order back", m.forks[0].Fork.ID)
	}
	if m.rankFooter() != "" {
		t.Errorf("footer = %q after clearing, want empty", m.rankFooter())
	}
}

// TestRankDiscardsStaleResults: a slow request that lands after the user has
// moved on must not overwrite the newer ranking.
func TestRankDiscardsStaleResults(t *testing.T) {
	m := newRankModel(stubScorer{marker: "wayland"},
		forkWithCommits("a", 1, "add wayland support"),
	)
	stale := m.startRankScoring("first query")().(rankResultMsg)
	// A second submission supersedes the first before its result arrives.
	fresh := m.startRankScoring("second query")().(rankResultMsg)

	updated, _ := m.handleRankResult(fresh)
	m = *(updated.(*Model))
	updated, _ = m.handleRankResult(stale)
	m = *(updated.(*Model))

	if m.rankApplied != "second query" {
		t.Errorf("rankApplied = %q, want the newer query to win", m.rankApplied)
	}
}

// TestRankDegradesOnScorerFailure: a Voyage outage must leave the table usable.
func TestRankDegradesOnScorerFailure(t *testing.T) {
	m := newRankModel(stubScorer{err: errors.New("voyage is down")},
		forkWithCommits("a", 5, "some work"),
		forkWithCommits("b", 9, "other work"),
	)
	m.reapplySort()
	msg := m.startRankScoring("anything")().(rankResultMsg)
	if msg.err == nil {
		t.Fatal("expected the scorer failure to be reported")
	}
	updated, _ := m.handleRankResult(msg)
	m = *(updated.(*Model))

	if m.sortCol == relevanceSortCol {
		t.Error("a failed ranking installed the relevance sort anyway")
	}
	if m.forks[0].Fork.ID != "b" {
		t.Errorf("order = %s first, want the heat order preserved", m.forks[0].Fork.ID)
	}
	if !strings.Contains(m.errMsg, "Rank failed") {
		t.Errorf("errMsg = %q, want it to report the failure", m.errMsg)
	}
	if m.rankPending {
		t.Error("rankPending stayed true after a failure; the footer would spin forever")
	}
}

// TestRankWithoutCompareDataReportsWhy: an un-enriched table has nothing to
// judge, and saying so beats silently doing nothing.
func TestRankWithoutCompareDataReportsWhy(t *testing.T) {
	m := newRankModel(stubScorer{marker: "x"},
		ScoredFork{Fork: forge.T1Data{ID: "bare", Owner: "o", Name: "bare"}},
	)
	msg := m.startRankScoring("anything")().(rankResultMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "compare data") {
		t.Errorf("err = %v, want it to explain that no fork has compare data yet", msg.err)
	}
}

// TestRankDefaultsToLexicalScorer: the TUI must work with no key, no network
// and no configuration.
func TestRankDefaultsToLexicalScorer(t *testing.T) {
	m := newRankModel(nil, forkWithCommits("a", 1, "add wayland support"))
	msg := m.startRankScoring("wayland")().(rankResultMsg)
	if msg.err != nil {
		t.Fatalf("lexical fallback failed: %v", msg.err)
	}
	if msg.method != embed.LexicalQueryMethod {
		t.Errorf("method = %q, want %q", msg.method, embed.LexicalQueryMethod)
	}
}

// TestRankKeyOpensAndSubmits covers the prompt itself: typing must not issue a
// request, and Enter must.
func TestRankKeyOpensAndSubmits(t *testing.T) {
	m := newRankModel(stubScorer{marker: "wayland"}, forkWithCommits("a", 1, "add wayland support"))
	m.promptRank()
	if m.view != viewRank {
		t.Fatalf("view = %v, want viewRank", m.view)
	}
	var cmd tea.Cmd
	for _, ch := range "way" {
		var updated tea.Model
		updated, cmd = m.handleRankKey(string(ch), string(ch))
		m = *(updated.(*Model))
		if cmd != nil {
			t.Fatal("typing issued a command; scoring must wait for Enter")
		}
	}
	if m.rankQuery != "way" {
		t.Errorf("rankQuery = %q, want %q", m.rankQuery, "way")
	}
	updated, cmd := m.handleRankKey("enter", "")
	m = *(updated.(*Model))
	if cmd == nil {
		t.Error("Enter did not start scoring")
	}
	if m.view != viewTable {
		t.Errorf("view = %v after Enter, want viewTable", m.view)
	}
}

// TestRankOnlyScoresVisibleForks is the compose contract between the two
// features: `/` narrows, `R` orders the survivors. Ranking rows the filter has
// hidden would pay for scores nobody can see.
func TestRankOnlyScoresVisibleForks(t *testing.T) {
	m := newRankModel(stubScorer{marker: "wayland"},
		forkWithCommits("keep-me", 1, "add wayland compositor support"),
		forkWithCommits("hidden", 9, "add wayland protocol support"),
	)
	m.applyFilter("keep")
	if m.visibleCount() != 1 {
		t.Fatalf("visibleCount = %d, want 1", m.visibleCount())
	}

	msg := m.startRankScoring("wayland")().(rankResultMsg)
	if msg.err != nil {
		t.Fatalf("scoring failed: %v", msg.err)
	}
	if len(msg.scores) != 1 {
		t.Fatalf("scored %d forks, want only the 1 the filter admits", len(msg.scores))
	}
	if _, ok := msg.scores["hidden"]; ok {
		t.Error("a filtered-out fork was scored")
	}
}

// TestRankEmptyQueryClears mirrors how `/` treats an empty query, so the two
// prompts behave the same way.
func TestRankEmptyQueryClears(t *testing.T) {
	m := newRankModel(stubScorer{marker: "wayland"},
		forkWithCommits("hot", 99, "bump dependencies"),
		forkWithCommits("relevant", 1, "add wayland support"),
	)
	msg := m.startRankScoring("wayland")().(rankResultMsg)
	updated, _ := m.handleRankResult(msg)
	m = *(updated.(*Model))
	if m.sortCol != relevanceSortCol {
		t.Fatalf("ranking did not apply")
	}

	m.promptRank()
	m.rankQuery = "   "
	updated, cmd := m.handleRankKey("enter", "")
	m = *(updated.(*Model))
	if cmd != nil {
		t.Error("an empty query issued a scoring request")
	}
	if m.sortCol != "heat" {
		t.Errorf("sortCol = %q, want heat — an empty query clears the ranking", m.sortCol)
	}
}

// TestRankEscKeepsActiveRanking: Esc cancels the edit, matching `/`, where Esc in
// the prompt cancels and Esc on the table clears.
func TestRankEscKeepsActiveRanking(t *testing.T) {
	m := newRankModel(stubScorer{marker: "wayland"},
		forkWithCommits("relevant", 1, "add wayland support"),
	)
	msg := m.startRankScoring("wayland")().(rankResultMsg)
	updated, _ := m.handleRankResult(msg)
	m = *(updated.(*Model))

	m.promptRank()
	updated, _ = m.handleRankKey("esc", "")
	m = *(updated.(*Model))
	if m.view != viewTable {
		t.Errorf("view = %v after Esc, want viewTable", m.view)
	}
	if m.rankApplied != "wayland" {
		t.Errorf("rankApplied = %q, want the active ranking left alone by Esc", m.rankApplied)
	}
}

// TestFilterAndRankAreIndependentState guards the collision this reconciliation
// exists to avoid: #105's filter and this ranking must not share fields.
func TestFilterAndRankAreIndependentState(t *testing.T) {
	m := newRankModel(stubScorer{marker: "wayland"},
		forkWithCommits("a-wayland", 1, "add wayland support"),
		forkWithCommits("b-other", 9, "bump deps"),
	)
	m.applyFilter("wayland")
	msg := m.startRankScoring("compositor")().(rankResultMsg)
	updated, _ := m.handleRankResult(msg)
	m = *(updated.(*Model))

	if m.filter != "wayland" {
		t.Errorf("filter = %q, want the ranking to leave it untouched", m.filter)
	}
	if m.rankApplied != "compositor" {
		t.Errorf("rankApplied = %q, want %q", m.rankApplied, "compositor")
	}
	m.applyFilter("")
	if m.rankApplied != "compositor" {
		t.Error("clearing the filter also cleared the ranking")
	}
}
