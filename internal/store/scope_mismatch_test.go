package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// TestLoadRepoSnapshot_RejectsScopeMismatch verifies that a snapshot stored with
// one scope is not served to a request with a different scope.
func TestLoadRepoSnapshot_RejectsScopeMismatch(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	repoKey := RepoKey("github", "github.com", "up", "stream")

	// Store with scopeA (apiVersion="github/2022-11-28", authMode="authenticated", scopeID="scopeA").
	storeSnap(t, s, ctx, repoKey, now, "github/2022-11-28", "authenticated", "scopeA")

	// Same scope → hit.
	got, err := s.LoadRepoSnapshotExact(ctx, "github", "github.com", "up", "stream",
		"github/2022-11-28", "authenticated", "scopeA")
	if err != nil {
		t.Fatalf("load scopeA: %v", err)
	}
	if got == nil {
		t.Fatal("expected hit for scopeA")
	}

	// Different apiVersion → miss.
	got, err = s.LoadRepoSnapshotExact(ctx, "github", "github.com", "up", "stream",
		"github/2024-01-01", "authenticated", "scopeA")
	if err != nil {
		t.Fatalf("load different apiVersion: %v", err)
	}
	if got != nil {
		t.Fatal("expected miss for different apiVersion")
	}

	// Different authMode → miss.
	got, err = s.LoadRepoSnapshotExact(ctx, "github", "github.com", "up", "stream",
		"github/2022-11-28", "anonymous", "scopeA")
	if err != nil {
		t.Fatalf("load different authMode: %v", err)
	}
	if got != nil {
		t.Fatal("expected miss for different authMode")
	}

	// Different authScopeID → miss.
	got, err = s.LoadRepoSnapshotExact(ctx, "github", "github.com", "up", "stream",
		"github/2022-11-28", "authenticated", "scopeB")
	if err != nil {
		t.Fatalf("load different authScopeID: %v", err)
	}
	if got != nil {
		t.Fatal("expected miss for different authScopeID")
	}
}

