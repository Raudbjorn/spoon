package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestScopeTrace(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Wipe any prior row to get a clean slate.
	repoKey := RepoKey("github", "github.com", "up", "stream")
	s.db.ExecContext(ctx, `DELETE FROM repos WHERE repo_key=?`, repoKey)

	snap := Snapshot{
		Repo: RepoRecord{
			Provider:           "github",
			Host:               "github.com",
			Owner:              "up",
			Name:               "stream",
			FirstSeen:          now,
			LastSeen:           now,
			APIVersion:         "github/2022-11-28",
			AcquisitionMethod:   "graphql",
			AuthScopeID:        "scopeA",
		},
		Fork: ForkRecord{
			ForgeID:  "alice/stream",
			Owner:    "alice",
			Name:     "stream",
			PushedAt: now.Add(-time.Hour),
			UpdatedAt: now,
		},
		T1: &forge.T1Data{
			ID:        "alice/stream",
			Owner:     "alice",
			Name:      "stream",
			PushedAt:  now.Add(-time.Hour),
			CreatedAt: now.Add(-24 * time.Hour),
		},
	}
	if err := s.UpsertSnapshot(ctx, snap); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Check schema: what columns exist in repos?
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(repos)`)
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	var cols []string
	for rows.Next() {
		var id int; var name, ctype string; var notnull, pk int; var dflt interface{}
		rows.Scan(&id, &name, &ctype, &notnull, &dflt, &pk)
		cols = append(cols, name)
	}
	rows.Close()
	t.Logf("repos columns: %v", cols)

	// Check schema version.
	var ver int
	s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&ver)
	t.Logf("schema version: %d", ver)

	// Confirm row count after upsert.
	var count int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos WHERE repo_key=?`, repoKey).Scan(&count)
	t.Logf("upserted: row_count=%d", count)

	// Read raw values from DB with explicit column names.
	var storedAPI, storedMethod, storedScopeID string
	err = s.db.QueryRowContext(ctx,
		`SELECT api_version, acquisition_method, auth_scope_id FROM repos WHERE repo_key=?`, repoKey).
		Scan(&storedAPI, &storedMethod, &storedScopeID)
	if err != nil {
		t.Fatalf("raw SELECT: %v", err)
	}
	t.Logf("stored raw: api=%q method=%q scope=%q", storedAPI, storedMethod, storedScopeID)

	// Also read via LoadRepoSnapshot (unscoped) to see if it gets the values.
	gotUnscoped, err := s.LoadRepoSnapshot(ctx, "github", "github.com", "up", "stream")
	if err != nil {
		t.Fatalf("LoadRepoSnapshot (unscoped): %v", err)
	}
	if gotUnscoped != nil {
		t.Logf("unscoped load: api=%q method=%q scope=%q",
			gotUnscoped.APIVersion, gotUnscoped.AcquisitionMethod, gotUnscoped.AuthScopeID)
	} else {
		t.Log("unscoped load: got nil")
	}

	// Load via Exact with matching scope.
	got, err := s.LoadRepoSnapshotExact(ctx, "github", "github.com", "up", "stream",
		"github/2022-11-28", "graphql", "scopeA")
	if err != nil {
		t.Fatalf("LoadRepoSnapshotExact: %v", err)
	}
	if got == nil {
		t.Fatal("LoadRepoSnapshotExact returned nil")
	}
	t.Logf("loaded: api=%q method=%q scope=%q", got.APIVersion, got.AcquisitionMethod, got.AuthScopeID)
}
