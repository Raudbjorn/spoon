// Package unidiff parses a unified diff in GitHub's `.diff` compare-endpoint
// format (Accept: application/vnd.github.v3.diff) into forge.FileDiff
// entries. It exists because GitHub's JSON compare response caps files[] at
// forge.CompareFilesCap (300); the same compare, requested as a diff instead
// of JSON, is not paginated or capped and is the only way to recover the
// remaining files. See internal/github's FetchCompareDiff, the only caller.
//
// This package depends on nothing but the standard library and
// internal/forge -- it does not know about GitHub, HTTP, or the network.
package unidiff

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/forge"
)

const (
	// PatchSourceComplete marks a FileDiff.Patch parsed in full from an
	// unbounded compare diff.
	PatchSourceComplete = "compare_diff"
	// PatchSourceOversize marks a file whose hunk text exceeded the
	// caller-supplied maxPatchBytes. Patch is emptied rather than truncated:
	// a truncated patch would misrepresent the file's actual diff to
	// anything that reads it (the semantic index, the TUI patch view).
	PatchSourceOversize = "compare_diff_oversize"
)

// Parse reads a unified diff made of one or more `diff --git a/X b/Y` blocks
// from r and returns one forge.FileDiff per file, in the order they appear.
//
// Per file, Patch is the hunk text starting at the first "@@" line -- it
// excludes the "diff --git", "index", mode, and "---"/"+++" header lines,
// matching what GitHub's JSON compare response puts in FileChange.Patch.
// A file whose hunk text exceeds maxPatchBytes gets Patch = "" and
// PatchSource = PatchSourceOversize instead of a truncated (and therefore
// misleading) patch; every other file gets PatchSource = PatchSourceComplete,
// including binary files (Patch is always "" for those; there is nothing to
// truncate).
//
// Parse does not itself bound the number of bytes read from r -- callers
// reading a network response should wrap r (e.g. io.LimitReader) themselves.
func Parse(r io.Reader, maxPatchBytes int) ([]forge.FileDiff, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var files []forge.FileDiff
	var cur *fileBuilder

	for {
		line, readErr := br.ReadString('\n')
		if line != "" {
			if strings.HasPrefix(line, "diff --git ") {
				if cur != nil {
					files = append(files, cur.finish(maxPatchBytes))
				}
				cur = &fileBuilder{}
				if err := cur.parseDiffGitLine(line); err != nil {
					return nil, fmt.Errorf("unidiff: %w", err)
				}
			} else if cur != nil {
				// Lines before the first "diff --git" (there should be none
				// in a compare .diff response) are silently ignored.
				if err := cur.consume(line); err != nil {
					return nil, fmt.Errorf("unidiff: %w", err)
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, fmt.Errorf("unidiff: reading diff: %w", readErr)
		}
	}
	if cur != nil {
		files = append(files, cur.finish(maxPatchBytes))
	}
	return files, nil
}

// fileBuilder accumulates one file's diff block as Parse streams lines
// through it. Path/PreviousPath are resolved lazily in finish() because
// GitHub (like git) spreads a file's identity across several lines --
// "diff --git", "rename from"/"rename to", "copy from"/"copy to", and
// "--- "/"+++ " -- and later, unambiguous lines (rename/copy, then ---/+++)
// must win over the "diff --git" line's own provisional (and, for a path
// containing a literal " b/", potentially wrong) split.
type fileBuilder struct {
	gitOldPath, gitNewPath string

	dashOldPath   string // from "--- a/X"; "" if never seen
	dashIsDevNull bool   // "--- /dev/null" seen (this file is new)
	plusNewPath   string // from "+++ b/Y"; "" if never seen
	plusIsDevNull bool   // "+++ /dev/null" seen (this file is deleted)

	renameFrom, renameTo string
	copyFrom, copyTo     string
	newFile, deletedFile bool
	binary               bool

	additions, deletions int
	inHunk               bool // true once this file's first "@@" line is consumed
	patch                strings.Builder
}

// parseDiffGitLine parses "diff --git a/X b/Y\n" -- X and/or Y may be
// C-quoted (git's core.quotePath escaping for control/non-ASCII bytes) -- and
// records the provisional old/new paths. These are overridden by the
// unambiguous per-line paths on rename/copy/---/+++ lines when present.
func (fb *fileBuilder) parseDiffGitLine(line string) error {
	rest := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	rest = strings.TrimPrefix(rest, "diff --git ")
	a, b, err := splitGitPathPair(rest)
	if err != nil {
		return fmt.Errorf("diff --git line %q: %w", rest, err)
	}
	fb.gitOldPath = strings.TrimPrefix(a, "a/")
	fb.gitNewPath = strings.TrimPrefix(b, "b/")
	return nil
}

// splitGitPathPair splits the "a/X b/Y" (or quoted-equivalent) remainder of
// a "diff --git " line into its two path tokens (still carrying their a/ b/
// prefix; the caller strips those). Quoting is per-half: either half may be
// independently C-quoted while the other stays bare.
func splitGitPathPair(rest string) (a, b string, err error) {
	if strings.HasPrefix(rest, `"`) {
		var remainder string
		a, remainder, err = readQuoted(rest)
		if err != nil {
			return "", "", err
		}
		rest = strings.TrimPrefix(remainder, " ")
	} else {
		// The old half is bare. Its end (and the new half's start) is
		// wherever " b/" -- or, if the new half is itself quoted, ` "b/` --
		// occurs. This is inherently ambiguous when the old half itself
		// contains that exact substring; real-world paths essentially never
		// do, and the authoritative per-line paths (rename/copy/---/+++)
		// override this guess whenever they are present.
		idx := strings.Index(rest, ` "b/`)
		if idx < 0 {
			idx = strings.LastIndex(rest, " b/")
		}
		if idx < 0 {
			return "", "", fmt.Errorf("could not find new-path boundary")
		}
		a = rest[:idx]
		rest = rest[idx+1:]
	}
	if strings.HasPrefix(rest, `"`) {
		b, _, err = readQuoted(rest)
		if err != nil {
			return "", "", err
		}
		return a, b, nil
	}
	return a, rest, nil
}

// readQuoted parses a C-quoted string starting at s[0] == '"' (git's
// core.quotePath format, which -- for the escapes git actually emits:
// \a \b \f \n \r \t \v \\ \" and \NNN octal byte values -- is a subset of Go
// double-quoted string literal syntax). It returns the unquoted value and
// the remainder of s after the closing quote.
func readQuoted(s string) (value string, remainder string, err error) {
	i := 1
	for i < len(s) {
		switch s[i] {
		case '\\':
			// Skip the escaped byte itself so it can never be mistaken for
			// the closing quote; any further octal digits are harmless
			// plain characters to this boundary scan.
			i += 2
			continue
		case '"':
			raw := s[:i+1]
			v, uqErr := strconv.Unquote(raw)
			if uqErr != nil {
				return "", "", fmt.Errorf("unquoting %q: %w", raw, uqErr)
			}
			return v, s[i+1:], nil
		}
		i++
	}
	return "", "", fmt.Errorf("unterminated quoted string in %q", s)
}

// unquoteBare returns s unquoted if it looks like a C-quoted string, or s
// itself otherwise. Unlike readQuoted it never errors -- a malformed quote
// on a header line that isn't essential (mode/index/similarity lines never
// reach it) degrades to the raw text rather than failing the whole parse.
func unquoteBare(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if v, err := strconv.Unquote(s); err == nil {
			return v
		}
	}
	return s
}

// consume processes one line that is not a "diff --git" line, updating fb's
// state. Once inHunk is true every line -- regardless of what it starts
// with -- belongs to the current hunk's content; header lines like "--- "/
// "+++ " are only ever recognized before the file's first "@@", which is
// what keeps a hunk content line that happens to start with "---" or "+++"
// (e.g. a deleted line of code reading "-- foo", which becomes the diff line
// "--- foo") from being misread as a header.
func (fb *fileBuilder) consume(line string) error {
	if fb.inHunk {
		return fb.consumeHunkLine(line)
	}
	trimmed := strings.TrimRight(line, "\r\n")
	switch {
	case strings.HasPrefix(trimmed, "@@ ") || trimmed == "@@":
		fb.inHunk = true
		return fb.consumeHunkLine(line)
	case trimmed == "new file mode" || strings.HasPrefix(trimmed, "new file mode "):
		fb.newFile = true
	case trimmed == "deleted file mode" || strings.HasPrefix(trimmed, "deleted file mode "):
		fb.deletedFile = true
	case strings.HasPrefix(trimmed, "rename from "):
		fb.renameFrom = unquoteBare(strings.TrimPrefix(trimmed, "rename from "))
	case strings.HasPrefix(trimmed, "rename to "):
		fb.renameTo = unquoteBare(strings.TrimPrefix(trimmed, "rename to "))
	case strings.HasPrefix(trimmed, "copy from "):
		fb.copyFrom = unquoteBare(strings.TrimPrefix(trimmed, "copy from "))
	case strings.HasPrefix(trimmed, "copy to "):
		fb.copyTo = unquoteBare(strings.TrimPrefix(trimmed, "copy to "))
	case strings.HasPrefix(trimmed, "Binary files ") && strings.HasSuffix(trimmed, " differ"):
		fb.binary = true
	case strings.HasPrefix(trimmed, "GIT binary patch"):
		// Not expected from GitHub's compare .diff endpoint (it emits the
		// terse "Binary files ... differ" form), but handled defensively:
		// treat as binary and let any literal/delta lines that follow fall
		// through the default case below (harmless -- inHunk stays false).
		fb.binary = true
	case trimmed == "--- /dev/null":
		fb.dashIsDevNull = true
	case strings.HasPrefix(trimmed, "--- "):
		fb.dashOldPath = strings.TrimPrefix(unquoteBare(strings.TrimPrefix(trimmed, "--- ")), "a/")
	case trimmed == "+++ /dev/null":
		fb.plusIsDevNull = true
	case strings.HasPrefix(trimmed, "+++ "):
		fb.plusNewPath = strings.TrimPrefix(unquoteBare(strings.TrimPrefix(trimmed, "+++ ")), "b/")
	default:
		// old mode / new mode / index / similarity index / dissimilarity
		// index -- no data we need.
	}
	return nil
}

// consumeHunkLine appends line verbatim to the file's patch text and, based
// solely on its first byte, counts it as an addition or deletion. Context
// lines (' '), the "\ No newline at end of file" marker ('\'), and hunk
// header lines ('@') are left uncounted by falling through both cases.
func (fb *fileBuilder) consumeHunkLine(line string) error {
	fb.patch.WriteString(line)
	if line == "" {
		return nil
	}
	switch line[0] {
	case '+':
		fb.additions++
	case '-':
		fb.deletions++
	}
	return nil
}

// finish resolves the accumulated state into a forge.FileDiff. maxPatchBytes
// governs whether Patch is kept or emptied (see PatchSourceOversize).
func (fb *fileBuilder) finish(maxPatchBytes int) forge.FileDiff {
	fd := forge.FileDiff{}
	switch {
	case fb.renameFrom != "" && fb.renameTo != "":
		fd.Status = "renamed"
		fd.Path = fb.renameTo
		fd.PreviousPath = fb.renameFrom
	case fb.copyFrom != "" && fb.copyTo != "":
		fd.Status = "copied"
		fd.Path = fb.copyTo
		fd.PreviousPath = fb.copyFrom
	case fb.newFile || (fb.dashIsDevNull && !fb.plusIsDevNull):
		fd.Status = "added"
		fd.Path = firstNonEmpty(fb.plusNewPath, fb.gitNewPath, fb.gitOldPath)
	case fb.deletedFile || (fb.plusIsDevNull && !fb.dashIsDevNull):
		fd.Status = "removed"
		fd.Path = firstNonEmpty(fb.dashOldPath, fb.gitOldPath, fb.gitNewPath)
	default:
		fd.Status = "modified"
		fd.Path = firstNonEmpty(fb.plusNewPath, fb.dashOldPath, fb.gitNewPath, fb.gitOldPath)
	}
	if fd.Path == "" {
		fd.Path = firstNonEmpty(fb.gitNewPath, fb.gitOldPath)
	}

	if fb.binary {
		// Zero counts, empty patch: there is no line-oriented content to
		// report or to have exceeded maxPatchBytes.
		fd.PatchSource = PatchSourceComplete
		return fd
	}

	fd.Additions = fb.additions
	fd.Deletions = fb.deletions
	patch := fb.patch.String()
	if len(patch) > maxPatchBytes {
		fd.PatchSource = PatchSourceOversize
	} else {
		fd.Patch = patch
		fd.PatchSource = PatchSourceComplete
	}
	return fd
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
