package threadsops

import (
	"context"
	"sort"

	"github.com/svnbjrn/spoon/internal/github"
)

// ListOptions tunes a List call. The zero value matches the historical
// behavior (no code-context fetch).
type ListOptions struct {
	// IncludeResolved fetches resolved threads as well as unresolved. When
	// false, only unresolved threads are returned (the historical default).
	IncludeResolved bool
	// ShowCodeLines, when > 0, attaches a CodeContext to each thread with
	// N lines of context before/after the comment's anchor. Requires Fetcher
	// to be non-nil; otherwise no context is fetched.
	ShowCodeLines int
	// Fetcher is the file-content fetcher used when ShowCodeLines > 0. Pass
	// the github.Client itself (which satisfies ContentFetcher).
	Fetcher ContentFetcher
}

// List returns the PR status plus all threads (annotated with requiresBody).
// includeResolved=false filters to unresolved only.
//
// This is a back-compat wrapper around ListWithOptions for callers that don't
// need the --show-code feature.
func List(ctx context.Context, api API, owner, repo string, number int, includeResolved bool) (github.PullRequestStatus, []ReviewThreadWithPolicy, *OpError) {
	return ListWithOptions(ctx, api, owner, repo, number, ListOptions{IncludeResolved: includeResolved})
}

// ListWithOptions is the options-bearing variant of List. When
// opts.ShowCodeLines > 0 and opts.Fetcher is non-nil, each returned thread is
// enriched with a CodeContext field.
func ListWithOptions(ctx context.Context, api API, owner, repo string, number int, opts ListOptions) (github.PullRequestStatus, []ReviewThreadWithPolicy, *OpError) {
	states := github.ThreadStateUnresolved
	if opts.IncludeResolved {
		states = github.ThreadStateAll
	}
	status, raw, err := api.FetchPR(ctx, owner, repo, number, states)
	if err != nil {
		if op := rateLimitedOpError(err); op != nil {
			return github.PullRequestStatus{}, nil, op
		}
		return github.PullRequestStatus{}, nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	annotated := AnnotateWithPolicy(raw)
	PopulateSuggestions(annotated)
	if opts.ShowCodeLines > 0 && opts.Fetcher != nil {
		attachCodeContext(ctx, annotated, opts.Fetcher, status.HeadSHA, owner, repo, opts.ShowCodeLines)
	}
	return status, annotated, nil
}

// attachCodeContext populates the CodeContext field on every thread that has
// a path/line anchor. Failures are silently dropped — code context is
// informational and shouldn't break list output.
func attachCodeContext(ctx context.Context, threads []ReviewThreadWithPolicy, fetcher ContentFetcher, headSHA, owner, repo string, contextLines int) {
	for i := range threads {
		cc, _ := FetchCodeContext(ctx, fetcher, headSHA, owner, repo, threads[i], contextLines)
		if cc != nil {
			threads[i].CodeContext = cc
		}
	}
}

// NextOptions tunes a Next call. The zero value matches the historical
// behavior.
type NextOptions struct {
	// ShowCodeLines, when > 0, attaches a CodeContext to the returned thread
	// with N lines of context before/after the comment's anchor.
	ShowCodeLines int
	// Fetcher is the file-content fetcher used when ShowCodeLines > 0.
	Fetcher ContentFetcher
}

// Next returns the oldest unresolved thread (sorted by firstCommentCreatedAt
// ASC, then threadID ASC for determinism), or nil.
//
// Back-compat wrapper around NextWithOptions.
func Next(ctx context.Context, api API, owner, repo string, number int) (github.PullRequestStatus, *ReviewThreadWithPolicy, *OpError) {
	return NextWithOptions(ctx, api, owner, repo, number, NextOptions{})
}

// NextWithOptions is the options-bearing variant of Next. When
// opts.ShowCodeLines > 0 and opts.Fetcher is non-nil, the returned thread is
// enriched with a CodeContext field.
func NextWithOptions(ctx context.Context, api API, owner, repo string, number int, opts NextOptions) (github.PullRequestStatus, *ReviewThreadWithPolicy, *OpError) {
	status, raw, err := api.FetchPR(ctx, owner, repo, number, github.ThreadStateUnresolved)
	if err != nil {
		if op := rateLimitedOpError(err); op != nil {
			return github.PullRequestStatus{}, nil, op
		}
		return github.PullRequestStatus{}, nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	unresolved := make([]github.ReviewThread, 0, len(raw))
	for _, t := range raw {
		if !t.IsResolved {
			unresolved = append(unresolved, t)
		}
	}
	if len(unresolved) == 0 {
		return status, nil, nil
	}
	sort.Slice(unresolved, func(i, j int) bool {
		ai, aj := firstCommentTime(unresolved[i]), firstCommentTime(unresolved[j])
		if ai != aj {
			return ai < aj
		}
		return unresolved[i].ID < unresolved[j].ID
	})
	one := AnnotateOneWithPolicy(&unresolved[0])
	PopulateOneSuggestions(one)
	if opts.ShowCodeLines > 0 && opts.Fetcher != nil {
		cc, _ := FetchCodeContext(ctx, opts.Fetcher, status.HeadSHA, owner, repo, *one, opts.ShowCodeLines)
		if cc != nil {
			one.CodeContext = cc
		}
	}
	return status, one, nil
}

func firstCommentTime(t github.ReviewThread) string {
	if len(t.Comments) == 0 {
		// Threads with no comments are an edge case; sort them last by
		// returning a far-future RFC3339 timestamp rather than "" (which
		// would sort before all real timestamps under lexical compare).
		return "9999-12-31T23:59:59Z"
	}
	return t.Comments[0].CreatedAt
}

// Reply posts a comment on a thread. Returns the new comment.
func Reply(ctx context.Context, api API, threadID, body string) (github.ThreadComment, *OpError) {
	if body == "" {
		return github.ThreadComment{}, &OpError{Code: OpCodeBadInput, Message: "body is required for reply"}
	}
	c, err := api.ReplyToThread(ctx, threadID, body)
	if err != nil {
		if op := rateLimitedOpError(err); op != nil {
			return github.ThreadComment{}, op
		}
		return github.ThreadComment{}, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	return c, nil
}
