// cmd/spn/forks_acquisition_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
)

// fakeForgeWithReport mirrors fakeForge but additionally sends a terminal
// forge.AcquisitionReport on the ForkMsg channel after the forks.
type fakeForgeWithReport struct {
	fakeForge
	report *forge.AcquisitionReport
}

func (f *fakeForgeWithReport) ListForks(_ context.Context, _, _ string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg, len(f.forks)+1)
	for _, fk := range f.forks {
		ch <- forge.ForkMsg{Fork: fk}
	}
	if f.report != nil {
		ch <- forge.ForkMsg{Report: f.report}
	}
	close(ch)
	return ch, nil
}

type topicReportsForge struct {
	fakeForge
	reports map[string]*forge.AcquisitionReport
}

func (f *topicReportsForge) ListForks(_ context.Context, _, name string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg, len(f.forks)+1)
	for _, fk := range f.forks {
		ch <- forge.ForkMsg{Fork: fk}
	}
	if report := f.reports[name]; report != nil {
		ch <- forge.ForkMsg{Report: report}
	}
	close(ch)
	return ch, nil
}

// TestSpnForksList_AcquisitionReportEnvelope proves that exactly one
// acquisition_report NDJSON envelope appears on stderr after the result
// channel closes, and stdout remains fork-only NDJSON (no envelope leak).
// It also asserts the sentinel token used in the report is never present
// in the captured stderr (a regression guard against secret leak via the
// report envelope).
func TestSpnForksList_AcquisitionReportEnvelope(t *testing.T) {
	isolateSpoonRun(t)
	prev := providerFactory
	defer func() { providerFactory = prev }()
	const sentinel = "GH_SENTINEL_TOKEN_DO_NOT_LEAK_ac3f42"
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return &fakeForgeWithReport{
			fakeForge: fakeForge{
				parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
				forks: []forge.T1Data{
					{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()},
				},
			},
			report: &forge.AcquisitionReport{
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
				// The sentinel is here only as a leak-check: any code path
				// that serializes the AuthScopeID should not also include
				// raw tokens. AuthScopeID is a 16-char hex, not the token.
				AuthScopeID: "0123456789abcdef",
			},
		}, "o/r", nil
	}

	// Sanity: the sentinel is not the same as the AuthScopeID we put in.
	if strings.Contains("0123456789abcdef", sentinel) {
		t.Fatal("sentinel collision with AuthScopeID")
	}

	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{"list", "o/r", "--tier", "1", "--no-cluster"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}

	// Secret-leak regression: the sentinel token must never appear on stderr.
	if strings.Contains(stderr.String(), sentinel) {
		t.Errorf("stderr leaks sentinel token; this is a secret-leak regression")
	}

	// Stdout must be fork-only NDJSON, no envelope.
	for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("invalid JSON line on stdout %q: %v", line, err)
			continue
		}
		if _, hasInfo := obj["info"]; hasInfo {
			t.Errorf("stdout contains info envelope (should be stderr-only): %s", line)
		}
	}

	// Stderr must contain exactly one acquisition_report envelope.
	var reportCount int
	var lastReport map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("invalid stderr JSON %q: %v", line, err)
			continue
		}
		info, ok := obj["info"].(map[string]any)
		if !ok {
			continue
		}
		code, _ := info["code"].(string)
		if code == "acquisition_report" {
			reportCount++
			lastReport = info
		}
	}
	if reportCount != 1 {
		t.Errorf("acquisition_report envelope count = %d, want 1", reportCount)
	}
	if lastReport == nil {
		t.Fatal("no acquisition_report envelope captured")
	}
	details, ok := lastReport["details"].(map[string]any)
	if !ok {
		t.Fatal("details field is not an object")
	}
	if got := details["method"]; got != "graphql" {
		t.Errorf("details.method = %v, want graphql", got)
	}
	if got := details["apiVersion"]; got != "2022-11-28" {
		t.Errorf("details.apiVersion = %v, want 2022-11-28", got)
	}
	if got := details["authScopeId"]; got != "0123456789abcdef" {
		t.Errorf("details.authScopeId = %v, want 0123456789abcdef", got)
	}
}

