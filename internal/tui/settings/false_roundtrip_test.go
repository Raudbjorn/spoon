package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestUnrelatedSaveRetainsExplicitFalseFileSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"github":{"proxy":{"enabled":false}},"forge":{"provider":"github"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := Candidate(FieldByMust("forge.provider"), cfg, "gitlab")
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, candidate); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	row := Resolve(FieldByMust("github.proxy.enabled"), reloaded, nil)
	if row.Source != FileSource || row.Value != "false" {
		t.Fatalf("false after unrelated save = %#v", row)
	}
}
