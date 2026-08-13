package settings

import (
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestActionRegistryIsCompleteAndNonblocking(t *testing.T) {
	want := []ActionID{ActionProviderProbe, ActionStoreCheck, ActionFastEmbedCheck, ActionVoyageStatus, ActionRewriteReadme, ActionCopyConfigPath, ActionSave}
	for _, id := range want {
		if _, ok := ActionByID(id); !ok {
			t.Fatalf("missing action %q", id)
		}
	}
	m := New(&config.Config{}, t.TempDir()+"/config.json", nil)
	m.startAction(ActionVoyageStatus)
	if !m.busy {
		t.Fatal("action must set busy before command completion")
	}
	updated, _ := m.Update(actionMsg{id: ActionVoyageStatus, text: "Voyage not configured"})
	if updated.(Model).busy {
		t.Fatal("completed action left model busy")
	}
}
