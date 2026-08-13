package settings

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestEnvironmentRowsUseStartupSnapshot(t *testing.T) {
	t.Setenv("SPOON_FASTEMBED_MODEL", "ambient-model")
	m := New(&config.Config{}, filepath.Join(t.TempDir(), "config.json"), nil).WithEnvironment(map[string]string{
		"SPOON_FASTEMBED_MODEL": "startup-model",
	})
	for _, row := range m.envRows() {
		if strings.HasPrefix(row, "SPOON_FASTEMBED_MODEL =") && row != "SPOON_FASTEMBED_MODEL = startup-model" {
			t.Fatalf("environment row = %q", row)
		}
	}
}
