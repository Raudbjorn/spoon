package forksops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// writeOwnerProfileCache writes a schema-v2 owner-profile file where
// LoadCachedOwnerProfile looks. Used to pin the cache-first helper
// Stream calls before the live-fetch cap.
func writeOwnerProfileCache(t *testing.T, login string, complete bool) time.Time {
	t.Helper()
	fetched := time.Now().UTC().Truncate(time.Second)
	dir := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "spoon", "owner-profiles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"schema_version":      2,
		"login":               login,
		"total_public_repos":  9,
		"fork_count":          6,
		"signal_fork_count":   2,
		"non_fork_repo_count": 3,
		"complete":            complete,
		"sample_order":        "pushed_desc",
		"fetched_at":          fetched.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strings.ToLower(login)+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return fetched
}

// TestApplyCachedOwnerProfile_Hit applies a fresh cache file with no
// GitHub client: this is the branch Stream takes before the cap,
// reserve, and client gates, so an exhausted cap still serves evidence.
func TestApplyCachedOwnerProfile_Hit(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fetched := writeOwnerProfileCache(t, "alice", true)
	fork := forge.T1Data{Owner: "alice"}
	var result Result
	if !applyCachedOwnerProfile(&fork, &result, 24*time.Hour) {
		t.Fatal("expected a cache hit")
	}
	if fork.OwnerProfile == nil {
		t.Fatal("fork.OwnerProfile is nil")
	}
	if result.Fork.OwnerProfile != fork.OwnerProfile {
		t.Fatal("result must mirror the same profile pointer")
	}
	p := fork.OwnerProfile
	if p.Login != "alice" || p.TotalPublicRepos != 9 || p.ForkCount != 6 ||
		p.SignalForkCount != 2 || p.NonForkRepoCount != 3 ||
		!p.Complete || p.SampleOrder != "pushed_desc" || !p.FetchedAt.Equal(fetched) {
		t.Errorf("profile: %+v", p)
	}
}

// TestApplyCachedOwnerProfile_Miss leaves nil meaning no signal, the
// same as a skipped live fetch. Stream then falls through to the cap.
func TestApplyCachedOwnerProfile_Miss(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fork := forge.T1Data{Owner: "alice"}
	var result Result
	if applyCachedOwnerProfile(&fork, &result, 24*time.Hour) {
		t.Fatal("empty cache must miss")
	}
	if fork.OwnerProfile != nil || result.Fork.OwnerProfile != nil {
		t.Fatal("miss must not invent a profile")
	}
}

// TestApplyCachedOwnerProfile_TTLZeroIsMiss is the --refresh path:
// ttl 0 disables the read so Stream does not serve a stale file
// before the live-fetch gates.
func TestApplyCachedOwnerProfile_TTLZeroIsMiss(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	writeOwnerProfileCache(t, "alice", true)
	fork := forge.T1Data{Owner: "alice"}
	var result Result
	if applyCachedOwnerProfile(&fork, &result, 0) {
		t.Fatal("ttl 0 must skip the cache")
	}
	if fork.OwnerProfile != nil {
		t.Fatal("ttl 0 must not apply a profile")
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
