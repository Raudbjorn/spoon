package gitea

import "testing"

func TestParseUnifiedDiff(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
index 111..222 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
-var x = 1
+var x = 2
+var y = 3
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1 +1,2 @@
 # title
+a new line
`
	files := parseUnifiedDiff(diff)
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d (%+v)", len(files), files)
	}
	if files[0].Path != "main.go" {
		t.Errorf("path[0]=%q", files[0].Path)
	}
	// main.go: +import, +var x=2, +var y=3 = 3 additions; -var x=1 = 1 deletion.
	if files[0].Additions != 3 || files[0].Deletions != 1 {
		t.Errorf("main.go counts: +%d/-%d want +3/-1", files[0].Additions, files[0].Deletions)
	}
	if files[1].Path != "README.md" || files[1].Additions != 1 || files[1].Deletions != 0 {
		t.Errorf("README: %+v want path=README.md +1/-0", files[1])
	}
}

func TestParseUnifiedDiff_empty(t *testing.T) {
	if files := parseUnifiedDiff(""); files != nil {
		t.Errorf("empty diff should yield nil, got %+v", files)
	}
}

func TestParseUnifiedDiff_contentLinesWithMarkerPrefix(t *testing.T) {
	// Content lines whose text begins with "++"/"--" render as "+++"/"---" in
	// the diff. They must be counted as additions/deletions, not skipped as
	// file headers (headers always have a trailing space after the marker).
	diff := `diff --git a/x.txt b/x.txt
--- a/x.txt
+++ b/x.txt
@@ -1,2 +1,2 @@
+++foo
---bar
`
	files := parseUnifiedDiff(diff)
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d (%+v)", len(files), files)
	}
	// "+++foo" is an added "++foo"; "---bar" is a removed "--bar".
	if files[0].Additions != 1 || files[0].Deletions != 1 {
		t.Errorf("counts: +%d/-%d want +1/-1", files[0].Additions, files[0].Deletions)
	}
}

func TestParseUnifiedDiff_crlf(t *testing.T) {
	// CRLF line endings must parse identically to LF without corrupting paths
	// or miscounting content lines.
	diff := "diff --git a/main.go b/main.go\r\n--- a/main.go\r\n+++ b/main.go\r\n@@ -1 +1,2 @@\r\n package main\r\n+var x = 1\r\n"
	files := parseUnifiedDiff(diff)
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d (%+v)", len(files), files)
	}
	if files[0].Path != "main.go" || files[0].Additions != 1 || files[0].Deletions != 0 {
		t.Errorf("got %+v want path=main.go +1/-0", files[0])
	}
}

func TestGitDiffPath(t *testing.T) {
	if got := gitDiffPath("diff --git a/src/x.go b/src/x.go"); got != "src/x.go" {
		t.Errorf("got %q", got)
	}
}

func TestIsMergeOrSync(t *testing.T) {
	for _, m := range []string{"Merge branch 'main'", "merge pull request #3", "Sync with upstream"} {
		if !isMergeOrSync(m) {
			t.Errorf("should be merge/sync: %q", m)
		}
	}
	if isMergeOrSync("Add real feature") {
		t.Error("feature commit misclassified as merge/sync")
	}
}
