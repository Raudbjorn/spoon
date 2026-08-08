package store

import (
	"path/filepath"
	"testing"
)

// A host without a resolvable home (system account, container) must still get
// a store location — the store is mandatory — landing in FHS state territory.
func TestDefaultPath_noHomeFallsBackToVarLib(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath must not error without a home: %v", err)
	}
	if want := filepath.Join("/var", "lib", "spoon", "spoon.db"); path != want {
		t.Errorf("DefaultPath = %q, want %q", path, want)
	}
}
