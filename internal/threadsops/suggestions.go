package threadsops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Suggestion is a parsed ```suggestion ... ``` block from a comment body.
type Suggestion struct {
	CommentID  string `json:"commentId"`
	Body       string `json:"body"`       // the suggestion content (without fences)
	Applicable bool   `json:"applicable"` // true if the thread has a path + line range
}

// ParseSuggestions extracts every suggestion block from a comment body in
// document order. Returns an empty slice if none.
//
// Handles:
//   - Standard fenced block: ```suggestion\n...\n```
//   - Tilde fences: ~~~suggestion ... ~~~
//   - Multi-line content (newlines preserved as-is)
//   - Nested code fences with different fence chars (rare but possible)
//
// Tag is matched case-insensitively. Indentation before the fence is preserved
// in the body verbatim (let the apply step deal with it).
func ParseSuggestions(commentID, body string) []Suggestion {
	out := []Suggestion{}
	if body == "" {
		return out
	}
	// Walk lines. A suggestion block opens with a fence line whose trimmed-left
	// text begins with three or more backticks (or tildes) followed by the tag
	// "suggestion" (case-insensitive). It closes with the *same* fence char at
	// the *same or greater* length with no info string.
	lines := splitLinesKeepEmpty(body)
	i := 0
	for i < len(lines) {
		line := lines[i]
		fenceChar, fenceLen, isOpen := matchSuggestionFenceOpen(line)
		if !isOpen {
			i++
			continue
		}
		// Capture body lines until we hit a matching close fence.
		var bodyLines []string
		j := i + 1
		closed := false
		for j < len(lines) {
			if isFenceClose(lines[j], fenceChar, fenceLen) {
				closed = true
				break
			}
			bodyLines = append(bodyLines, lines[j])
			j++
		}
		// If not closed (truncated), still record what we have.
		sugBody := strings.Join(bodyLines, "\n")
		out = append(out, Suggestion{
			CommentID: commentID,
			Body:      sugBody,
		})
		if closed {
			i = j + 1
		} else {
			i = j
		}
	}
	return out
}

// splitLinesKeepEmpty splits on '\n' without trimming the trailing empty after
// a final newline (so "a\n" → ["a", ""]). We then ignore the final "" when
// joining bodyLines later to preserve user expectations.
//
// The wrapper exists for symmetry with the join+trim logic elsewhere in this
// file: callers reason about "lines kept verbatim" vs "lines re-joined and
// retrimmed", and the named function makes that distinction visible.
func splitLinesKeepEmpty(s string) []string {
	// strings.Split keeps the trailing empty if s ends with \n. That's
	// actually what we want here — the close-fence detection doesn't care.
	return strings.Split(s, "\n")
}

// matchSuggestionFenceOpen reports whether line is a suggestion-block opening
// fence. Returns fenceChar (either '`' or '~'), the fence length (number of
// fence chars), and whether a match was found.
//
// The opening fence allows any leading whitespace, then 3+ fence chars, then
// the literal "suggestion" tag (case-insensitive), then optional trailing
// whitespace. We tolerate a trailing info string after "suggestion" only when
// it is preceded by whitespace; this matches GitHub's lenient renderer.
func matchSuggestionFenceOpen(line string) (rune, int, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) < 4 { // "```s" minimum
		return 0, 0, false
	}
	var fc byte
	switch trimmed[0] {
	case '`':
		fc = '`'
	case '~':
		fc = '~'
	default:
		return 0, 0, false
	}
	// Count fence chars.
	n := 0
	for n < len(trimmed) && trimmed[n] == fc {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	rest := trimmed[n:]
	// Allow optional whitespace before the tag (GitHub allows none; we are
	// generous). Then the tag itself, case-insensitive.
	rest = strings.TrimLeft(rest, " \t")
	const tag = "suggestion"
	if len(rest) < len(tag) {
		return 0, 0, false
	}
	if !strings.EqualFold(rest[:len(tag)], tag) {
		return 0, 0, false
	}
	after := rest[len(tag):]
	// After the tag we must see either EOL, whitespace, or nothing. If there
	// are extra non-space characters (e.g. "suggestionXYZ"), it's not a match.
	if after == "" {
		return rune(fc), n, true
	}
	if after[0] == ' ' || after[0] == '\t' {
		return rune(fc), n, true
	}
	return 0, 0, false
}

