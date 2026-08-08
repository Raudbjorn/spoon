package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// OpenDefault must move a pre-relocation database (XDG_DATA_HOME) to the new
// config-dir location once, keeping its data readable there.
func TestOpenDefaultMigratesLegacyDatabase(t *testing.T) {
	dataDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	// Seed a database at the legacy location.
	legacy := filepath.Join(dataDir, "spoon", "spoon.db")
	s, err := Open(legacy)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	now := time.Now().UTC()
	if err := s.UpsertSnapshot(context.Background(), Snapshot{
		Repo: RepoRecord{Provider: "github", Host: "github.com", Owner: "o", Name: "r", FirstSeen: now, LastSeen: now},
		Fork: ForkRecord{ForgeID: "f/x", Owner: "f", Name: "x", PushedAt: now, UpdatedAt: now},
	}); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	sd, err := OpenDefault()
	if err != nil {
		t.Fatalf("open default: %v", err)
	}
	defer sd.Close()

	newPath := filepath.Join(configDir, "spoon", "spoon.db")
	if sd.path != newPath {
		t.Fatalf("store path = %q, want %q", sd.path, newPath)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("migrated database missing: %v", err)
	}
	var n int
	if err := sd.db.QueryRowContext(context.Background(), "SELECT count(*) FROM forks").Scan(&n); err != nil {
		t.Fatalf("query migrated data: %v", err)
	}
	if n != 1 {
		t.Fatalf("forks after migration = %d, want 1", n)
	}

	// Second open must not re-copy (the legacy file stays for downgrades but is
	// no longer consulted once the new path exists).
	if err := sd.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if s2, err := OpenDefault(); err != nil {
		t.Fatalf("re-open default: %v", err)
	} else {
		s2.Close()
	}
}
