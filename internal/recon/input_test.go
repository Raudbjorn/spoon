package recon

import (
	"context"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/secretscan"
)

const reconTestSecret = "spoon_test_secret_7f3c9a1e"

func reconScanner(t *testing.T) *secretscan.Scanner {
	t.Helper()
	scanner, err := secretscan.LoadRules(strings.NewReader(`{"schemaVersion":1,"version":"test-rules-v1","rules":[{"id":"spoon-test-secret","name":"Spoon test secret","severity":"warning","pattern":"(` + reconTestSecret + `)","secretGroup":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return scanner
}

func TestScanForkNDJSONMapsAggregateAndCommitProvenance(t *testing.T) {
	sha := strings.Repeat("a", 40)
	input := strings.NewReader(`{"id":"fork-owner/fork","pushed_at":"2026-10-06T01:02:03+02:00","upstream":"root-owner/root","t2":{"files":[{"path":"aggregate.go","additions":1,"patch":"@@ -0,0 +1,1 @@\n+` + reconTestSecret + `","patchSource":"compare_rest"}],"commits":[{"sha":"` + sha + `","files":[{"path":"commit.go","additions":1,"patch":"@@ -0,0 +1,1 @@\n+` + reconTestSecret + `","patchSource":"commit_rest"}]}],"commit_files_complete":true}}
`)

	var got []secretscan.Finding
	summary, err := ScanForkNDJSON(context.Background(), input, reconScanner(t).NewSession(), func(finding secretscan.Finding) error {
		got = append(got, finding)
		return nil
	})
	if err != nil {
		t.Fatalf("ScanForkNDJSON() error = %v", err)
	}
	if !summary.Complete || summary.Records != 1 || summary.ScanUnits != 2 || summary.Findings != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	if len(got) != 2 {
		t.Fatalf("findings = %#v, want aggregate and commit", got)
	}
	aggregate, commit := got[0], got[1]
	if aggregate.Source != secretscan.SourceCurrentCompare || aggregate.SnapshotID != "2026-10-05T23:02:03Z" || aggregate.CommitSHA != "" || aggregate.Repository != "fork-owner/fork" || aggregate.Fork != "fork-owner/fork" || aggregate.NetworkRoot != "root-owner/root" || aggregate.Path != "aggregate.go" || aggregate.PatchSource != "compare_rest" {
		t.Fatalf("aggregate attribution = %#v", aggregate)
	}
	if commit.Source != secretscan.SourceCurrentCommit || commit.SnapshotID != sha || commit.CommitSHA != sha || commit.Repository != "fork-owner/fork" || commit.Fork != "fork-owner/fork" || commit.NetworkRoot != "root-owner/root" || commit.Path != "commit.go" || commit.PatchSource != "commit_rest" {
		t.Fatalf("commit attribution = %#v", commit)
	}
}

func TestScanForkNDJSONTracksPartialInputWithoutDiscardingCompletePatches(t *testing.T) {
	input := strings.NewReader(`{"owner":"fork-owner","name":"fork","pushed_at":"2026-10-06T00:00:00Z","lineage":{"networkRoot":"root-owner/root"},"t2":{"files":[{"path":"complete.go","additions":1,"patch":"@@ -0,0 +1,1 @@\n+` + reconTestSecret + `","patchSource":"compare_rest"},{"path":"missing.go","additions":1,"patch":"","patchSource":"compare_rest"}],"files_truncated":true}}
{"id":"bad-owner/bad","pushed_at":"not-a-time","t2":{"files":[{"path":"ignored.go","additions":1,"patch":"@@ -0,0 +1,1 @@\n+` + reconTestSecret + `","patchSource":"compare_rest"}]}}
{"error":{"code":"upstream_error"}}
`)

	var got []secretscan.Finding
	summary, err := ScanForkNDJSON(context.Background(), input, reconScanner(t).NewSession(), func(finding secretscan.Finding) error {
		got = append(got, finding)
		return nil
	})
	if err != nil {
		t.Fatalf("ScanForkNDJSON() error = %v", err)
	}
	if summary.Complete || len(got) != 1 || summary.InputErrors != 1 || summary.Reasons["files_truncated"] == 0 || summary.Reasons["missing_patch"] == 0 || summary.Reasons["invalid_provenance"] == 0 {
		t.Fatalf("summary/findings = %#v / %#v", summary, got)
	}
	if got[0].NetworkRoot != "root-owner/root" || got[0].Repository != "fork-owner/fork" {
		t.Fatalf("fallback identity/lineage attribution = %#v", got[0])
	}
}

func TestScanForkNDJSONRejectsAllUnscannableOrMalformedInputWithoutLeakingIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{
			name:  "unfetched-files",
			input: `{"id":"owner/repo","pushed_at":"2026-10-06T00:00:00Z","t2":{"files_unfetched":true}}` + "\n",
		},
		{
			name:  "malformed-json",
			input: `{"id":"` + reconTestSecret + `"` + "\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary, err := ScanForkNDJSON(context.Background(), strings.NewReader(tc.input), reconScanner(t).NewSession(), func(secretscan.Finding) error { return nil })
			if err == nil || !IsBadInput(err) {
				t.Fatalf("ScanForkNDJSON() error = %v, want bad input", err)
			}
			if strings.Contains(err.Error(), reconTestSecret) {
				t.Fatalf("ScanForkNDJSON() leaked input in error %q", err)
			}
			if tc.name == "unfetched-files" && summary.Reasons["files_unfetched"] != 1 {
				t.Fatalf("summary = %#v", summary)
			}
		})
	}
}

func TestScanForkNDJSONMarksMalformedHunksPartialWithoutLeakingPatch(t *testing.T) {
	input := strings.NewReader(`{"id":"owner/repo","pushed_at":"2026-10-06T00:00:00Z","t2":{"files":[{"path":"bad.go","additions":1,"patch":"not a hunk ` + reconTestSecret + `","patchSource":"compare_rest"}],"commits":[],"commit_files_complete":true}}
`)
	summary, err := ScanForkNDJSON(context.Background(), input, reconScanner(t).NewSession(), func(secretscan.Finding) error { return nil })
	if err != nil {
		t.Fatalf("ScanForkNDJSON() error = %v", err)
	}
	if summary.Complete || summary.Reasons["malformed_patch"] != 1 || summary.PartialUnits != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestScanForkNDJSONRejectsLinesOverSixtyFourMiB(t *testing.T) {
	input := strings.NewReader(strings.Repeat("x", 64*1024*1024+1) + "\n")
	_, err := ScanForkNDJSON(context.Background(), input, reconScanner(t).NewSession(), func(secretscan.Finding) error { return nil })
	if err == nil || !IsBadInput(err) {
		t.Fatalf("ScanForkNDJSON() error = %v, want bad input", err)
	}
}
func TestScanForkNDJSONRejectsNonEmptyInvalidIDWithoutFallback(t *testing.T) {
	input := strings.NewReader(`{"id":"\u0000invalid","owner":"fallback","name":"repo","pushed_at":"2026-10-06T00:00:00Z","t2":{"files":[{"path":"invalid.go","additions":1,"patch":"@@ -0,0 +1,1 @@\n+spoon_test_secret_7f3c9a1e","patchSource":"compare_rest"}],"commits":[],"commit_files_complete":true}}
{"id":"owner/repo","pushed_at":"2026-10-06T00:00:00Z","t2":{"files":[{"path":"valid.go","additions":1,"patch":"@@ -0,0 +1,1 @@\n+spoon_test_secret_7f3c9a1e","patchSource":"compare_rest"}],"commits":[],"commit_files_complete":true}}
`)
	var findings []secretscan.Finding
	summary, err := ScanForkNDJSON(context.Background(), input, reconScanner(t).NewSession(), func(finding secretscan.Finding) error {
		findings = append(findings, finding)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Repository != "owner/repo" || summary.Reasons["invalid_provenance"] != 1 {
		t.Fatalf("findings/summary = %#v / %#v", findings, summary)
	}
}

func TestScanForkNDJSONMarksOmittedCommitDetailPartial(t *testing.T) {
	input := strings.NewReader(`{"id":"owner/repo","pushed_at":"2026-10-06T00:00:00Z","t2":{"files":[{"path":"valid.go","additions":1,"patch":"@@ -0,0 +1,1 @@\n+spoon_test_secret_7f3c9a1e","patchSource":"compare_rest"}]}}
`)
	summary, err := ScanForkNDJSON(context.Background(), input, reconScanner(t).NewSession(), func(secretscan.Finding) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if summary.Complete || summary.Reasons["missing_detail"] != 2 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestScanForkNDJSONClassifiesInvalidPathsAsInvalidProvenance(t *testing.T) {
	input := strings.NewReader(`{"id":"owner/repo","pushed_at":"2026-10-06T00:00:00Z","t2":{"files":[{"path":"","additions":1,"patch":"@@ -0,0 +1,1 @@\n+spoon_test_secret_7f3c9a1e","patchSource":"compare_rest"},{"path":"valid.go","additions":1,"patch":"@@ -0,0 +1,1 @@\n+spoon_test_secret_7f3c9a1e","patchSource":"compare_rest"}],"commits":[],"commit_files_complete":true}}
`)
	var findings []secretscan.Finding
	summary, err := ScanForkNDJSON(context.Background(), input, reconScanner(t).NewSession(), func(finding secretscan.Finding) error {
		findings = append(findings, finding)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || summary.Reasons["invalid_provenance"] != 1 || summary.Reasons["missing_patch"] != 0 {
		t.Fatalf("findings/summary = %#v / %#v", findings, summary)
	}
}
func TestForEachNDJSONLineAllowsSixtyFourMiBContentBeforeNewline(t *testing.T) {
	input := strings.NewReader(strings.Repeat("x", maxNDJSONLineBytes) + "\n")
	called := false
	err := forEachNDJSONLine(input, func(line []byte) error {
		called = true
		if len(line) != maxNDJSONLineBytes {
			t.Fatalf("line length = %d, want %d", len(line), maxNDJSONLineBytes)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("forEachNDJSONLine() = %v, called = %v", err, called)
	}
}
