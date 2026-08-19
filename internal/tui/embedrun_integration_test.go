package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/tui/settings"
)

// TestEmbedRunIntegration exercises the whole chain against a real ONNX Runtime
// and a real store: fork list -> documents -> vectors -> coverage -> column.
//
// Every unit test above stubs one of those seams, and the interesting failures
// live between them. Two in particular are invisible to a stubbed test: whether
// the TUI writes documents at all (it did not, before), and whether the document
// IDs it writes actually join to the fork rows written in the same transaction.
// A coverage query that silently returns nothing for every fork looks exactly
// like "nothing has been embedded yet".
func TestEmbedRunIntegration(t *testing.T) {
	if os.Getenv("ONNX_PATH") == "" {
		t.Skip("ONNX_PATH not set")
	}

	db, raw := documentStore(t)
	m := refreshModel(t)
	m.db = db

	env := map[string]string{embed.ONNXPathEnv: os.Getenv("ONNX_PATH")}
	cfg := &config.Config{}
	m.settings = settings.New(cfg, t.TempDir()+"/config.json", nil).
		WithEnvironment(env).
		WithEffective(config.ResolveEffectiveConfig(cfg, nil, env))
	m.refreshEmbedStatus()
	m.resolveEmbedModels()

	if reason, ok := m.fastEmbedRunnable(); !ok {
		t.Skipf("fastembed unavailable on this host: %s", reason)
	}

	// Voyage is excluded deliberately: this test must never make a billed call.
	cmd := m.startEmbedRunWith(true, true, false)
	if cmd == nil {
		t.Fatal("no embedding run was started")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("run command = %T, want a batch", cmd())
	}
	var done embedRunDoneMsg
	var started bool
	for _, sub := range batch {
		switch msg := sub().(type) {
		case embedRunDoneMsg:
			done = msg
		case embedRunStartedMsg:
			started = true
		}
	}
	if !started {
		t.Error("no start message, so the status bar would never say a run was under way")
	}
	if done.err != nil {
		t.Fatalf("run failed: %v (warnings %v)", done.err, done.warnings)
	}
	if len(done.warnings) > 0 {
		t.Errorf("unexpected warnings: %v", done.warnings)
	}
	if done.indexed != len(m.forks) {
		t.Fatalf("indexed %d documents for %d forks", done.indexed, len(m.forks))
	}

	// Vectors must land under the exact identity the coverage query asks for.
	// Under any other, the column reads empty forever and every run redoes the
	// same work.
	var model string
	var rows, dim int
	if err := raw.QueryRowContext(context.Background(),
		"SELECT model, COUNT(*), MIN(dim) FROM embeddings GROUP BY model").Scan(&model, &rows, &dim); err != nil {
		t.Fatal(err)
	}
	if model != embed.FastEmbedModelID {
		t.Errorf("vectors stored under %q, want %q", model, embed.FastEmbedModelID)
	}
	if dim != embed.FastEmbedDimension {
		t.Errorf("stored dimension %d, want %d", dim, embed.FastEmbedDimension)
	}

	repoKey, ok := m.repoKey()
	if !ok {
		t.Fatal("no repo key")
	}
	coverage, err := db.EmbeddingCoverage(context.Background(), repoKey, []string{m.embedModels.fastEmbed})
	if err != nil {
		t.Fatal(err)
	}
	m.embedCoverage = coverage
	if len(coverage) != len(m.forks) {
		t.Fatalf("coverage covers %d forks, want %d — documents and forks are not joining", len(coverage), len(m.forks))
	}
	for i := range m.forks {
		if cell := m.embedCellFor(m.forks[i]); !strings.HasPrefix(cell, "+") && !strings.HasPrefix(cell, "✓") {
			t.Errorf("%s renders %q after a successful run", m.forks[i].Fork.ID, cell)
		}
	}

	// A second pass must find nothing to do: the content hashes still match, so
	// re-running is free rather than re-embedding everything.
	m.embedRunning = false
	second := m.startEmbedRunWith(true, true, false)
	if second == nil {
		t.Fatal("second run was not started")
	}
	secondBatch, ok := second().(tea.BatchMsg)
	if !ok {
		t.Fatalf("second run command = %T, want a batch", second())
	}
	doneCount := 0
	for _, sub := range secondBatch {
		msg, isDone := sub().(embedRunDoneMsg)
		if !isDone {
			continue
		}
		doneCount++
		if msg.err != nil {
			t.Fatalf("second run failed: %v", msg.err)
		}
		if len(msg.warnings) != 0 {
			t.Fatalf("second run warned: %v", msg.warnings)
		}
		if msg.indexed != 0 {
			t.Errorf("re-running embedded %d documents again; the content-hash join is not holding", msg.indexed)
		}
	}
	if doneCount != 1 {
		t.Fatalf("second run produced %d completion messages, want 1", doneCount)
	}
}