// TestLoadRepoSnapshot_LegacyUnscoped verifies that a pre-schemaV4 row with no
// scope metadata is a cache miss for scoped requests but still readable via the
// unscoped path.
func TestLoadRepoSnapshot_LegacyUnscoped(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	repoKey := RepoKey("github", "github.com", "legacy", "repo")

	// Insert a pre-schemaV4 row: no scope columns populated.
	s.db.ExecContext(ctx, `DELETE FROM repos WHERE repo_key=?`, repoKey)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO repos(repo_key,provider,host,owner,name,first_seen,last_seen) VALUES(?,?,?,?,?,?,?)`,
		repoKey, "github", "github.com", "legacy", "repo", ts(now), ts(now))
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	// Legacy row is a miss for scoped request.
	got, err := s.LoadRepoSnapshotExact(ctx, "github", "github.com", "legacy", "repo",
		"github/2022-11-28", "authenticated", "scopeA")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != nil {
		t.Fatal("expected miss for legacy unscoped row under scoped request")
	}

	// Legacy row is still readable via unscoped LoadRepoSnapshot.
	got, err = s.LoadRepoSnapshot(ctx, "github", "github.com", "legacy", "repo")
	if err != nil {
		t.Fatalf("load unscoped: %v", err)
	}
	if got == nil {
		t.Fatal("expected hit via unscoped LoadRepoSnapshot")
	}
}

// TestPersistSnapshot_RotatesScopeMismatch verifies that upserting a snapshot
// with a mismatched AuthScopeID (e.g. a rotated PAT, or a fresh per-run
// GITHUB_TOKEN in CI) succeeds by adopting the new scope and clearing the
// stale scope's forks, rather than permanently refusing the write. A hard
// refusal here would leave the repo un-refreshable forever once its
// credential set changes -- LoadRepoSnapshotExact already treats a scope
// mismatch as a plain cache miss, so a stale-scope row left in place would
// never be served anyway.
func TestPersistSnapshot_RotatesScopeMismatch(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	repoKey := RepoKey("github", "github.com", "up", "stream")

	// Store with scopeA.
	storeSnap(t, s, ctx, repoKey, now, "github/2022-11-28", "authenticated", "scopeA")

	// Overwrite with scopeB → must succeed and adopt the new scope.
	snapB := Snapshot{
		Repo: RepoRecord{
			Provider:           "github",
			Host:               "github.com",
			Owner:              "up",
			Name:               "stream",
			FirstSeen:          now,
			LastSeen:           now,
			APIVersion:         "github/2022-11-28",
			AcquisitionMethod:   "authenticated",
			AuthScopeID:        "scopeB",
		},
		Fork: ForkRecord{
			ForgeID:  "bob/stream",
			Owner:    "bob",
			Name:     "stream",
			PushedAt: now.Add(-time.Hour),
			UpdatedAt: now,
		},
		T1: &forge.T1Data{
			ID:        "bob/stream",
			Owner:     "bob",
			Name:      "stream",
			PushedAt:  now.Add(-time.Hour),
			CreatedAt: now.Add(-24 * time.Hour),
		},
	}
	if err := s.UpsertSnapshot(ctx, snapB); err != nil {
		t.Fatalf("upsert under rotated scope: %v", err)
	}

	// scopeA is now a miss: its forks were cleared when scopeB adopted the row.
	got, err := s.LoadRepoSnapshotExact(ctx, "github", "github.com", "up", "stream",
		"github/2022-11-28", "authenticated", "scopeA")
	if err != nil {
		t.Fatalf("load scopeA: %v", err)
	}
	if got != nil {
		t.Fatal("expected miss for scopeA after rotation to scopeB")
	}

	// scopeB is a hit, and carries only the fork written under scopeB.
	got, err = s.LoadRepoSnapshotExact(ctx, "github", "github.com", "up", "stream",
		"github/2022-11-28", "authenticated", "scopeB")
	if err != nil {
		t.Fatalf("load scopeB: %v", err)
	}
	if got == nil {
		t.Fatal("expected hit for scopeB")
	}
	for _, f := range got.Forks {
		if f.T1.ID == "alice/stream" {
			t.Errorf("stale scopeA fork %q was not cleared on rotation", f.T1.ID)
		}
	}
}

// TestT2Cache_SameScopeHits verifies that T2 cache uses Exact loading with
// matching scope, so a snapshot stored with scopeA is a hit for scopeA.
func TestT2Cache_SameScopeHits(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	repoKey := RepoKey("github", "github.com", "up", "stream")

	// Store with scopeA.
	storeSnap(t, s, ctx, repoKey, now, "github/2022-11-28", "authenticated", "scopeA")

	// Load with same scope → hit.
	got, err := s.LoadRepoSnapshotExact(ctx, "github", "github.com", "up", "stream",
		"github/2022-11-28", "authenticated", "scopeA")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("expected snapshot under scopeA")
	}
}

// TestCacheScopeKey verifies the key derivation.
func TestCacheScopeKey(t *testing.T) {
	key := CacheScopeKey("github", "github.com", "up", "stream",
		"github/2022-11-28", "authenticated", "scopeA")
	want := "github:github.com:up/stream:v=github/2022-11-28:m=authenticated:s=scopeA"
	if key != want {
		t.Errorf("CacheScopeKey mismatch:\n got: %s\nwant: %s", key, want)
	}
}

// storeSnap is a test helper that writes a minimal snapshot with the given scope fields.
func storeSnap(t *testing.T, s *Store, ctx context.Context, repoKey string, now time.Time, apiVersion, acquisitionMethod, authScopeID string) {
	t.Helper()
	s.db.ExecContext(ctx, `DELETE FROM repos WHERE repo_key=?`, repoKey)
	snap := Snapshot{
		Repo: RepoRecord{
			Provider:           "github",
			Host:               "github.com",
			Owner:              "up",
			Name:               "stream",
			FirstSeen:          now,
			LastSeen:           now,
			APIVersion:         apiVersion,
			AcquisitionMethod:   acquisitionMethod,
			AuthScopeID:        authScopeID,
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
}
