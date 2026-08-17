package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

func refreshModel(t *testing.T) *Model {
	t.Helper()
	ctx, err := theme.ResolveContext("", "", "no-color", "", "ascii")
	if err != nil {
		t.Fatal(err)
	}
	m := newClusterTestModel([]ScoredFork{
		makeSF("alice/tool", 90, "", "", 0),
		makeSF("bob/tool", 80, "", "", 0),
		makeSF("carol/widget", 70, "", "", 0),
	})
	m.parent = &forge.ParentData{FullName: "owner/repo", DefaultBranch: "main"}
	m.view, m.width, m.height = viewTable, 80, 24
	// startFetch re-derives the repository from the input buffer, so a model
	// built directly needs it set or refresh short-circuits as invalid input.
	m.input = "owner/repo"
	themed := m.WithTheme(ctx)
	return &themed
}

// TestRefreshShowsProgressNotAnEmptyTable is the reported bug: pressing `r` on a
// populated table reported "No forks found." instantly. doRefresh nils the fork
// list to re-fetch it, but the table view had no loading branch, so the empty
// guard fired on the very next frame and told the user their forks were gone.
func TestRefreshShowsProgressNotAnEmptyTable(t *testing.T) {
	m := refreshModel(t)
	if _, cmd := m.handleTableKey("r"); cmd == nil {
		t.Fatal("`r` produced no fetch command")
	}
	if !m.loading {
		t.Fatal("refresh did not enter the loading state")
	}

	view := m.View()
	if strings.Contains(view, "No forks found") {
		t.Fatalf("refresh reported an empty repository while still loading:\n%s", view)
	}
	if !strings.Contains(view, "Refreshing") {
		t.Fatalf("refresh did not report progress:\n%s", view)
	}
}

// TestEmptyResultShowsWhyItIsEmpty separates "this repository has no forks" from
// "the fetch failed". The empty guard returned before the feedback line was ever
// written, so a rate-limited or rejected refresh was indistinguishable from an
// unforked repository -- the one question the user actually had.
func TestEmptyResultShowsWhyItIsEmpty(t *testing.T) {
	m := refreshModel(t)
	m.handleForksFetched(forksFetchedMsg{err: errors.New("API rate limit exceeded")})

	if m.loading {
		t.Fatal("a settled fetch left the model loading")
	}
	view := m.View()
	if !strings.Contains(view, "API rate limit exceeded") {
		t.Fatalf("empty table hid the reason it is empty:\n%s", view)
	}
}

// TestForkFetchErrorIsRenderable guards the timestamp specifically. The footer
// gates on time.Since(errMsgTime) < 5s, and the error branch set errMsg without
// ever setting errMsgTime -- so a zero timestamp made the message unrenderable
// forever. The warn branch beside it always set both.
func TestForkFetchErrorIsRenderable(t *testing.T) {
	m := refreshModel(t)
	m.handleForksFetched(forksFetchedMsg{err: errors.New("boom")})

	if m.errMsg == "" {
		t.Fatal("fetch error did not reach errMsg")
	}
	if m.errMsgTime.IsZero() {
		t.Fatal("fetch error left errMsgTime zero, so the footer can never render it")
	}
	if time.Since(m.errMsgTime) > time.Minute {
		t.Fatalf("errMsgTime is stale on arrival: %v", m.errMsgTime)
	}
}

// TestRefreshDropsInFlightEnrichment: doRefresh cancels enrichment and replaces
// the fork slice, but tier2ResultMsg values already buffered from the cancelled
// run stayed in pendingUpdates and were applied to the new list by index.
func TestRefreshDropsInFlightEnrichment(t *testing.T) {
	m := refreshModel(t)
	m.pendingUpdates = []tier2ResultMsg{{forkID: "alice/tool"}, {forkID: "bob/tool"}}

	m.doRefresh()

	if len(m.pendingUpdates) != 0 {
		t.Fatalf("refresh kept %d enrichment results from the cancelled run", len(m.pendingUpdates))
	}
}

// TestRefreshClearsRanking: the rank footer names the query the table is sorted
// by. Across a refresh the scores were dropped with the fork slice but
// rankApplied and sortCol were not, so the footer kept advertising a ranking
// that no longer existed.
func TestRefreshClearsRanking(t *testing.T) {
	m := refreshModel(t)
	m.rankApplied = "oauth refresh"
	m.rankMethod = "lexical"
	m.rankScores = map[string]float64{"alice/tool": 0.9}
	m.sortCol = "relevance"

	m.doRefresh()

	if m.rankApplied != "" || m.rankMethod != "" || m.rankScores != nil {
		t.Fatalf("refresh kept stale ranking state: applied=%q method=%q scores=%v",
			m.rankApplied, m.rankMethod, m.rankScores)
	}
	if m.sortCol == "relevance" {
		t.Fatal("refresh left the table sorted by a relevance ranking it just discarded")
	}
}
