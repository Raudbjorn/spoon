package settings

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestHostRendersRegisteredSchemaVersion(t *testing.T) {
	m := New(&config.Config{Version: config.CurrentVersion}, filepath.Join(t.TempDir(), "config.json"), nil)
	m.section = 7
	if got := m.View(); !strings.Contains(got, "Schema version: 2") {
		t.Fatalf("host did not render registry version: %q", got)
	}
}
