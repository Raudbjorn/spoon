package recon

import "testing"

func TestSummaryNewAndMerge(t *testing.T) {
	left := NewSummary("scan")
	if left.Type != "recon_summary" || left.Mode != "scan" || !left.Complete || left.Reasons == nil {
		t.Fatalf("NewSummary() = %#v", left)
	}
	left.Records = 2
	left.ScanUnits = 3
	left.PartialUnits = 1
	left.InputErrors = 4
	left.Findings = 5
	left.DedupeEvictions = 6
	left.Candidates = 7
	left.Rewrites = 8
	left.Unknown = 9
	left.GraphQLQueries = 10
	left.GraphQLCost = 11
	left.LogicalRESTCalls = 12
	left.Reasons["files_truncated"] = 1

	right := NewSummary("scan")
	right.Records = 13
	right.ScanUnits = 14
	right.PartialUnits = 15
	right.InputErrors = 16
	right.Findings = 17
	right.DedupeEvictions = 18
	right.Candidates = 19
	right.Rewrites = 20
	right.Unknown = 21
	right.GraphQLQueries = 22
	right.GraphQLCost = 23
	right.LogicalRESTCalls = 24
	right.Complete = false
	right.Reasons["files_truncated"] = 2
	right.Reasons["missing_patch"] = 3

	left.Merge(right)
	if left.Records != 15 || left.ScanUnits != 17 || left.PartialUnits != 16 || left.InputErrors != 20 || left.Findings != 22 || left.DedupeEvictions != 24 || left.Candidates != 26 || left.Rewrites != 28 || left.Unknown != 30 || left.GraphQLQueries != 32 || left.GraphQLCost != 34 || left.LogicalRESTCalls != 36 || left.Complete {
		t.Fatalf("Merge() = %#v", left)
	}
	if left.Reasons["files_truncated"] != 3 || left.Reasons["missing_patch"] != 3 {
		t.Fatalf("Merge reasons = %#v", left.Reasons)
	}
}

func TestSummaryRejectsUnknownOrMismatchedModes(t *testing.T) {
	assertPanics(t, func() { NewSummary("other") })
	assertPanics(t, func() {
		summary := NewSummary("scan")
		summary.Merge(NewSummary("rewrites"))
	})
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("operation did not panic")
		}
	}()
	fn()
}
