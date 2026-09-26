package unidiff

import (
	"os"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

// testdata/fixture.diff is `git diff -M -C --find-copies-harder main head`
// from a throwaway two-commit repo built specifically for this test (not
// checked in). Its base commit had modme.go, deleteme.txt, old_name.go,
// copysrc.go, binary.dat (marked `binary` via .gitattributes so git doesn't
// need content sniffing to treat it as binary), and café.txt (a non-ASCII
// filename, quoted by git's default core.quotePath). The head commit: edited
// modme.go, deleted deleteme.txt, added new_added.go, renamed old_name.go to
// new_name.go with a one-line content edit, copied copysrc.go byte-identical
// to copysrc_copy.go, changed binary.dat's bytes, and appended a line to
// café.txt.
//
// The expected additions/deletions below are `git diff -M -C
// --find-copies-harder --numstat main head` against that same pair of
// commits:
//
//	1	1	binary.dat            (binary: numstat shows "-  -"; see below)
//	1	0	"caf\303\251.txt"
//	0	0	copysrc.go => copysrc_copy.go
//	0	2	deleteme.txt
//	5	1	modme.go
//	5	0	new_added.go
//	1	1	old_name.go => new_name.go
//
// (numstat actually reports "-	-	binary.dat" for the binary file, GitHub's
// convention for "not applicable"; forge.FileDiff has no such value, so 0/0
// is the correct zero-value representation -- matching this package's
// "Binary files ... differ -> zero counts, empty patch" contract.)
func TestParse_Fixture(t *testing.T) {
	f, err := os.Open("testdata/fixture.diff")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	files, err := Parse(f, 64<<10)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []forge.FileDiff{
		{Path: "binary.dat", Status: "modified", Additions: 0, Deletions: 0, Patch: ""},
		{Path: "café.txt", Status: "modified", Additions: 1, Deletions: 0},
		{Path: "copysrc_copy.go", PreviousPath: "copysrc.go", Status: "copied", Additions: 0, Deletions: 0, Patch: ""},
		{Path: "deleteme.txt", Status: "removed", Additions: 0, Deletions: 2},
		{Path: "modme.go", Status: "modified", Additions: 5, Deletions: 1},
		{Path: "new_added.go", Status: "added", Additions: 5, Deletions: 0},
		{Path: "new_name.go", PreviousPath: "old_name.go", Status: "renamed", Additions: 1, Deletions: 1},
	}

	if len(files) != len(want) {
		t.Fatalf("Parse returned %d files, want %d: %+v", len(files), len(want), files)
	}
	for i, w := range want {
		got := files[i]
		if got.Path != w.Path || got.PreviousPath != w.PreviousPath || got.Status != w.Status ||
			got.Additions != w.Additions || got.Deletions != w.Deletions {
			t.Errorf("file %d:\n got  = %+v\n want = %+v", i, sanitizedForCompare(got), sanitizedForCompare(w))
		}
		if got.PatchSource != PatchSourceComplete {
			t.Errorf("file %d (%s): PatchSource = %q, want %q", i, got.Path, got.PatchSource, PatchSourceComplete)
		}
	}

	// Binary and 100%-similarity copy entries must carry no patch text.
	if files[0].Patch != "" {
		t.Errorf("binary.dat Patch = %q, want empty", files[0].Patch)
	}
	if files[2].Patch != "" {
		t.Errorf("copysrc_copy.go Patch = %q, want empty (100%% similarity, no hunks)", files[2].Patch)
	}

	// modme.go's patch: hunk text only, starting at "@@" -- no "diff --git",
	// "index", or "---"/"+++" lines.
	modme := files[4]
	if !strings.HasPrefix(modme.Patch, "@@ -1,5 +1,9 @@\n") {
		t.Errorf("modme.go Patch does not start with the hunk header:\n%s", modme.Patch)
	}
	for _, banned := range []string{"diff --git", "\nindex ", "\n--- ", "\n+++ "} {
		if strings.Contains(modme.Patch, banned) {
			t.Errorf("modme.go Patch contains %q, want header lines excluded:\n%s", banned, modme.Patch)
		}
	}
	if strings.Contains(modme.Patch, "\treturn 2") == false {
		t.Errorf("modme.go Patch missing expected content line:\n%s", modme.Patch)
	}

	// café.txt: the quoted, non-ASCII path must be unquoted to its real
	// UTF-8 value, not left as the literal escape sequence.
	if files[1].Path != "café.txt" {
		t.Errorf("café.txt Path = %q (bytes %x), want unquoted UTF-8", files[1].Path, []byte(files[1].Path))
	}
}

func sanitizedForCompare(fd forge.FileDiff) forge.FileDiff {
	fd.Patch = ""
	fd.PatchSource = ""
	return fd
}

// A rename can carry a same-size content edit with the count coming purely
// from its hunk -- this is covered by new_name.go in the fixture above, but
// pinned again here narrowly against a hand-built single-file diff so this
// property doesn't rely on git's rename-detection heuristics picking the
// exact case the fixture happens to produce.
func TestParse_RenameWithHunk(t *testing.T) {
	diff := "diff --git a/old.txt b/new.txt\n" +
		"similarity index 90%\n" +
		"rename from old.txt\n" +
		"rename to new.txt\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/old.txt\n" +
		"+++ b/new.txt\n" +
		"@@ -1,2 +1,2 @@\n" +
		" unchanged\n" +
		"-old line\n" +
		"+new line\n"

	files, err := Parse(strings.NewReader(diff), 1<<20)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	fd := files[0]
	if fd.Status != "renamed" || fd.Path != "new.txt" || fd.PreviousPath != "old.txt" {
		t.Errorf("got Status=%q Path=%q PreviousPath=%q, want renamed/new.txt/old.txt", fd.Status, fd.Path, fd.PreviousPath)
	}
	if fd.Additions != 1 || fd.Deletions != 1 {
		t.Errorf("got Additions=%d Deletions=%d, want 1/1", fd.Additions, fd.Deletions)
	}
}

// A hunk content line that happens to look like a "---"/"+++" header must
// still be counted as a deletion/addition, not misread as a second header.
func TestParse_HunkLineLooksLikeHeader(t *testing.T) {
	diff := "diff --git a/weird.txt b/weird.txt\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/weird.txt\n" +
		"+++ b/weird.txt\n" +
		"@@ -1,2 +1,2 @@\n" +
		" context\n" +
		"-- old comment\n" +
		"++ new comment\n"

	files, err := Parse(strings.NewReader(diff), 1<<20)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	fd := files[0]
	if fd.Additions != 1 || fd.Deletions != 1 {
		t.Errorf("got Additions=%d Deletions=%d, want 1/1 (the header-lookalike content lines must still count)", fd.Additions, fd.Deletions)
	}
}

func TestParse_OversizePatchIsEmptiedNotTruncated(t *testing.T) {
	diff := "diff --git a/big.txt b/big.txt\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/big.txt\n" +
		"+++ b/big.txt\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+" + strings.Repeat("x", 100) + "\n"

	files, err := Parse(strings.NewReader(diff), 10) // tiny cap, well under this hunk's size
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	fd := files[0]
	if fd.Patch != "" {
		t.Errorf("Patch = %q, want empty for an oversize hunk", fd.Patch)
	}
	if fd.PatchSource != PatchSourceOversize {
		t.Errorf("PatchSource = %q, want %q", fd.PatchSource, PatchSourceOversize)
	}
	// Counts are still accurate even though the patch text was discarded.
	if fd.Additions != 1 || fd.Deletions != 1 {
		t.Errorf("got Additions=%d Deletions=%d, want 1/1 even when the patch is emptied", fd.Additions, fd.Deletions)
	}
}

func TestParse_EmptyInput(t *testing.T) {
	files, err := Parse(strings.NewReader(""), 1<<20)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("got %d files, want 0", len(files))
	}
}

