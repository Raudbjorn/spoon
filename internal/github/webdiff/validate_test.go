package webdiff

import (
	"strings"
	"testing"
)

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
		{"unchanged hunk", "@@ -1 +1 @@\n context\n", false},
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

func TestParseHTMLRetainsHunkCompletenessEvidence(t *testing.T) {
	for _, contextClass := range []string{"blob-code blob-code-context", "diff-context-cell"} {
		t.Run(contextClass, func(t *testing.T) {
			fixture := `<div data-path="a.go"></div><table>
<tr><td class="blob-code blob-code-hunk">@@ -1,2 +1,2 @@</td></tr>
<tr><td class="` + contextClass + `">kept</td></tr>
<tr><td class="blob-code blob-code-deletion">old</td></tr>
<tr><td class="blob-code blob-code-addition">new</td></tr></table>`
			patches, _, err := ParseHTML(strings.NewReader(fixture))
			if err != nil {
				t.Fatal(err)
			}
			patch := patches["a.go"]
			if !strings.HasPrefix(patch, "@@ -1,2 +1,2 @@\n") {
				t.Fatal("parser discarded the hunk's declared row counts")
			}
			// Both versions have the same +/- totals; only hunk ranges expose lost context.
			valid := contextClass == "blob-code blob-code-context"
			if err := ValidatePatch(patch); (err == nil) != valid {
				t.Fatalf("ValidatePatch error=%v, want valid=%v", err, valid)
			}
		})
	}
}

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