// isFenceClose reports whether line is a closing fence for the given fence
// char and minimum length. A closing fence is leading whitespace + N+ fence
// chars + optional trailing whitespace (no info string).
func isFenceClose(line string, fenceChar rune, minLen int) bool {
	trimmed := strings.TrimLeft(line, " \t")
	fc := byte(fenceChar)
	n := 0
	for n < len(trimmed) && trimmed[n] == fc {
		n++
	}
	if n < minLen {
		return false
	}
	rest := strings.TrimRight(trimmed[n:], " \t")
	return rest == ""
}

// AnnotateThreadsWithSuggestions walks each thread's comments and attaches the
// parsed suggestions to a returned per-thread slice. Pure function; doesn't
// mutate the inputs.
//
// The returned slice has the same length and ordering as `threads`. Each entry
// is the flat list of suggestions across all comments in that thread, in
// document order (comment order × within-body order).
func AnnotateThreadsWithSuggestions(threads []ReviewThreadWithPolicy) [][]Suggestion {
	out := make([][]Suggestion, len(threads))
	for i, t := range threads {
		var sugs []Suggestion
		applicable := t.Path != "" && t.Line > 0
		for _, c := range t.Comments {
			for _, s := range ParseSuggestions(c.ID, c.Body) {
				s.Applicable = applicable
				sugs = append(sugs, s)
			}
		}
		out[i] = sugs
	}
	return out
}

// PopulateSuggestions sets Suggestions on each ReviewThreadWithPolicy in
// place, by calling AnnotateThreadsWithSuggestions. This is the convenience
// glue used by List/Next/Resolve before serializing JSON.
func PopulateSuggestions(threads []ReviewThreadWithPolicy) {
	per := AnnotateThreadsWithSuggestions(threads)
	for i := range threads {
		if len(per[i]) > 0 {
			threads[i].Suggestions = per[i]
		}
	}
}

// PopulateOneSuggestions sets Suggestions on a single ReviewThreadWithPolicy.
func PopulateOneSuggestions(t *ReviewThreadWithPolicy) {
	if t == nil {
		return
	}
	one := []ReviewThreadWithPolicy{*t}
	per := AnnotateThreadsWithSuggestions(one)
	if len(per) > 0 && len(per[0]) > 0 {
		t.Suggestions = per[0]
	}
}

// ApplyOptions controls how a suggestion is applied to the local working tree.
type ApplyOptions struct {
	RepoRoot string // local checkout root (default: current dir)
	DryRun   bool   // if true, print what would change but don't write
	Force    bool   // bypass outdated-thread check AND dirty-file content check

	// Fetcher, Owner, Repo, and PRHeadRef enable the dirty-file content check.
	// When Fetcher != nil AND PRHeadRef != "" AND !Force, ApplySuggestion
	// fetches the file content at the PR head and refuses to apply if the
	// local working-tree file differs (the suggestion's line numbers anchor
	// to what the reviewer saw, so a dirty local copy could mis-apply the
	// change). A nil Fetcher skips the check entirely (back-compat for
	// callers that don't have a content-fetching API available — e.g.
	// tests). Fetch failures are treated as a soft skip (the check is a
	// safety net, not a hard requirement).
	Fetcher   ContentFetcher
	Owner     string
	Repo      string
	PRHeadRef string
}

// ApplyResult is the outcome of an ApplySuggestion call.
type ApplyResult struct {
	Path     string   `json:"path"`
	Applied  bool     `json:"applied"`
	DryRun   bool     `json:"dryRun"`
	OldLines []string `json:"oldLines"` // what was replaced
	NewLines []string `json:"newLines"` // what was written
	Message  string   `json:"message"`  // human-readable status
}

