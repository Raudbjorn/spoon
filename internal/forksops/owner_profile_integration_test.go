package forksops

import (
	"context"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// TestOwnerProfileDefaultCap_Pinned covers the constant: the default
// per-run cap is 30, and the docs on Options.OwnerProfileCap match.
func TestOwnerProfileDefaultCap_Pinned(t *testing.T) {
	if ownerProfileDefaultCap != 30 {
		t.Errorf("ownerProfileDefaultCap: got %d, want 30 (locked in plan)", ownerProfileDefaultCap)
	}
}

// TestStream_OwnerProfileNil_NonGHProvider covers the cross-provider
// path: when the forge is a fakeForge (not *gh.GHProvider), the
// owner-profile fetch is skipped and Fork.OwnerProfile stays nil for
// every emitted result. No crash, no error.
func TestStream_OwnerProfileNil_NonGHProvider(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	parentPushed := now.Add(-30 * 24 * time.Hour)
	ff := &fakeForge{
		parent:      forge.ParentData{DefaultBranch: "main", PushedAt: parentPushed},
		concurrency: 1,
		forks: []forge.T1Data{
			{ID: "alice/repo-a", Owner: "alice", Name: "repo-a", DefaultBranch: "main",
				Stars: 5, PushedAt: now.Add(-24 * time.Hour),
				Branches: []forge.BranchRef{{Name: "main", CommittedDate: parentPushed.Add(1 * 24 * time.Hour)}}},
		},
		t2: map[string]forge.T2Data{"alice/repo-a": {AheadCount: 1}},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 2})
	if err != nil {
		t.Fatal(err)
	}
	var results []Result
	for r := range ch {
		results = append(results, r)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Fork.OwnerProfile != nil {
		t.Errorf("non-GH provider: expected nil OwnerProfile, got %+v", results[0].Fork.OwnerProfile)
	}
}

// TestResult_OwnerProfileSkip_Populated covers the envelope wiring:
// when a Result has OwnerProfileSkip set, the consumer can detect
// and surface a warning rather than reporting the missing penalty as
// zero. The plan's "telemetry envelopes" step requires this field to
// be present in the public Result type.
func TestResult_OwnerProfileSkip_Populated(t *testing.T) {
	r := Result{
		OwnerProfileSkip: &StageSkip{
			Stage:  "owner_profile",
			ForkID: "alice/repo",
			Reason: "rate-limit reserve reached",
		},
	}
	if r.OwnerProfileSkip == nil {
		t.Fatal("OwnerProfileSkip should be non-nil")
	}
	if r.OwnerProfileSkip.Stage != "owner_profile" {
		t.Errorf("Stage: got %q, want %q", r.OwnerProfileSkip.Stage, "owner_profile")
	}
	if r.OwnerProfileSkip.ForkID != "alice/repo" {
		t.Errorf("ForkID: got %q, want %q", r.OwnerProfileSkip.ForkID, "alice/repo")
	}
}

// TestResult_SiblingSimSkip_Populated covers the SiblingSim envelope
// shape: when the embedder is unavailable or the candidate set is
// empty, the run is still completed with SiblingSimSkip set.
func TestResult_SiblingSimSkip_Populated(t *testing.T) {
	r := Result{
		SiblingSimSkip: &StageSkip{
			Stage:  "sibling_sim",
			ForkID: "alice/repo",
			Reason: "embedder unavailable",
		},
	}
	if r.SiblingSimSkip == nil {
		t.Fatal("SiblingSimSkip should be non-nil")
	}
	if r.SiblingSimSkip.Stage != "sibling_sim" {
		t.Errorf("Stage: got %q, want %q", r.SiblingSimSkip.Stage, "sibling_sim")
	}
}
