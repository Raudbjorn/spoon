package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeAtomicPublicationUsesDirectoryCapability(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := ProbeAtomicPublication(path); err != nil {
		t.Fatalf("writable directory must permit atomic replacement: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("probe touched target: %v", err)
	}
}
