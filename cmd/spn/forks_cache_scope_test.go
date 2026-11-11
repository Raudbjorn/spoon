package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
	githubforge "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/store"
)

// TestPersistForkSnapshot_ExactCacheHit uses the production persistence input
// (forge.AuthInfo) to prove that the stored API-version representation is the
// same representation passed back to the exact T2-cache read. The GitHub HTTP
// header keeps using defaultRESTVersion; this test covers only cache storage.
func TestPersistForkSnapshot_ExactCacheHit(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	auth := forge.AuthInfo{
		Provider:    forge.ProviderGitHub,
		Host:        "github.com",
		APIVersion:  githubforge.StoredAPIVersion,
		AuthMode:    "authenticated",
		AuthScopeID: "scope-a",
	}
	result := forksops.Result{Fork: forge.T1Data{
		ID:        "alice/fork",
		Owner:     "alice",
		Name:      "fork",
		CreatedAt: time.Now().UTC(),
	}}
	if _, err := persistForkSnapshot(context.Background(), db, auth, "upstream", "repo", "", result); err != nil {
		t.Fatalf("persist production snapshot: %v", err)
	}

	snapshot, err := db.LoadRepoSnapshotExact(context.Background(), "github", "github.com", "upstream", "repo",
		auth.APIVersion, auth.AuthMode, auth.AuthScopeID)
	if err != nil {
		t.Fatalf("exact read: %v", err)
	}
	if snapshot == nil {
		t.Fatal("same-scope production snapshot was a cache miss")
	}
	if snapshot.APIVersion != githubforge.StoredAPIVersion {
		t.Errorf("stored API version = %q, want %q", snapshot.APIVersion, githubforge.StoredAPIVersion)
	}
}

func TestPersistForkSnapshot_CachedRefreshAndInvalidation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	auth := forge.AuthInfo{Provider: forge.ProviderGitHub, Host: "github.com"}
	now := time.Now().UTC().Truncate(time.Second)
	result := forksops.Result{
		Fork: forge.T1Data{ID: "alice/fork", Owner: "alice", Name: "fork", DefaultBranch: "main", PushedAt: now},
		T2: &forge.T2Data{Performed: true, HeadSHA: "head", ComparedBranch: "main", AheadCount: 1, BehindCount: 2,
			Diffs:   []forge.FileDiff{{Path: "a.go", Patch: "keep patch"}},
			Commits: []forge.AheadCommit{{SHA: "head", Message: "keep commit", Timestamp: now}}},
	}
	persist := func() {
		t.Helper()
		if _, err := persistForkSnapshot(ctx, db, auth, "upstream", "repo", "", result); err != nil {
			t.Fatal(err)
		}
	}
	load := func() *forge.T2Data {
		t.Helper()
		lookup := storeCachedT2(ctx, db, auth, "upstream", "repo", false)
		if lookup == nil {
			t.Fatal("snapshot disappeared")
		}
		return lookup(result.Fork)
	}
	persist()
	result.T2 = load()
	result.T2.BehindCount = 23
	// Even a partial cached value must leave the stored relational rows alone.
	result.T2.Diffs, result.T2.Commits = nil, nil
	result.T2FromCache, result.T2BehindRefreshed = true, true
	persist()
	got := load()
	if got == nil || got.BehindCount != 23 || len(got.Diffs) != 1 || len(got.Commits) != 1 || got.Commits[0].Message != "keep commit" {
		t.Fatalf("cached refresh round trip: %+v", got)
	}
	result.T2, result.T2Invalidated = nil, got
	result.T2BehindRefreshed = false
	persist()
	if got := load(); got != nil {
		t.Fatalf("invalidated comparison resurrected: %+v", got)
	}
	// A successful replacement takes precedence over the old invalidation marker.
	result.T2 = &forge.T2Data{Performed: true, HeadSHA: "new-head", ComparedBranch: "main", BehindCount: 24}
	result.T2FromCache = false
	persist()
	if got := load(); got == nil || got.HeadSHA != "new-head" || got.BehindCount != 24 {
		t.Fatalf("replacement not saved: %+v", got)
	}
}