// Raw .diff bodies can carry bytes that are not UTF-8, in hunk content and in
// C-quoted paths ("\377"). Parse must hand back valid UTF-8 (the store cannot
// bind anything else as TEXT) without disturbing the line counts.
func TestParse_InvalidUTF8IsReplaced(t *testing.T) {
	diff := "diff --git \"a/cfg\\377.yaml\" \"b/cfg\\377.yaml\"\n" +
		"new file mode 100644\n" +
		"--- /dev/null\n" +
		"+++ \"b/cfg\\377.yaml\"\n" +
		"@@ -0,0 +1,2 @@\n" +
		"+version: 0.34\n" +
		"+vQ\xff\n"

	files, err := Parse(strings.NewReader(diff), 1<<20)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	fd := files[0]
	if fd.Path != "cfg�.yaml" {
		t.Errorf("Path = %q, want %q", fd.Path, "cfg�.yaml")
	}
	if want := "@@ -0,0 +1,2 @@\n+version: 0.34\n+vQ�\n"; fd.Patch != want {
		t.Errorf("Patch = %q, want %q", fd.Patch, want)
	}
	if fd.Additions != 2 || fd.Deletions != 0 {
		t.Errorf("got Additions=%d Deletions=%d, want 2/0", fd.Additions, fd.Deletions)
	}
}