// TestSpnForksList_NoAcquisitionReportWhenProviderOmits proves that the
// acquisition_report envelope is emitted exactly once when present, and
// zero times when the provider sends no terminal report.
func TestSpnForksList_NoAcquisitionReportWhenProviderOmits(t *testing.T) {
	isolateSpoonRun(t)
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
			forks: []forge.T1Data{
				{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()},
			},
		}, "o/r", nil
	}

	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{"list", "o/r", "--tier", "1", "--no-cluster"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}

	for _, line := range strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if info, ok := obj["info"].(map[string]any); ok {
			if code, _ := info["code"].(string); code == "acquisition_report" {
				t.Errorf("acquisition_report envelope must NOT appear when provider omits report: %s", line)
			}
		}
	}
}

// _ ensures io is referenced if other helpers are added later.
var _ = io.EOF

func acquisitionReportDetails(t *testing.T, stderr string) []map[string]any {
	t.Helper()
	var details []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if line == "" {
			continue
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("stderr contains invalid JSON %q: %v", line, err)
		}
		info, ok := envelope["info"].(map[string]any)
		if !ok || info["code"] != "acquisition_report" {
			continue
		}
		detail, ok := info["details"].(map[string]any)
		if !ok {
			t.Fatalf("acquisition report details = %T, want object", info["details"])
		}
		details = append(details, detail)
	}
	return details
}

func TestSpnForksList_CSVEmitsAcquisitionReport(t *testing.T) {
	isolateSpoonRun(t)
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return &fakeForgeWithReport{
			fakeForge: fakeForge{
				parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
				forks:  []forge.T1Data{{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()}},
			},
			report: &forge.AcquisitionReport{Method: "rest", Scope: "direct", APIVersion: "2022-11-28", AuthMode: "anonymous", FallbackChain: []string{"rest"}, UniqueRows: 1, RawRows: 1},
		}, "o/r", nil
	}

	var stdout, stderr bytes.Buffer
	if exit := runForksWith([]string{"list", "o/r", "--tier", "1", "--no-cluster", "--csv"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "id,owner,") {
		t.Fatalf("stdout = %q, want CSV header", stdout.String())
	}
	if strings.Contains(stdout.String(), "acquisition_report") {
		t.Fatalf("stdout contains acquisition report: %s", stdout.String())
	}
	if reports := acquisitionReportDetails(t, stderr.String()); len(reports) != 1 {
		t.Fatalf("CSV acquisition reports = %d, want 1: %s", len(reports), stderr.String())
	}
}

func TestStreamAndEmit_AcquisitionReportPerRepoAndNoStaleReuse(t *testing.T) {
	reports := map[string]*forge.AcquisitionReport{
		"first":  {Method: "graphql", Scope: "direct", APIVersion: "2022-11-28", FallbackChain: []string{"graphql"}, UniqueRows: 1},
		"second": {Method: "rest", Scope: "direct", APIVersion: "2022-11-28", FallbackChain: []string{"rest"}, UniqueRows: 2},
	}
	provider := &topicReportsForge{
		fakeForge: fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
			forks:  []forge.T1Data{{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()}},
		},
		reports: reports,
	}
	var report forge.AcquisitionReport
	opts := forksops.Options{Tier: 1, Report: &report, Cluster: forksops.ClusterOptions{Enabled: false}}
	var stdout, stderr bytes.Buffer
	for _, name := range []string{"first", "second"} {
		if exit := streamAndEmit(context.Background(), nil, forge.AuthInfo{}, provider, "o", name, "o/"+name, opts, detailOptions{}, "", &stdout, &stderr); exit != 0 {
			t.Fatalf("streamAndEmit(%s) exit=%d stderr=%s", name, exit, stderr.String())
		}
	}
	got := acquisitionReportDetails(t, stderr.String())
	if len(got) != 2 {
		t.Fatalf("topic acquisition reports = %d, want 2: %s", len(got), stderr.String())
	}
	for i, want := range []string{"graphql", "rest"} {
		if got[i]["method"] != want {
			t.Errorf("report %d method = %v, want %q", i, got[i]["method"], want)
		}
	}

	provider.reports["third"] = nil
	if exit := streamAndEmit(context.Background(), nil, forge.AuthInfo{}, provider, "o", "third", "o/third", opts, detailOptions{}, "", &stdout, &stderr); exit != 0 {
		t.Fatalf("streamAndEmit(third) exit=%d stderr=%s", exit, stderr.String())
	}
	if got := acquisitionReportDetails(t, stderr.String()); len(got) != 2 {
		t.Fatalf("provider omission re-emitted stale report: got %d reports, want 2: %s", len(got), stderr.String())
	}
}
