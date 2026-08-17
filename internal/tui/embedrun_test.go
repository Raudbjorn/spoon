package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
)

// TestEmbedRunNeverDownloadsOnAKeypress is a guard against a very bad first
// impression. embed.NewFastEmbedEmbedder provisions the model unconditionally,
// downloading roughly a hundred megabytes under a fifteen-minute timeout, so a
// user on a fresh machine pressing the embed key would get a UI that says
// "embedding" for minutes with no indication that a download is happening.
//
// The probe already knows whether the model is on disk. It has to be a
// precondition, not merely a thing shown in the status bar.
func TestEmbedRunNeverDownloadsOnAKeypress(t *testing.T) {
	m := refreshModel(t)
	m.embedModels = embedModels{fastEmbed: embed.FastEmbedModelID}
	m.embedProbe = embedStatus{
		resolved:  true,
		fastEmbed: embed.FastEmbedStatus{Model: "fast-bge-small-en-v1.5", ModelPresent: false, RuntimeFound: true},
	}

	reason, ok := m.fastEmbedRunnable()
	if ok {
		t.Fatal("a run was allowed with no model on disk; it would download one mid-keypress")
	}
	if !strings.Contains(reason, "spoon setup") {
		t.Errorf("refusal %q does not tell the user how to fix it", reason)
	}
}

// TestEmbedRunRefusesWithoutARuntime keeps the two prerequisites distinct at the
// point of action, not just in the status line.
func TestEmbedRunRefusesWithoutARuntime(t *testing.T) {
	m := refreshModel(t)
	m.embedModels = embedModels{fastEmbed: embed.FastEmbedModelID}
	m.embedProbe = embedStatus{
		resolved:  true,
		fastEmbed: embed.FastEmbedStatus{Model: "fast-bge-small-en-v1.5", ModelPresent: true, RuntimeFound: false},
	}

	reason, ok := m.fastEmbedRunnable()
	if ok {
		t.Fatal("a run was allowed with no ONNX Runtime")
	}
	if !strings.Contains(reason, embed.ONNXPathEnv) {
		t.Errorf("refusal %q does not name ONNX_PATH", reason)
	}
}

// TestEmbedMarkedWithNothingMarkedIsANoOp: an empty selection must not quietly
// become "all forks". Under Voyage that is the difference between zero spend
// and a full billed pass.
func TestEmbedMarkedWithNothingMarkedIsANoOp(t *testing.T) {
	m := refreshModel(t)
	m.embedModels = embedModels{fastEmbed: embed.FastEmbedModelID}
	m.embedProbe = embedStatus{
		resolved:  true,
		fastEmbed: embed.FastEmbedStatus{ModelPresent: true, RuntimeFound: true},
	}
	for i := range m.forks {
		m.forks[i].Marked = false
	}

	_, cmd := m.handleTableKey("i")
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, running := msg.(embedRunStartedMsg); running {
				t.Fatal("embedding started with nothing marked")
			}
		}
	}
	if !strings.Contains(m.errMsg, "marked") {
		t.Errorf("no explanation given for the no-op: %q", m.errMsg)
	}
}

// TestEmbedTargetsSelectsTheRightForks pins what each key means.
func TestEmbedTargetsSelectsTheRightForks(t *testing.T) {
	m := refreshModel(t)
	m.forks[0].Marked = true
	m.forks[2].Marked = true

	marked := m.embedTargets(false)
	if len(marked) != 2 {
		t.Fatalf("marked targets = %d, want 2", len(marked))
	}
	all := m.embedTargets(true)
	if len(all) != len(m.forks) {
		t.Fatalf("all targets = %d, want %d", len(all), len(m.forks))
	}
}

// TestEmbedRunKeysAreBoundAndDiscoverable: an action nobody can reach is not a
// feature, and one nobody can find is barely one.
//
// The second half matters because Bug 2's fix trimmed the table footer to a
// curated list of bindings. A new key added to the registry but left out of that
// list works and is invisible, discoverable only by opening `?`.
func TestEmbedRunKeysAreBoundAndDiscoverable(t *testing.T) {
	for _, key := range []string{"i", "I"} {
		if action := keymap.Dispatch(keymap.MainTable, key); action != keymap.EmbedMarked && action != keymap.EmbedAll {
			t.Errorf("key %q dispatches to %q, not an embedding run", key, action)
		}
	}

	m := frameModel(t, 100, 30, 4)
	m.embedModels = embedModels{fastEmbed: embed.FastEmbedModelID}
	if !strings.Contains(m.viewTable(), "Embed marked forks") {
		t.Errorf("the table footer does not mention embedding, so the keys are undiscoverable:\n%s", m.viewTable())
	}
}

// TestEmbedRunDoublePressIsRefused pins the concurrency guard. The pre-flight
// mutation in startEmbedRunWith is what makes this work: handleTableKey has a
// pointer receiver, so the flag lands before the next key is read rather than
// waiting for embedRunStartedMsg to round-trip. Under Voyage a second run is a
// second bill.
func TestEmbedRunDoublePressIsRefused(t *testing.T) {
	db, _ := documentStore(t)
	m := refreshModel(t)
	m.db = db
	m.embedModels = embedModels{fastEmbed: embed.FastEmbedModelID}
	m.embedProbe = embedStatus{resolved: true}

	if _, cmd := m.handleTableKey("I"); cmd == nil {
		t.Fatal("the first press started nothing")
	}
	_, second := m.handleTableKey("I")
	if second != nil {
		t.Fatal("a second press in the same frame started another run")
	}
	if !strings.Contains(m.errMsg, "already in progress") {
		t.Errorf("the refusal was not explained: %q", m.errMsg)
	}
}

var _ tea.Cmd = nil
