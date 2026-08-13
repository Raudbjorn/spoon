package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

// Every action is run against an isolated layer. A provider or Voyage HTTP
// transport is deliberately absent: successful completion proves actions stay
// within local resolution/check/publication behavior.
func TestEveryActionCompletesWithoutNetworkTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &config.Config{}
	for _, action := range Actions {
		if _, err := runAction(action.ID, cfg, path, writableCache{}); err != nil {
			t.Fatalf("%s: %v", action.ID, err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("save did not publish isolated config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "README.md")); err != nil {
		t.Fatalf("README action did not run: %v", err)
	}
}
