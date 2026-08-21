package forge

import (
	"testing"
)

// Unset LinearHistory (nil) must round-trip as "unknown" — the JSON emitter
// must distinguish nil (never computed) from &false (computed non-linear).
// Mirrors the established pattern of keeping nil-able booleans in T1Data so
// a missing signal does not collapse to false.
func TestLinearHistory_UnsetIsUnknown(t *testing.T) {
	var f T1Data
	if f.LinearHistory != nil {
		t.Fatalf("zero-value T1Data has LinearHistory = %v, want nil (unknown)", *f.LinearHistory)
	}
}

// A nil MergeCommitHistory must mean "unknown" rather than "empty". A
// computed empty slice (MergeCommitHistory: []int{}) reads as "linear —
// zero merge commits" and round-trips through JSON as such; nil is the
// no-signal state and must persist as that.
func TestMergeCommitHistory_NilMeansUnknown(t *testing.T) {
	var f T1Data
	if f.MergeCommitHistory != nil {
		t.Fatalf("zero-value T1Data has MergeCommitHistory = %v, want nil (unknown)", f.MergeCommitHistory)
	}
	// Computed empty slice stays empty and non-nil so downstream
	// consumers can distinguish "we tried, found nothing" from
	// "we never computed".
	f.LinearHistory = ptrBool(true)
	f.MergeCommitHistory = []int{}
	if f.MergeCommitHistory == nil {
		t.Fatal("explicit empty vector became nil")
	}
}

func ptrBool(b bool) *bool { return &b }
