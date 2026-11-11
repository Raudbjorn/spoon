package settings

import (
	"errors"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/secrets"
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

type cleanupFailStore struct{ *secrets.MemoryStore }

func (s cleanupFailStore) Remove(string) error { return errors.New("cleanup refused") }

func TestSaveActionReportsCleanupWarning(t *testing.T) {
	t.Setenv(secrets.EnvBackend, "")
	defer config.UseSecretStore(cleanupFailStore{secrets.NewMemoryStore()})()
	path := t.TempDir() + "/config.json"
	cfg := &config.Config{GitHub: config.GitHubConfig{Tokens: []string{"synthetic-token"}}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.GitHub.Tokens = nil
	m := New(cfg, path, nil)
	message, err := runActionWithDeps(ActionSave, &m, ActionDeps{})
	if err != nil || !strings.Contains(message, "saved; warning:") || !strings.Contains(message, "cleanup refused") {
		t.Fatalf("save result = %q, %v", message, err)
	}
}