// ApplySuggestion writes the suggestion's body into the file at the thread's
// path:line range. Safety checks (unless opts.Force):
//   - thread.Path is resolved against opts.RepoRoot; paths that escape the
//     repo root (e.g. "../foo") or are absolute are rejected as bad_input.
//   - Symlinks at the resolved path are refused (writes never follow links).
//   - File must exist at filepath.Join(opts.RepoRoot, thread.Path).
//   - Thread must not be outdated (IsOutdated false).
//   - When opts.Fetcher and opts.PRHeadRef are set, the working-tree file
//     content must match the PR head's content at thread.Path (dirty-file
//     check). Force bypasses this AND the outdated check (but NOT the
//     path-traversal/symlink guards — those are always enforced).
//   - The file's lines [startLine, line] inclusive will be replaced with
//     the suggestion body. If startLine is unset, just `line` is replaced.
//   - An empty suggestion body deletes the range entirely (no orphan blank
//     line). A single-newline body still replaces with one blank line.
//
// Error code semantics (mapped to agentio.Code):
//   - bad_input: thread has no Path/Line, path escapes the repo root, path
//     is a symlink, or the line range is out of bounds
//   - not_found: file does not exist on disk
//   - policy_violation: thread is outdated and Force is false, OR the
//     working-tree file differs from the PR head and Force is false
//   - internal: read/write failure
func ApplySuggestion(ctx context.Context, thread ReviewThreadWithPolicy, sug Suggestion, opts ApplyOptions) (ApplyResult, *OpError) {
	res := ApplyResult{DryRun: opts.DryRun}
	if thread.Path == "" || thread.Line <= 0 {
		return res, &OpError{
			Code:    OpCodeBadInput,
			Message: "thread has no path/line anchor; cannot apply suggestion",
			Details: map[string]any{"thread_id": thread.ID},
		}
	}
	if !opts.Force && thread.IsOutdated {
		return res, &OpError{
			Code:    OpCodePolicy,
			Message: "thread is outdated; refusing to apply (use --force to override)",
			Details: map[string]any{"thread_id": thread.ID, "outdated": true},
		}
	}
	root := opts.RepoRoot
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return res, &OpError{Code: OpCodeInternal, Message: "getwd: " + err.Error()}
		}
	}
	// Resolve the repo root to an absolute, cleaned path and then confirm the
	// joined target stays within it. Without this guard, a malicious
	// thread.Path like "../../../.ssh/authorized_keys" (the path is
	// reviewer-controlled — set by the PR comment anchor on GitHub's side)
	// would let an `apply-suggestion` invocation clobber arbitrary files on
	// the user's machine.
	//
	// We reject absolute paths up front: filepath.Join("/root", "/etc/foo")
	// silently strips the leading slash and produces "/root/etc/foo", which
	// would falsely pass the .. check below.
	if filepath.IsAbs(thread.Path) {
		return res, &OpError{
			Code:    OpCodeBadInput,
			Message: fmt.Sprintf("path %q escapes repo root", thread.Path),
			Details: map[string]any{"path": thread.Path},
		}
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return res, &OpError{Code: OpCodeInternal, Message: "resolve root path: " + err.Error()}
	}
	absRoot = filepath.Clean(absRoot)
	cleaned := filepath.Clean(filepath.Join(absRoot, thread.Path))
	rel, relErr := filepath.Rel(absRoot, cleaned)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return res, &OpError{
			Code:    OpCodeBadInput,
			Message: fmt.Sprintf("path %q escapes repo root", thread.Path),
			Details: map[string]any{"path": thread.Path, "repoRoot": absRoot},
		}
	}
	full := cleaned
	res.Path = thread.Path
	// Symlink guard: refuse to write through a symlink. A repo can legitimately
	// contain a symlink that points outside it (e.g. `vendor/foo` → /etc/passwd);
	// writing through it would clobber the target. Block all symlink writes —
	// users who need this can resolve the symlink themselves and pass
	// --repo-root pointing at the resolved location. Skip the check entirely
	// when the file doesn't exist (handled by the os.ReadFile branch below).
	if info, lerr := os.Lstat(full); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return res, &OpError{
			Code:    OpCodeBadInput,
			Message: fmt.Sprintf("path %q is a symlink; refusing to write through it", thread.Path),
			Details: map[string]any{"path": thread.Path},
		}
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return res, &OpError{
				Code:    OpCodeNotFound,
				Message: "file not found: " + thread.Path,
				Details: map[string]any{"path": full},
			}
		}
		return res, &OpError{Code: OpCodeInternal, Message: "read file: " + err.Error()}
	}
	// Dirty-file check: if the caller wired a Fetcher + PRHeadRef, fetch the
	// file content at the PR head and refuse to apply if the working-tree file
	// differs. This guarantees the suggestion's 1-indexed line numbers still
	// anchor to what the reviewer saw. Force bypasses the check.
	if !opts.Force && opts.Fetcher != nil && opts.PRHeadRef != "" {
		expected, ferr := opts.Fetcher.FetchFileContent(ctx, opts.Owner, opts.Repo, thread.Path, opts.PRHeadRef)
		if ferr == nil && expected != "" && expected != string(data) {
			return res, &OpError{
				Code:    OpCodePolicy,
				Message: "working-tree file differs from PR head; use --force to apply anyway",
				Details: map[string]any{
					"path":      thread.Path,
					"prHeadRef": opts.PRHeadRef,
				},
			}
		}
		// On fetch error or empty content, fall through (graceful skip — the
		// check is a safety net, not a hard requirement).
	}
	// Split preserving the trailing-newline behavior we'll restore on write.
	hadTrailingNewline := strings.HasSuffix(string(data), "\n")
	content := string(data)
	if hadTrailingNewline {
		content = strings.TrimSuffix(content, "\n")
	}
	lines := strings.Split(content, "\n")
	// Determine the [start, end] (1-based, inclusive) range.
	end := thread.Line
	start := end
	if thread.StartLine != nil && *thread.StartLine > 0 {
		start = *thread.StartLine
	}
	if start > end {
		start, end = end, start
	}
	if start < 1 || end > len(lines) {
		return res, &OpError{
			Code:    OpCodeBadInput,
			Message: fmt.Sprintf("thread line range %d..%d out of bounds (file has %d lines)", start, end, len(lines)),
			Details: map[string]any{"path": thread.Path, "fileLines": len(lines)},
		}
	}
	oldLines := append([]string(nil), lines[start-1:end]...)
	// Split suggestion body into new lines. An empty body means "delete the
	// range entirely" — collapse without leaving an orphan blank line. A
	// single-newline body ("\n") preserves the historical "replace with one
	// blank line" behaviour, because that's an explicit single empty line.
	var newLines []string
	if sug.Body == "" {
		newLines = nil
	} else {
		newLines = strings.Split(sug.Body, "\n")
		// A body that ends with "\n" produces a trailing empty element from
		// Split. Drop it so the suggestion's trailing newline doesn't
		// translate into an extra blank line in the file.
		if strings.HasSuffix(sug.Body, "\n") && len(newLines) > 0 && newLines[len(newLines)-1] == "" {
			newLines = newLines[:len(newLines)-1]
		}
	}
	res.OldLines = oldLines
	res.NewLines = newLines
	if opts.DryRun {
		res.Applied = false
		res.Message = fmt.Sprintf("would replace %d line(s) in %s", end-start+1, thread.Path)
		return res, nil
	}
	out := make([]string, 0, len(lines)-len(oldLines)+len(newLines))
	out = append(out, lines[:start-1]...)
	out = append(out, newLines...)
	out = append(out, lines[end:]...)
	joined := strings.Join(out, "\n")
	if hadTrailingNewline {
		joined += "\n"
	}
	if err := os.WriteFile(full, []byte(joined), 0o644); err != nil {
		return res, &OpError{Code: OpCodeInternal, Message: "write file: " + err.Error()}
	}
	res.Applied = true
	res.Message = fmt.Sprintf("replaced %d line(s) in %s", end-start+1, thread.Path)
	return res, nil
}

// WrapSuggestionBody wraps the supplied body in a `suggestion` fenced block,
// optionally prefixed by an intro line. Used by the --suggest flag on reply.
//
// Format:
//
//	<intro>
//
//	```suggestion
//	<body>
//	```
//
// If intro is empty, the default "How about this?" is used. The body is
// written verbatim — leading/trailing whitespace, blank lines, and final
// newlines all preserved. The result has no trailing newline; callers that
// want one must add it.
func WrapSuggestionBody(intro, body string) string {
	if intro == "" {
		intro = "How about this?"
	}
	// Trim a single trailing newline off body so the closing fence sits on
	// its own line. Internal newlines are kept as-is.
	body = strings.TrimRight(body, "\n")
	var b strings.Builder
	b.WriteString(intro)
	b.WriteString("\n\n```suggestion\n")
	b.WriteString(body)
	b.WriteString("\n```")
	return b.String()
}
