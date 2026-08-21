package forksops

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// fakeForgeReport is a minimal forge.Forge that returns a single fork
// then sends a terminal AcquisitionReport on the channel.
type fakeForgeReport struct {
	report *forge.AcquisitionReport
	forks  []forge.T1Data
}

func (f *fakeForgeReport) Auth(_ context.Context) (forge.AuthInfo, error) {
	return forge.AuthInfo{}, nil
}
func (f *fakeForgeReport) Headroom() float64 { return 0 }
func (f *fakeForgeReport) Parent(_ context.Context, _, _ string) (forge.ParentData, error) {
	return forge.ParentData{PushedAt: time.Now()}, nil
}
func (f *fakeForgeReport) ListForks(_ context.Context, _, _ string) (<-chan forge.ForkMsg, error) {
	out := make(chan forge.ForkMsg, 4)
	go func() {
		defer close(out)
		for _, fk := range f.forks {
			out <- forge.ForkMsg{Fork: fk}
		}
		if f.report != nil {
			out <- forge.ForkMsg{Report: f.report}
		}
	}()
	return out, nil
}
func (f *fakeForgeReport) Branches(_ context.Context, _ forge.T1Data, n int) ([]forge.BranchRef, error) {
	return nil, nil
}
func (f *fakeForgeReport) Compare(_ context.Context, fork forge.T1Data, branch string) (forge.T2Data, error) {
	return forge.T2Data{}, nil
}
func (f *fakeForgeReport) Contributors(_ context.Context, fork forge.T1Data) (forge.T3Data, error) {
	return forge.T3Data{}, nil
}

// TestStreamEmitsAcquisitionReport proves opts.Report is populated from the
// terminal ForkMsg.Report after the channel closes, and the report never
// surfaces as a fork Result.
func TestStreamEmitsAcquisitionReport(t *testing.T) {
	want := &forge.AcquisitionReport{
		Method:        "graphql",
		Scope:         "direct",
		APIVersion:    "2022-11-28",
		AuthMode:      "authenticated",
		FallbackChain: []string{"graphql"},
		Pages:         2,
		RawRows:       50,
		UniqueRows:    50,
		DuplicateRows: 0,
		CaptureAt:     time.Now(),
		AuthScopeID:   "abcd1234abcd1234",
	}
	provider := &fakeForgeReport{
		report: want,
		forks: []forge.T1Data{
			{ID: "owner/repo1", Owner: "owner", Name: "repo1"},
		},
	}
	opts := Options{Report: new(forge.AcquisitionReport)}
	ch, err := Stream(context.Background(), provider, "owner", "repo", opts)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}

	// Drain the channel; ensure no Result ever carries a Report.
	var resultCount int
	for r := range ch {
		resultCount++
		if r.Err != nil {
			t.Fatalf("unexpected error result: %v", r.Err)
		}
	}
	if resultCount != 1 {
		t.Errorf("result count = %d, want 1 (Report must not surface as a Result)", resultCount)
	}

	// Primary assertion: opts.Report is populated after channel close.
	if opts.Report == nil {
		t.Fatal("opts.Report is nil after Stream closed")
	}
	if opts.Report.Method != want.Method {
		t.Errorf("opts.Report.Method = %q, want %q", opts.Report.Method, want.Method)
	}
	if opts.Report.APIVersion != want.APIVersion {
		t.Errorf("opts.Report.APIVersion = %q, want %q", opts.Report.APIVersion, want.APIVersion)
	}
	if opts.Report.AuthScopeID != want.AuthScopeID {
		t.Errorf("opts.Report.AuthScopeID = %q, want %q", opts.Report.AuthScopeID, want.AuthScopeID)
	}
	if !reflect.DeepEqual(opts.Report.FallbackChain, want.FallbackChain) {
		t.Errorf("opts.Report.FallbackChain = %v, want %v", opts.Report.FallbackChain, want.FallbackChain)
	}
}

// TestStreamReportNilOptsNoPanic verifies Stream works when Options.Report is nil
// and the provider still emits a Report (it is silently dropped).
func TestStreamReportNilOptsNoPanic(t *testing.T) {
	rep := &forge.AcquisitionReport{
		Method:        "rest",
		Scope:         "direct",
		APIVersion:    "2022-11-28",
		AuthMode:      "anonymous",
		FallbackChain: []string{"rest"},
		AuthScopeID:   "0000000000000000",
	}
	provider := &fakeForgeReport{
		report: rep,
		forks:  []forge.T1Data{{ID: "owner/repo1", Owner: "owner", Name: "repo1"}},
	}
	ch, err := Stream(context.Background(), provider, "owner", "repo", Options{})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	count := 0
	for r := range ch {
		count++
		if r.Err != nil {
			t.Fatalf("unexpected error: %v", r.Err)
		}
	}
	if count != 1 {
		t.Errorf("result count = %d, want 1", count)
	}
}

// TestStreamNoReport verifies Stream works when the provider sends no report.
func TestStreamNoReport(t *testing.T) {
	provider := &fakeForgeReport{
		forks: []forge.T1Data{{ID: "owner/repo1", Owner: "owner", Name: "repo1"}},
	}
	opts := Options{Report: new(forge.AcquisitionReport)}
	ch, err := Stream(context.Background(), provider, "owner", "repo", opts)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	for r := range ch {
		if r.Err != nil {
			t.Fatalf("unexpected: %v", r.Err)
		}
	}
	if opts.Report == nil {
		t.Fatal("opts.Report should be allocated even when no report is emitted")
	}
	if opts.Report.Method != "" {
		t.Errorf("opts.Report.Method = %q, want empty (no report)", opts.Report.Method)
	}
}

// TestStreamAcquisitionReportJSON verifies the report serialises with stable JSON
// shape (lowercase field tags).
func TestStreamAcquisitionReportJSON(t *testing.T) {
	rep := &forge.AcquisitionReport{
		Method:        "graphql+rest",
		Scope:         "direct",
		APIVersion:    "2022-11-28",
		AuthMode:      "authenticated",
		FallbackChain: []string{"graphql", "rest"},
		Pages:         3,
		RawRows:       120,
		UniqueRows:    110,
		DuplicateRows: 10,
		CaptureAt:     time.Unix(1700000000, 0).UTC(),
		AuthScopeID:   "deadbeefdeadbeef",
	}
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(data)
	for _, must := range []string{
		`"method":"graphql+rest"`,
		`"scope":"direct"`,
		`"apiVersion":"2022-11-28"`,
		`"authMode":"authenticated"`,
		`"fallbackChain":["graphql","rest"]`,
		`"pages":3`,
		`"rawRows":120`,
		`"uniqueRows":110`,
		`"duplicateRows":10`,
		`"authScopeId":"deadbeefdeadbeef"`,
	} {
		if !strings.Contains(s, must) {
			t.Errorf("JSON missing %q in %s", must, s)
		}
	}
	if strings.Contains(s, "AuthScopeID") {
		t.Errorf("JSON contains un-tagged field name; want lowercase tag")
	}
}
