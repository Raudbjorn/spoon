package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
	githubforge "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/store"
)

type failedReplacementForge struct {
	*fakeForge
	auth       forge.AuthInfo
	cachedSeen bool
}

func (f *failedReplacementForge) Auth(context.Context) (forge.AuthInfo, error) {
	return f.auth, nil
}

func (f *failedReplacementForge) BatchCompare(_ context.Context, forks []forge.T1Data, cached map[string]*forge.T2Data, _ float64) (map[string]forge.ForkDivergence, forge.BatchStats, error) {
	result := make(map[string]forge.ForkDivergence)
	for _, fork := range forks {
		f.cachedSeen = cached[fork.ID] != nil
		result[fork.ID] = forge.ForkDivergence{Resolved: true, CountsOnly: true,
			Default: forge.BranchDivergence{Name: "main", TipSHA: "head", AheadBy: 0, BehindBy: 3}}
	}
	return result, forge.BatchStats{}, nil
}

func TestFailedReplacementPersistsInvalidation(t *testing.T) {
	for _, route := range []string{"ndjson", "csv", "topic"} {
		t.Run(route, func(t *testing.T) {
			isolateSpoonRun(t)
			t.Setenv("TURSO_DATABASE_URL", "")
			t.Setenv("TURSO_AUTH_TOKEN", "")
			t.Setenv("SPOON_NO_EMBED", "1")
			t.Setenv("SPOON_NO_VOYAGE", "1")
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Second)
			auth := forge.AuthInfo{Provider: forge.ProviderGitHub, Host: "github.com", Tier: forge.AuthCLI, Concurrency: 2,
				APIVersion: githubforge.StoredAPIVersion, AuthMode: "authenticated", AuthScopeID: "test-scope"}
			fork := forge.T1Data{ID: "alice/fork", Owner: "alice", Name: "fork", DefaultBranch: "main", PushedAt: now, CreatedAt: now.Add(-time.Hour), Description: "current T1 evidence"}
			provider := &failedReplacementForge{auth: auth, fakeForge: &fakeForge{
				parent: forge.ParentData{DefaultBranch: "main"}, forks: []forge.T1Data{fork},
				compareErr: map[string]error{fork.ID: errors.New("replacement compare failed")},
			}}
			db, err := store.OpenDefault()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			seed := forksops.Result{Fork: fork, T2: &forge.T2Data{Performed: true, ComparedBranch: "main", HeadSHA: "head", AheadCount: 2,
				Commits: []forge.AheadCommit{{SHA: "head", Message: "disproven commit evidence"}},
				Diffs:   []forge.FileDiff{{Path: "old.go", Patch: "+disproven diff evidence"}}}}
			if _, err := persistForkSnapshot(ctx, db, auth, "upstream", "repo", "test-model", seed); err != nil {
				t.Fatal(err)
			}
			docs, err := db.PendingDocuments(ctx, "test-model")
			if err != nil || len(docs) != 1 || !strings.Contains(docs[0].Body, "disproven") {
				t.Fatalf("seed documents = %+v, %v", docs, err)
			}
			oldHash := docs[0].ContentHash
			if err := db.UpsertEmbeddings(ctx, []store.EmbeddingRecord{{DocumentID: docs[0].DocumentID, ContentHash: oldHash,
				Model: "test-model", Dim: 1, Vector: []byte{0, 0, 128, 63}, CreatedAt: now}}); err != nil {
				t.Fatal(err)
			}
			previousFactory := providerFactory
			t.Cleanup(func() { providerFactory = previousFactory })
			providerFactory = func(context.Context, string, string, string) (forge.Forge, string, *agentio.Error) {
				return provider, "upstream/repo", nil
			}
			var stdout, stderr bytes.Buffer
			var code int
			if route == "topic" {
				code = streamAndEmit(ctx, db, auth, provider, "upstream", "repo", "upstream/repo", forksops.Options{Tier: 2}, detailOptions{}, "", &stdout, &stderr)
			} else {
				args := []string{"list", "upstream/repo", "--tier", "2", "--no-cluster", "--no-embed"}
				if route == "csv" {
					args = append(args, "--csv")
				}
				code = runForksWith(args, &stdout, &stderr)
			}
			if code != 0 || !provider.cachedSeen || !strings.Contains(stderr.String(), "replacement compare failed") {
				t.Fatalf("replacement path not exercised: code=%d cached=%v stderr=%s", code, provider.cachedSeen, stderr.String())
			}
			lookup := storeCachedT2(ctx, db, auth, "upstream", "repo", false)
			if lookup == nil || lookup(fork) != nil {
				t.Fatal("failed replacement left the disproven comparison reusable")
			}
			docs, err = db.PendingDocuments(ctx, "test-model")
			if err != nil || len(docs) != 1 || docs[0].ContentHash == oldHash || strings.Contains(docs[0].Body, "disproven") || !strings.Contains(docs[0].Body, "current T1 evidence") {
				t.Fatalf("invalidation did not regenerate T1 document with embedding disabled: %+v, %v", docs, err)
			}
			if rows, err := db.SearchRows(ctx, "test-model", "upstream", "repo"); err != nil || len(rows) != 0 {
				t.Fatalf("stale comparison remains searchable: %d rows, %v", len(rows), err)
			}
		})
	}
}

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
	modelID := "test-model"
	persist := func() {
		t.Helper()
		if _, err := persistForkSnapshot(ctx, db, auth, "upstream", "repo", modelID, result); err != nil {
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
	docs, err := db.PendingDocuments(ctx, "test-model")
	if err != nil || len(docs) != 1 || !strings.Contains(docs[0].Body, "keep commit") {
		t.Fatalf("seed comparison document: %+v, %v", docs, err)
	}
	oldHash := docs[0].ContentHash
	modelID = ""
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
	// A last-touch result has synthetic T2 scalars but cannot replace invalidated evidence.
	result.T2, result.T2Invalidated = &forge.T2Data{Performed: true}, got
	result.T2FilesUnfetched, result.T2FromCache = true, false
	result.T2BehindRefreshed = false
	persist()
	if got := load(); got != nil {
		t.Fatalf("invalidated comparison resurrected: %+v", got)
	}
	docs, err = db.PendingDocuments(ctx, "test-model")
	if err != nil || len(docs) != 1 || docs[0].ContentHash == oldHash || strings.Contains(docs[0].Body, "keep commit") {
		t.Fatalf("synthetic T2 retained stale document with indexing disabled: %+v, %v", docs, err)
	}
	result.T2FilesUnfetched = false
	// A successful replacement takes precedence over the old invalidation marker.
	result.T2 = &forge.T2Data{Performed: true, HeadSHA: "new-head", ComparedBranch: "main", BehindCount: 24}
	result.T2FromCache = false
	persist()
	if got := load(); got == nil || got.HeadSHA != "new-head" || got.BehindCount != 24 {
		t.Fatalf("replacement not saved: %+v", got)
	}
}
