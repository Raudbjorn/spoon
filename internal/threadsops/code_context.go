package threadsops

import (
	"context"

	"github.com/svnbjrn/spoon/internal/github"
)

// CodeContext is the fetched-and-trimmed code surrounding a thread's line range.
type CodeContext struct {
	Path      string   `json:"path"`
	Ref       string   `json:"ref"`       // commit SHA from the PR head
	StartLine int      `json:"startLine"` // first line in Lines (1-indexed, after clamping)
	EndLine   int      `json:"endLine"`   // last line in Lines (1-indexed)
	Lines     []string `json:"lines"`     // raw text, no line numbers prepended
	Outdated  bool     `json:"outdated"`  // copied from thread.IsOutdated for caller convenience
}

// ContentFetcher abstracts the file-content fetch so threadsops doesn't depend
// directly on internal/github at the type level. Implemented by *github.Client.
type ContentFetcher interface {
	FetchFileContent(ctx context.Context, owner, repo, path, ref string) (string, error)
}

// Compile-time check that *github.Client satisfies ContentFetcher.
var _ ContentFetcher = (*github.Client)(nil)

// FetchCodeContext fetches N lines of context around a thread's comment range
// from the PR head ref. Returns nil + nil if thread has no path/line anchor
// (no error — code context just isn't applicable).
//
// contextLines is symmetric: N lines BEFORE the thread's startLine and N lines
// AFTER the thread's line. Single-line threads use just `line` for both ends.
//
// Outdated threads: still fetched, but CodeContext.Outdated=true so callers
// can warn the user that the displayed code may differ from what the comment
// referenced.
//
// On fetch failure (file deleted, 404), returns nil + nil (graceful skip).
func FetchCodeContext(ctx context.Context, fetcher ContentFetcher, prHeadRef, owner, repo string, thread ReviewThreadWithPolicy, contextLines int) (*CodeContext, error) {
	if fetcher == nil || contextLines <= 0 {
		return nil, nil
	}
	if thread.Path == "" || thread.Line <= 0 {
		return nil, nil
	}
	start := thread.Line
	if thread.StartLine != nil && *thread.StartLine > 0 {
		start = *thread.StartLine
	}
	fetchStart := start - contextLines
	fetchEnd := thread.Line + contextLines
	if fetchStart < 1 {
		fetchStart = 1
	}
	content, err := fetcher.FetchFileContent(ctx, owner, repo, thread.Path, prHeadRef)
	if err != nil {
		// Treat as graceful skip rather than fatal — code context is informational.
		return nil, nil
	}
	if content == "" {
		return nil, nil
	}
	lines := github.ExtractLines(content, fetchStart, fetchEnd)
	if len(lines) == 0 {
		return nil, nil
	}
	// Recompute the effective end line after any clamping in ExtractLines.
	effectiveEnd := fetchStart + len(lines) - 1
	return &CodeContext{
		Path:      thread.Path,
		Ref:       prHeadRef,
		StartLine: fetchStart,
		EndLine:   effectiveEnd,
		Lines:     lines,
		Outdated:  thread.IsOutdated,
	}, nil
}
