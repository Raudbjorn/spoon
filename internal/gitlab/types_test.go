package gitlab

import "testing"

func TestParseDiffStats(t *testing.T) {
	tests := []struct {
		name    string
		diff    glDiff
		wantAdd int
		wantDel int
	}{
		{
			name: "simple additions and deletions",
			diff: glDiff{
				Diff: `--- a/file.go
+++ b/file.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
-    println("hello")
+    fmt.Println("hello")
 }
`,
			},
			wantAdd: 2,
			wantDel: 1,
		},
		{
			name: "empty diff",
			diff: glDiff{
				Diff: "",
			},
			wantAdd: 0,
			wantDel: 0,
		},
		{
			name: "too_large diff returns zero",
			diff: glDiff{
				TooLarge: true,
				Diff:     "+some content\n-other content\n",
			},
			wantAdd: 0,
			wantDel: 0,
		},
		{
			name: "only headers no changes",
			diff: glDiff{
				Diff: `--- a/file.txt
+++ b/file.txt
@@ -1 +1 @@
 unchanged line
`,
			},
			wantAdd: 0,
			wantDel: 0,
		},
		{
			name: "additions only",
			diff: glDiff{
				Diff: `--- /dev/null
+++ b/newfile.go
@@ -0,0 +1,3 @@
+package newpkg
+
+func New() {}
`,
			},
			wantAdd: 3,
			wantDel: 0,
		},
		{
			name: "deletions only",
			diff: glDiff{
				Diff: `--- a/oldfile.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package oldpkg
-func Old() {}
`,
			},
			wantAdd: 0,
			wantDel: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			add, del := tt.diff.parseDiffStats()
			if add != tt.wantAdd {
				t.Errorf("additions = %d, want %d", add, tt.wantAdd)
			}
			if del != tt.wantDel {
				t.Errorf("deletions = %d, want %d", del, tt.wantDel)
			}
		})
	}
}

func TestIsMergeOrSync(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"Merge branch 'main' into feature", true},
		{"Merge pull request #42 from owner/branch", true},
		{"Merge request !42 from author", true},
		{"Merge remote-tracking branch 'upstream/main'", true},
		{"Sync with upstream", true},
		{"sync upstream changes", true},
		{`Revert "Merge branch 'main'"`, true},
		{"Add new feature", false},
		{"Fix bug in parser", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			got := isMergeOrSync(tt.msg)
			if got != tt.want {
				t.Errorf("isMergeOrSync(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}
