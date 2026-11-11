package webdiff

import (
	"strings"
	"testing"
)

// TestValidatePatchProvesContextAndHunkCompleteness exercises exact declared
// row counts, accepted context-only hunks, and malformed range rejection.
func TestValidatePatchProvesContextAndHunkCompleteness(t *testing.T) {
	for _, tc := range []struct {
		name, patch string
		valid       bool
	}{
		{"replacement with context", "@@ -1,2 +1,2 @@ function\n keep\n-old\n+new\n", true},
		{"addition", "@@ -0,0 +1 @@\n+new\n", true},
		{"deletion", "@@ -1 +0,0 @@\n-old\n", true},
		{"multiple hunks", "@@ -1 +1 @@\n-old\n+new\n@@ -8 +8,2 @@\n-old\n+new\n+another\n", true},
		{"newline markers", "@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n", true},
		{"missing context with matching changes", "@@ -1,2 +1,2 @@\n-old\n+new\n", false},
		{"missing context before next hunk", "@@ -1,2 +1,2 @@\n-old\n+new\n@@ -8 +8 @@\n-x\n+y\n", false},
		{"missing header", "-old\n+new\n", false},
		{"unrecognized row", "@@ -1 +1 @@\nold\n+new\n", false},
		{"extra context", "@@ -1 +1 @@\n context\n-old\n+new\n", false},
		{"overlapping hunks", "@@ -1 +1 @@\n-old\n+new\n@@ -1 +1 @@\n-x\n+y\n", false},
		// Expanded context is complete when both declared row counts match;
		// changed rows are not required to prove that no context was omitted.
		{"unchanged hunk", "@@ -1 +1 @@\n context\n", true},
		{"empty hunk", "@@ -0,0 +0,0 @@\n", false},
		{"missing range", "@@\n-old\n+new\n", false},
		{"invalid range", "@@ -1,-1 +1 @@\n-old\n+new\n", false},
		{"invalid zero start", "@@ -0 +1 @@\n-old\n+new\n", false},
		{"overflowing range", "@@ -9223372036854775807,1 +1 @@\n-old\n+new\n", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePatch(tc.patch); (err == nil) != tc.valid {
				t.Fatalf("ValidatePatch error=%v, want valid=%v", err, tc.valid)
			}
		})
	}
}

// TestValidatePatchChecksUnchangedGaps verifies consistent inter-hunk offsets,
// including the boundary coordinates used by zero-count insertions/deletions.
func TestValidatePatchChecksUnchangedGaps(t *testing.T) {
	for _, tc := range []struct {
		name, patch string
		valid       bool
	}{
		{"equal gaps", "@@ -1 +1 @@\n-old\n+new\n@@ -10 +10 @@\n keep\n", true},
		{"unequal gaps", "@@ -1 +1 @@\n-old\n+new\n@@ -10 +9 @@\n keep\n", false},
		{"after insertion", "@@ -0,0 +1 @@\n+new\n@@ -3 +4 @@\n keep\n", true},
		{"after deletion", "@@ -1 +0,0 @@\n-old\n@@ -4 +3 @@\n keep\n", true},
		{"before insertion", "@@ -1 +1 @@\n-old\n+new\n@@ -3,0 +4 @@\n+added\n", true},
		{"before deletion", "@@ -1 +1 @@\n-old\n+new\n@@ -4 +3,0 @@\n-removed\n", true},
		{"adjacent insertion", "@@ -1 +1 @@\n-old\n+new\n@@ -1,0 +2 @@\n+added\n", true},
		{"adjacent deletion", "@@ -1 +1 @@\n-old\n+new\n@@ -2 +1,0 @@\n-removed\n", true},
		{"consecutive insertions", "@@ -0,0 +1 @@\n+first\n@@ -0,0 +2 @@\n+second\n", true},
		{"consecutive deletions", "@@ -1 +0,0 @@\n-first\n@@ -2 +0,0 @@\n-second\n", true},
		{"wrong insertion offset", "@@ -0,0 +1 @@\n+new\n@@ -3 +3 @@\n keep\n", false},
		{"wrong deletion offset", "@@ -1 +0,0 @@\n-old\n@@ -3 +3 @@\n keep\n", false},
		{"wrong gap before insertion", "@@ -1 +1 @@\n-old\n+new\n@@ -2,0 +4 @@\n+extra\n", false},
		{"wrong gap before deletion", "@@ -1 +1 @@\n-old\n+new\n@@ -4 +2,0 @@\n-extra\n", false},
		{"overlapping insertion", "@@ -1 +1 @@\n-old\n+new\n@@ -0,0 +2 @@\n+added\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePatch(tc.patch); (err == nil) != tc.valid {
				t.Fatalf("ValidatePatch error=%v, want valid=%v", err, tc.valid)
			}
		})
	}
}

// TestParseHTMLRetainsHunkCompletenessEvidence checks preserved hunk counts and
// rejects unrecognized context cells before their evidence can be discarded.
func TestParseHTMLRetainsHunkCompletenessEvidence(t *testing.T) {
	for _, contextClass := range []string{"blob-code blob-code-context", "diff-context-cell"} {
		t.Run(contextClass, func(t *testing.T) {
			fixture := `<div data-path="a.go"></div><table>
<tr><td class="blob-code blob-code-hunk">@@ -1,2 +1,2 @@</td></tr>
<tr><td class="` + contextClass + `">kept</td></tr>
<tr><td class="blob-code blob-code-deletion">old</td></tr>
<tr><td class="blob-code blob-code-addition">new</td></tr></table>`
			patches, _, err := ParseHTML(strings.NewReader(fixture))
			if contextClass != "blob-code blob-code-context" {
				// Drift must now fail during extraction: dropping the header too
				// could otherwise hide an entire context-only hunk from validation.
				if err == nil || patches != nil {
					t.Fatalf("unrecognized context accepted: patches=%v, error=%v", patches, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			patch := patches["a.go"]
			if !strings.HasPrefix(patch, "@@ -1,2 +1,2 @@\n") {
				t.Fatal("parser discarded the hunk's declared row counts")
			}
			if err := ValidatePatch(patch); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestParseHTMLRejectsUnrecognizedCodeRows prevents an unfamiliar code role
// from silently disappearing even when its base blob-code class survives.
func TestParseHTMLRejectsUnrecognizedCodeRows(t *testing.T) {
	fixture := `<div data-path="a.go"></div><table>
<tr><td class="blob-code blob-code-hunk">@@ -1,2 +1,2 @@</td></tr>
<tr><td class="blob-code new-context-class">kept</td></tr>
<tr><td class="blob-code blob-code-deletion">old</td></tr>
<tr><td class="blob-code blob-code-addition">new</td></tr></table>`
	if _, _, err := ParseHTML(strings.NewReader(fixture)); err == nil {
		t.Fatal("unrecognized code row was silently omitted")
	}
}
