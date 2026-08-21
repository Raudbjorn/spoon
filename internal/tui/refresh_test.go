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

// TestRefreshShowsProgressNotAnEmptyTable is the reported bug: pressing `r` on
// a populated table reported "No forks found." instantly. Pressing `r` used
// to wipe m.forks synchronously, so the empty-table guard fired on the very
// next frame and told the user their forks were gone. The fix keeps the
// existing fork slice visible during the refresh round-trip; the new list
// is swapped in only when the network call returns successfully.
func TestRefreshShowsProgressNotAnEmptyTable(t *testing.T) {
	m := refreshModel(t)
	before := len(m.forks)
	if before == 0 {
		t.Fatal("refreshModel seeded no forks; the test cannot prove the regression")
	}
	if _, cmd := m.handleTableKey("r"); cmd == nil {
		t.Fatal("`r` produced no fetch command")
	}
	if !m.loading {
		t.Fatal("refresh did not enter the loading state")
	}
	if got := len(m.forks); got != before {
		t.Fatalf("refresh wiped the visible fork slice: %d -> %d", before, got)
	}
	if m.parent == nil {
		t.Fatal("refresh wiped m.parent; the status bar would lose the repo name")
	}

	view := m.View()
	if strings.Contains(view, "No forks found") {
		t.Fatalf("refresh reported an empty repository while still loading:\n%s", view)
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
// TestRefreshSwapsForksOnSuccess: a successful forksFetchedMsg after a
// refresh must replace the in-memory fork slice with the new one. The
// previous behavior was identical to the wipe-on-refresh bug: the slice
// was already nil, so the success path had nothing to replace.
func TestRefreshSwapsForksOnSuccess(t *testing.T) {
	m := refreshModel(t)
	m.doRefresh()
	now := time.Now()
	m.handleForksFetched(forksFetchedMsg{forks: []forge.T1Data{
		{ID: "dave", Owner: "dave", Name: "forks", PushedAt: now},
		{ID: "eve", Owner: "eve", Name: "forks", PushedAt: now},
	}})
	if len(m.forks) != 2 || m.forks[0].Fork.ID != "dave" || m.forks[1].Fork.ID != "eve" {
		t.Fatalf("success did not swap in the new forks: %+v", m.forks)
	}
}

// TestRefreshKeepsForksOnError: a failed forksFetchedMsg after a refresh
// must leave the existing fork slice visible. The user pressed `r` to see
// what changed, and a network blip is not the same as "I have no forks".
func TestRefreshKeepsForksOnError(t *testing.T) {
	m := refreshModel(t)
	before := len(m.forks)
	if before == 0 {
		t.Fatal("refreshModel seeded no forks; cannot prove retention")
	}
	m.doRefresh()
	m.handleForksFetched(forksFetchedMsg{err: errors.New("503 Service Unavailable")})

	if len(m.forks) != before {
		t.Fatalf("refresh error wiped the existing forks: %d -> %d", before, len(m.forks))
	}
	if m.loading {
		t.Fatal("a settled fetch left the model loading")
	}
}

// TestRefreshPathIsNotANoOp: the slice survives doRefresh because the
// m.refresh flag is latched before startFetch, and startFetch now skips
// the m.forks/m.parent wipe on a refresh. The wipe that lived in
// doRefresh() was removed because it raced the wipe in startFetch and
// pulled the table out from under the user before the new forks arrived.
func TestRefreshPathIsNotANoOp(t *testing.T) {
	m := refreshModel(t)
	m.refresh = false
	m.doRefresh()
	if !m.refresh {
		t.Fatal("doRefresh did not latch the refresh flag")
	}
	if len(m.forks) == 0 {
		t.Fatal("doRefresh wiped the visible forks; the refresh flag should suppress the wipe")
	}
}

