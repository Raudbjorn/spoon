package main

import (
	"context"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

type stubForge struct {
	parent  forge.ParentData
	forks   []forge.T1Data
	compare map[string]forge.T2Data
}

func (s *stubForge) Auth(_ context.Context) (forge.AuthInfo, error) {
	return forge.AuthInfo{}, nil
}

func (s *stubForge) Parent(_ context.Context, _, _ string) (forge.ParentData, error) {
	return s.parent, nil
}

func (s *stubForge) ListForks(_ context.Context, _, _ string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg, len(s.forks))
	for _, f := range s.forks {
		ch <- forge.ForkMsg{Fork: f}
	}
	close(ch)
	return ch, nil
}

func (s *stubForge) Branches(_ context.Context, _ forge.T1Data, _ int) ([]forge.BranchRef, error) {
	return nil, nil
}

func (s *stubForge) Compare(_ context.Context, fork forge.T1Data, _ string) (forge.T2Data, error) {
	return s.compare[fork.ID], nil
}

func (s *stubForge) Contributors(_ context.Context, _ forge.T1Data) (forge.T3Data, error) {
	return forge.T3Data{}, nil
}

func (s *stubForge) Headroom() float64 {
	return 1.0
}

func TestDumpFeatures_TopN(t *testing.T) {
	sf := &stubForge{
		forks: []forge.T1Data{
			{ID: "a", Owner: "x", Name: "r1"},
			{ID: "b", Owner: "y", Name: "r2"},
			{ID: "c", Owner: "z", Name: "r3"},
		},
		compare: map[string]forge.T2Data{
			"a": {Diffs: []forge.FileDiff{{Path: "main.go", Additions: 1, Deletions: 0}}},
			"b": {Diffs: []forge.FileDiff{{Path: "README.md", Additions: 2, Deletions: 0}}},
			"c": {Diffs: []forge.FileDiff{{Path: "go.mod", Additions: 3, Deletions: 0}}},
		},
	}
	got, err := dumpFeatures(context.Background(), sf, "owner", "repo", 2)
	if err != nil {
		t.Fatalf("dumpFeatures: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 records, got %d", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("unexpected order: %v", got)
	}
	if got[0].Features.Paths != "main.go" {
		t.Errorf("Paths not populated: %q", got[0].Features.Paths)
	}
}

func TestDumpFeatures_SkipsCompareErrors(t *testing.T) {
	sf := &stubForgeWithErr{
		stubForge: stubForge{
			forks:   []forge.T1Data{{ID: "ok", Owner: "x", Name: "r"}, {ID: "bad", Owner: "y", Name: "r"}},
			compare: map[string]forge.T2Data{"ok": {Diffs: []forge.FileDiff{{Path: "f", Additions: 1}}}},
		},
		failIDs: map[string]bool{"bad": true},
	}
	got, err := dumpFeatures(context.Background(), sf, "owner", "repo", 0)
	if err != nil {
		t.Fatalf("dumpFeatures: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ok" {
		t.Errorf("want 1 record with id=ok, got %v", got)
	}
}

type stubForgeWithErr struct {
	stubForge
	failIDs map[string]bool
}

func (s *stubForgeWithErr) Compare(ctx context.Context, fork forge.T1Data, branch string) (forge.T2Data, error) {
	if s.failIDs[fork.ID] {
		return forge.T2Data{}, context.Canceled
	}
	return s.stubForge.Compare(ctx, fork, branch)
}
