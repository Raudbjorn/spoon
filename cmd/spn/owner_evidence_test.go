package main

import (
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
)

// TestForkToJSONOwnerEvidence pins the inspectable side of the owner
// penalty: the counts, the order they were sampled in, and whether the
// sample was the whole account. A partial sample must be visibly
// partial, otherwise "nonForkRepos: 0" reads as a fact about the owner.
func TestForkToJSONOwnerEvidence(t *testing.T) {
	fetched := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	r := forksops.Result{Fork: forge.T1Data{OwnerProfile: &forge.OwnerProfile{
		Login:            "alice",
		TotalPublicRepos: 500,
		ForkCount:        480,
		SignalForkCount:  3,
		NonForkRepoCount: 20,
		Complete:         false,
		SampleOrder:      "pushed_desc",
		FetchedAt:        fetched,
	}}}

	ev, ok := forkToJSON(r)["ownerEvidence"].(map[string]any)
	if !ok {
		t.Fatalf("ownerEvidence block missing: %v", forkToJSON(r)["ownerEvidence"])
	}
	want := map[string]any{
		"login":         "alice",
		"observedRepos": 500,
		"forks":         480,
		"signalForks":   3,
		"nonForkRepos":  20,
		"sampleOrder":   "pushed_desc",
		"complete":      false,
	}
	for k, w := range want {
		if got := ev[k]; got != w {
			t.Errorf("ownerEvidence[%q] = %v, want %v", k, got, w)
		}
	}
	if got, ok := ev["fetchedAt"].(time.Time); !ok || !got.Equal(fetched) {
		t.Errorf("ownerEvidence[fetchedAt] = %v, want %v", ev["fetchedAt"], fetched)
	}
}

// TestForkToJSONOwnerEvidenceOmittedWithoutProfile keeps nil meaning
// "no signal": no profile, no block, so a skipped or failed fetch is
// not reported as an owner with zero repositories.
func TestForkToJSONOwnerEvidenceOmittedWithoutProfile(t *testing.T) {
	if _, has := forkToJSON(forksops.Result{})["ownerEvidence"]; has {
		t.Fatal("ownerEvidence must be omitted when the owner profile is nil")
	}
}
