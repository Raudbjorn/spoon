package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/cluster"
)

// newTestModel constructs a Model wired up just enough for unit-testing
// cluster bridge handlers (no provider, no real channels). Each test
// composes additional state as needed.
func newTestModel() *Model {
	m := Model{
		view:        viewTable,
		clusterMsgs: make(chan tea.Msg, 8),
	}
	return &m
}

func TestHandleClusterResult_Skip(t *testing.T) {
	m := newTestModel()
	m.clusterOpts.Enabled = true

	skip := &cluster.SkipReason{Code: "embedder_failed", Message: "embedder failed"}
	mAfter, _ := m.handleClusterResult(clusterResultMsg{Skip: skip})
	mm, ok := mAfter.(*Model)
	if !ok {
		t.Fatalf("expected Model, got %T", mAfter)
	}
	if mm.clusterStatus != "skipped" {
		t.Fatalf("clusterStatus = %q; want %q", mm.clusterStatus, "skipped")
	}
	if mm.clusterSkipReason != "embedder failed" {
		t.Fatalf("clusterSkipReason = %q; want %q", mm.clusterSkipReason, "embedder failed")
	}
}

func TestHandleClusterResult_SilentDisabled(t *testing.T) {
	m := newTestModel()
	m.clusterOpts.Enabled = true

	skip := &cluster.SkipReason{Code: "disabled", Message: "clustering disabled"}
	mAfter, _ := m.handleClusterResult(clusterResultMsg{Skip: skip})
	mm := mAfter.(*Model)
	if mm.clusterStatus != "skipped" {
		t.Fatalf("clusterStatus = %q; want %q", mm.clusterStatus, "skipped")
	}
	if mm.clusterSkipReason != "" {
		t.Fatalf("clusterSkipReason should be empty for disabled, got %q", mm.clusterSkipReason)
	}
}

func TestHandleClusterResult_Done(t *testing.T) {
	m := newTestModel()
	m.clusterOpts.Enabled = true

	mAfter, _ := m.handleClusterResult(clusterResultMsg{})
	mm := mAfter.(*Model)
	if mm.clusterStatus != "done" {
		t.Fatalf("clusterStatus = %q; want done", mm.clusterStatus)
	}
}

func TestMaybeStartClusterPipeline_DisabledReturnsNil(t *testing.T) {
	m := newTestModel()
	m.clusterOpts.Enabled = false
	if cmd := m.maybeStartClusterPipeline(); cmd != nil {
		t.Fatal("disabled → maybeStartClusterPipeline should return nil")
	}
}

func TestMaybeStartClusterPipeline_AlreadyRanReturnsNil(t *testing.T) {
	m := newTestModel()
	m.clusterOpts.Enabled = true
	m.clusterRan = true
	if cmd := m.maybeStartClusterPipeline(); cmd != nil {
		t.Fatal("clusterRan → maybeStartClusterPipeline should return nil")
	}
}

func TestClusterFooter(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		status     string
		reason     string
		wantPrefix string
	}{
		{"disabled", false, "", "", ""},
		{"running", true, "running", "", "clusters: computing..."},
		{"skipped with reason", true, "skipped", "no model", "clusters skipped: no model"},
		{"skipped silent", true, "skipped", "", ""},
		{"error", true, "error", "embedder down", "clusters error: embedder down"},
		{"done", true, "done", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			m.clusterOpts.Enabled = tt.enabled
			m.clusterStatus = tt.status
			m.clusterSkipReason = tt.reason
			got := m.clusterFooter()
			if tt.wantPrefix == "" {
				if got != "" {
					t.Fatalf("got %q; want empty", got)
				}
				return
			}
			if got != tt.wantPrefix {
				t.Fatalf("got %q; want %q", got, tt.wantPrefix)
			}
		})
	}
}

func TestSplitParentName(t *testing.T) {
	tests := []struct {
		in, wantOwner, wantRepo string
	}{
		{"golang/go", "golang", "go"},
		{"", "", ""},
		{"nopath", "", ""},
		{"trailing/", "", ""},
		{"/leading", "", ""},
		{"a/b/c", "a", "b/c"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			o, r := splitParentName(tt.in)
			if o != tt.wantOwner || r != tt.wantRepo {
				t.Fatalf("splitParentName(%q) = (%q, %q); want (%q, %q)",
					tt.in, o, r, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

func TestSettingsViewStillProcessesClusterResults(t *testing.T) {
	m := newTestModel()
	m.view = viewSettings

	updated, cmd := m.Update(clusterResultMsg{})
	var got Model
	switch value := updated.(type) {
	case Model:
		got = value
	case *Model:
		got = *value
	default:
		t.Fatalf("Update returned %T", updated)
	}
	if got.clusterStatus != "done" {
		t.Fatalf("clusterStatus = %q; want done while Settings is open", got.clusterStatus)
	}
	if cmd == nil {
		t.Fatal("cluster message pump was not re-armed while Settings is open")
	}
}
