package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestResolvePreservesExplicitFalseAsFileSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"github":{"proxy":{"enabled":false}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	row := Resolve(FieldByMust("github.proxy.enabled"), cfg, nil)
	if row.Source != FileSource || row.Value != "false" {
		t.Fatalf("explicit false resolved as %#v", row)
	}
}
