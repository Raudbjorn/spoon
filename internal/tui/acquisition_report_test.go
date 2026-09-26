package tui

import (
	"context"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// fakeForgeWithReport emits one real fork then a terminal report.
type fakeForgeWithReport struct {
	report *forge.AcquisitionReport
}

func (f *fakeForgeWithReport) ListForks(ctx context.Context, owner, repo string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg, 2)
	go func() {
		ch <- forge.ForkMsg{Fork: forge.T1Data{
			ID:         "alice/fork",
			Owner:      "alice",
			Name:       "fork",
			PushedAt:   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			IsArchived: false,
		}}
		ch <- forge.ForkMsg{Report: f.report}
		close(ch)
	}()
	return ch, nil
}

func (f *fakeForgeWithReport) Auth(ctx context.Context) (forge.AuthInfo, error) {
	return forge.AuthInfo{}, nil
}
func (f *fakeForgeWithReport) Headroom() float64 { return 1.0 }
func (f *fakeForgeWithReport) Branches(ctx context.Context, fork forge.T1Data, n int) ([]forge.BranchRef, error) {
	return nil, nil
}
func (f *fakeForgeWithReport) Compare(ctx context.Context, fork forge.T1Data, branch string) (forge.T2Data, error) {
	return forge.T2Data{}, nil
}
func (f *fakeForgeWithReport) Contributors(ctx context.Context, fork forge.T1Data) (forge.T3Data, error) {
	return forge.T3Data{}, nil
}
func (f *fakeForgeWithReport) Parent(ctx context.Context, owner, repo string) (forge.ParentData, error) {
	return forge.ParentData{}, nil
}

// fakeForgeNoReport emits one fork with no report.
type fakeForgeNoReport struct{}

func (f *fakeForgeNoReport) ListForks(ctx context.Context, owner, repo string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg, 1)
	go func() {
		ch <- forge.ForkMsg{Fork: forge.T1Data{
			ID:         "bob/fork",
			Owner:      "bob",
			Name:       "fork",
			PushedAt:   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			IsArchived: false,
		}}
		close(ch)
	}()
	return ch, nil
}

func (f *fakeForgeNoReport) Auth(ctx context.Context) (forge.AuthInfo, error) {
	return forge.AuthInfo{}, nil
}
func (f *fakeForgeNoReport) Headroom() float64 { return 1.0 }
func (f *fakeForgeNoReport) Branches(ctx context.Context, fork forge.T1Data, n int) ([]forge.BranchRef, error) {
	return nil, nil
}
func (f *fakeForgeNoReport) Compare(ctx context.Context, fork forge.T1Data, branch string) (forge.T2Data, error) {
	return forge.T2Data{}, nil
}
func (f *fakeForgeNoReport) Contributors(ctx context.Context, fork forge.T1Data) (forge.T3Data, error) {
	return forge.T3Data{}, nil
}
func (f *fakeForgeNoReport) Parent(ctx context.Context, owner, repo string) (forge.ParentData, error) {
	return forge.ParentData{}, nil
}

