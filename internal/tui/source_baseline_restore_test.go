package tui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/store"
)

type restoreFakeForge struct {
	owner, repo, branch string
}

func (f *restoreFakeForge) Auth(context.Context) (forge.AuthInfo, error) {
	return forge.AuthInfo{}, nil
}
func (f *restoreFakeForge) Parent(context.Context, string, string) (forge.ParentData, error) {
	return forge.ParentData{}, nil
}
func (f *restoreFakeForge) ListForks(context.Context, string, string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg)
	close(ch)
	return ch, nil
}
func (f *restoreFakeForge) Branches(context.Context, forge.T1Data, int) ([]forge.BranchRef, error) {
	return nil, nil
}
func (f *restoreFakeForge) Compare(context.Context, forge.T1Data, string) (forge.T2Data, error) {
	return forge.T2Data{}, nil
}
func (f *restoreFakeForge) Contributors(context.Context, forge.T1Data) (forge.T3Data, error) {
	return forge.T3Data{}, nil
}
func (f *restoreFakeForge) Headroom() float64 { return 1 }
func (f *restoreFakeForge) SetCompareBaseline(owner, repo, defaultBranch string) {
	f.owner, f.repo, f.branch = owner, repo, defaultBranch
}

func TestStartFetchRestoresSourceBaseline(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now().UTC()
	parent := &forge.ParentData{
		FullName: "mid/child", DefaultBranch: "dev",
		SourceFullPath: "root/orig", SourceDefaultBranch: "main",
	}
	if err := db.UpsertSnapshot(context.Background(), store.Snapshot{
		Repo: store.RepoRecord{
			Provider: "github", Host: "github.com", Owner: "mid", Name: "child",
			FirstSeen: now, LastSeen: now, Parent: parent, ForksSyncedAt: now,
		},
		Fork: store.ForkRecord{ForgeID: "a/f", Owner: "a", Name: "f", UpdatedAt: now},
		T1:   &forge.T1Data{ID: "a/f", Owner: "a", Name: "f"},
	}); err != nil {
		t.Fatal(err)
	}
	fake := &restoreFakeForge{}
	m := &Model{
		provider: fake,
		db:       db,
		auth:     forge.AuthInfo{Provider: forge.ProviderGitHub, Host: "github.com"},
		input:    "mid/child",
	}
	cmd := m.startFetch()
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	msg := cmd()
	if _, ok := msg.(cachedLoadMsg); !ok {
		t.Fatalf("got %T, want cachedLoadMsg", msg)
	}
	if fake.owner != "root" || fake.repo != "orig" || fake.branch != "main" {
		t.Fatalf("restored %s/%s %s", fake.owner, fake.repo, fake.branch)
	}
}

func TestStartFetchRestoresSeedWhenSourceAbsent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now().UTC()
	parent := &forge.ParentData{
		FullName: "mid/child", DefaultBranch: "dev",
	}
	if err := db.UpsertSnapshot(context.Background(), store.Snapshot{
		Repo: store.RepoRecord{
			Provider: "github", Host: "github.com", Owner: "mid", Name: "child",
			FirstSeen: now, LastSeen: now, Parent: parent, ForksSyncedAt: now,
		},
		Fork: store.ForkRecord{ForgeID: "a/f", Owner: "a", Name: "f", UpdatedAt: now},
		T1:   &forge.T1Data{ID: "a/f", Owner: "a", Name: "f"},
	}); err != nil {
		t.Fatal(err)
	}
	fake := &restoreFakeForge{}
	m := &Model{
		provider: fake,
		db:       db,
		auth:     forge.AuthInfo{Provider: forge.ProviderGitHub, Host: "github.com"},
		input:    "mid/child",
	}
	cmd := m.startFetch()
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	msg := cmd()
	if _, ok := msg.(cachedLoadMsg); !ok {
		t.Fatalf("got %T, want cachedLoadMsg", msg)
	}
	if fake.owner != "mid" || fake.repo != "child" || fake.branch != "dev" {
		t.Fatalf("restored %s/%s %s", fake.owner, fake.repo, fake.branch)
	}
}
