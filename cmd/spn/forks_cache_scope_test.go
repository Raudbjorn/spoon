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
