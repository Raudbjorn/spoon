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

	skip := &cluster.SkipReason{Code: "ollama_unreachable", Message: "ollama down"}
	mAfter, _ := m.handleClusterResult(clusterResultMsg{Skip: skip})
	mm, ok := mAfter.(*Model)
	if !ok {
		t.Fatalf("expected Model, got %T", mAfter)
	}
	if mm.clusterStatus != "skipped" {
		t.Fatalf("clusterStatus = %q; want %q", mm.clusterStatus, "skipped")
	}
	if mm.clusterSkipReason != "ollama down" {
		t.Fatalf("clusterSkipReason = %q; want %q", mm.clusterSkipReason, "ollama down")
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

func TestHandleClusterPrompt_TransitionsView(t *testing.T) {
	m := newTestModel()
	reply := make(chan bool, 1)
	prompt := clusterPromptMsg{Model: "nomic-embed-text", SizeMB: 274, Reply: reply}

	mAfter, _ := m.handleClusterPrompt(prompt)
	mm := mAfter.(*Model)
	if mm.view != viewEmbedderBootstrap {
		t.Fatalf("view = %v; want viewEmbedderBootstrap", mm.view)
	}
	if mm.clusterPendingPrompt == nil {
		t.Fatalf("clusterPendingPrompt should be set")
	}
	if mm.clusterPendingPrompt.Model != "nomic-embed-text" {
		t.Fatalf("prompt model = %q", mm.clusterPendingPrompt.Model)
	}
}

func TestHandleEmbedderBootstrapKey_Yes(t *testing.T) {
	m := newTestModel()
	reply := make(chan bool, 1)
	m.clusterPendingPrompt = &clusterPromptMsg{Model: "x", SizeMB: 1, Reply: reply}
	m.view = viewEmbedderBootstrap

	_, cmd := m.handleEmbedderBootstrapKey("y")
	if cmd == nil {
		t.Fatal("expected a Cmd")
	}
	msg := cmd()
	resp, ok := msg.(clusterPromptResponseMsg)
	if !ok {
		t.Fatalf("expected clusterPromptResponseMsg, got %T", msg)
	}
	if !resp.Yes {
		t.Fatal("expected Yes=true")
	}
	if resp.Reply != reply {
		t.Fatal("reply channel must be passed through")
	}
}

func TestHandleEmbedderBootstrapKey_No(t *testing.T) {
	m := newTestModel()
	reply := make(chan bool, 1)
	m.clusterPendingPrompt = &clusterPromptMsg{Model: "x", SizeMB: 1, Reply: reply}
	m.view = viewEmbedderBootstrap

	_, cmd := m.handleEmbedderBootstrapKey("n")
	if cmd == nil {
		t.Fatal("expected a Cmd")
	}
	resp := cmd().(clusterPromptResponseMsg)
	if resp.Yes {
		t.Fatal("expected Yes=false")
	}
}

func TestHandleEmbedderBootstrapKey_NoPendingPrompt(t *testing.T) {
	m := newTestModel()
	m.view = viewEmbedderBootstrap

	mAfter, cmd := m.handleEmbedderBootstrapKey("y")
	if cmd != nil {
		t.Fatal("no pending prompt → no Cmd")
	}
	if mAfter.(*Model).view != viewTable {
		t.Fatal("should fall back to viewTable when prompt is absent")
	}
}

func TestClusterPromptResponse_ReplyAndClear(t *testing.T) {
	m := newTestModel()
	reply := make(chan bool, 1)
	m.clusterPendingPrompt = &clusterPromptMsg{Model: "x", SizeMB: 1, Reply: reply}
	m.view = viewEmbedderBootstrap

	// Simulate the full Update path for clusterPromptResponseMsg.
	// Update has a value receiver, so it returns a Model value.
	mAfter, _ := m.Update(clusterPromptResponseMsg{Reply: reply, Yes: true})
	mm := mAfter.(Model)
	if mm.clusterPendingPrompt != nil {
		t.Fatal("pending prompt should be cleared")
	}
	if mm.view != viewTable {
		t.Fatalf("view = %v; want viewTable", mm.view)
	}
	select {
	case got := <-reply:
		if !got {
			t.Fatal("answer routed to reply channel was wrong")
		}
	default:
		t.Fatal("reply channel never received")
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