// TestAcquisitionReportNotTreatedAsFork: a provider that emits a terminal
// report produces exactly one scored fork and a non-nil stored report.
func TestAcquisitionReportNotTreatedAsFork(t *testing.T) {
	report := &forge.AcquisitionReport{
		Method:        "graphql",
		Scope:         "direct",
		APIVersion:    "2022-11-28",
		AuthMode:      "authenticated",
		FallbackChain: []string{"graphql"},
		Pages:         1,
		RawRows:       1,
		UniqueRows:    1,
		DuplicateRows: 0,
		CaptureAt:     time.Now(),
		AuthScopeID:   "abc123",
	}

	m := newClusterTestModel(nil)
	m.provider = &fakeForgeWithReport{report: report}

	cmd := m.fetchForks()
	if cmd == nil {
		t.Fatal("fetchForks returned nil cmd")
	}

	msg := cmd()
	fm, ok := msg.(forksFetchedMsg)
	if !ok {
		t.Fatalf("expected forksFetchedMsg, got %T", msg)
	}
	if fm.err != nil {
		t.Fatalf("unexpected error: %v", fm.err)
	}
	if fm.Report == nil {
		t.Fatal("Report is nil on forksFetchedMsg")
	}
	if fm.Report.Method != "graphql" {
		t.Errorf("Report.Method = %q, want %q", fm.Report.Method, "graphql")
	}
	// The report is a terminal message with zero Fork; it must not appear as a fork.
	if len(fm.forks) != 1 {
		t.Errorf("len(forks) = %d, want 1", len(fm.forks))
	}
	if fm.forks[0].ID != "alice/fork" {
		t.Errorf("forks[0].ID = %q, want %q", fm.forks[0].ID, "alice/fork")
	}

	// Route through handleForksFetched. It has a pointer receiver and mutates m.
	_, _ = m.handleForksFetched(fm)

	if m.acquisition == nil {
		t.Fatal("m.acquisition is nil after handleForksFetched with Report")
	}
	if m.acquisition.Method != "graphql" {
		t.Errorf("m.acquisition.Method = %q, want %q", m.acquisition.Method, "graphql")
	}
	// Fork list must have exactly one scored fork.
	if len(m.forks) != 1 {
		t.Errorf("m.forks len = %d, want 1", len(m.forks))
	}
	if m.forks[0].Fork.ID != "alice/fork" {
		t.Errorf("m.forks[0].Fork.ID = %q, want %q", m.forks[0].Fork.ID, "alice/fork")
	}
}

// TestNoReportStillWorks: a provider that never sends a report still functions.
func TestNoReportStillWorks(t *testing.T) {
	m := newClusterTestModel(nil)
	m.provider = &fakeForgeNoReport{}

	cmd := m.fetchForks()
	if cmd == nil {
		t.Fatal("fetchForks returned nil cmd")
	}

	msg := cmd()
	fm, ok := msg.(forksFetchedMsg)
	if !ok {
		t.Fatalf("expected forksFetchedMsg, got %T", msg)
	}
	if fm.err != nil {
		t.Fatalf("unexpected error: %v", fm.err)
	}
	// Report must be nil when the provider doesn't supply one.
	if fm.Report != nil {
		t.Errorf("fm.Report = %v, want nil", fm.Report)
	}
	if len(fm.forks) != 1 {
		t.Errorf("len(forks) = %d, want 1", len(fm.forks))
	}

	_, _ = m.handleForksFetched(fm)
	// No report was sent; model acquisition stays nil.
	if m.acquisition != nil {
		t.Errorf("m.acquisition = %v, want nil", m.acquisition)
	}
	if len(m.forks) != 1 {
		t.Errorf("m.forks len = %d, want 1", len(m.forks))
	}
}

// fakeForgeRepeats emits the same fork twice around a distinct one, the shape
// an unstable provider listing produced on ggml-org/llama.cpp.
type fakeForgeRepeats struct{ fakeForgeNoReport }

func (f *fakeForgeRepeats) ListForks(ctx context.Context, owner, repo string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg, 3)
	pushed := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	ch <- forge.ForkMsg{Fork: forge.T1Data{ID: "a/fork", Owner: "a", Name: "fork", PushedAt: pushed}}
	ch <- forge.ForkMsg{Fork: forge.T1Data{ID: "b/fork", Owner: "b", Name: "fork", PushedAt: pushed}}
	ch <- forge.ForkMsg{Fork: forge.T1Data{ID: "a/fork", Owner: "a", Name: "fork", PushedAt: pushed}}
	close(ch)
	return ch, nil
}

// TestFetchForksDropsRepeatedForks: a repeated fork must become one scored
// row, or it is scored, compared and exported as independent twins.
func TestFetchForksDropsRepeatedForks(t *testing.T) {
	m := newClusterTestModel(nil)
	m.provider = &fakeForgeRepeats{}

	fm, ok := m.fetchForks()().(forksFetchedMsg)
	if !ok || fm.err != nil {
		t.Fatalf("fetchForks: ok=%v err=%v", ok, fm.err)
	}
	if len(fm.forks) != 2 || fm.forks[0].ID != "a/fork" || fm.forks[1].ID != "b/fork" {
		t.Fatalf("forks = %+v, want a/fork then b/fork once each", fm.forks)
	}
	_, _ = m.handleForksFetched(fm)
	if len(m.forks) != 2 {
		t.Errorf("len(m.forks) = %d, want 2", len(m.forks))
	}
}
