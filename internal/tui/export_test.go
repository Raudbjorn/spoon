package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// The export must say when the fork list itself fell short of the provider's
// count: missing forks are invisible in the per-row counts.
func TestDoExport_listingReportsShortfall(t *testing.T) {
	now := time.Now()
	forks := []ScoredFork{{Fork: forge.T1Data{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now}}}
	m := &Model{
		parent:      &forge.ParentData{FullName: "o/r", DefaultBranch: "main"},
		auth:        forge.AuthInfo{Provider: forge.ProviderGitHub, Host: "github.com"},
		forks:       forks,
		acquisition: &forge.AcquisitionReport{Method: "graphql", ExpectedRows: 3, DuplicateRows: 2},
	}
	path := filepath.Join(t.TempDir(), "out.json")
	if dm, ok := m.doExport(forks, path)().(exportDoneMsg); !ok || dm.err != nil {
		t.Fatalf("export failed: %#v", dm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	want := ExportListing{Listed: 1, Expected: 3, RepeatsDropped: 2, Method: "graphql"}
	if data.Listing == nil || *data.Listing != want {
		t.Errorf("listing = %+v, want %+v", data.Listing, want)
	}
}

// Listed counts every unique fork the provider returned, so rows dropped as
// unreachable still count and listed, expected and unreachable reconcile.
func TestExportListing_listedCountsDroppedRows(t *testing.T) {
	report := &forge.AcquisitionReport{Method: "graphql", ExpectedRows: 100, UniqueRows: 100}
	got := exportListing(report, 90, 10)
	if got == nil || got.Listed != 100 || got.Unreachable != 10 || got.Expected != 100 {
		t.Errorf("listing = %+v, want listed 100 (not the 90 survivors), unreachable 10, expected 100", got)
	}
	// No unique count in the report: rebuild it from survivors plus dropped.
	got = exportListing(&forge.AcquisitionReport{ExpectedRows: 100}, 90, 10)
	if got == nil || got.Listed != 100 {
		t.Errorf("fallback listed = %+v, want 100", got)
	}
}

// Rows dropped as gone were listed, so they must not read as a listing
// shortfall in the status bar (they have their own "gone" figure).
func TestStatusBar_goneRowsAreNotListingShortfall(t *testing.T) {
	m := movementModel(3)
	m.parent = &forge.ParentData{FullName: "o/r"}
	m.acquisition = &forge.AcquisitionReport{ExpectedRows: 5}
	m.unreachable = 2 // 3 survivors + 2 gone = the 5 listed
	bar := m.renderStatusBar()
	if strings.Contains(bar, "forks listed") {
		t.Errorf("status bar = %q, reports a shortfall for rows that were listed", bar)
	}
	if !strings.Contains(bar, "2 gone") {
		t.Errorf("status bar = %q, want the gone figure", bar)
	}
	m.acquisition = &forge.AcquisitionReport{ExpectedRows: 9}
	if bar := m.renderStatusBar(); !strings.Contains(bar, "5 of 9 forks listed") {
		t.Errorf("status bar = %q, want a real shortfall as 5 of 9", bar)
	}
}

// Listing figures belong to the list they were captured for: a cached load
// carries no report, so the previous repository's must not survive it.
func TestHandleCachedLoad_clearsPreviousListingState(t *testing.T) {
	m := movementModel(1)
	m.provider = &tierFakeForge{headroom: 1}
	m.acquisition = &forge.AcquisitionReport{Method: "graphql", ExpectedRows: 99, DuplicateRows: 3}
	m.unreachable = 4
	m.handleCachedLoad(cachedLoadMsg{parent: forge.ParentData{FullName: "o/other"}})
	if m.acquisition != nil || m.unreachable != 0 {
		t.Errorf("after cached load: acquisition=%+v unreachable=%d, want nil and 0", m.acquisition, m.unreachable)
	}
}
