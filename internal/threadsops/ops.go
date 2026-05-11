package threadsops

import (
	"context"
	"sort"

	"github.com/svnbjrn/spoon/internal/github"
)

// List returns the PR status plus all threads (annotated with requiresBody).
// includeResolved=false filters to unresolved only.
func List(ctx context.Context, api API, owner, repo string, number int, includeResolved bool) (github.PullRequestStatus, []ReviewThreadWithPolicy, *OpError) {
	states := github.ThreadStateUnresolved
	if includeResolved {
		states = github.ThreadStateAll
	}
	status, raw, err := api.FetchPR(ctx, owner, repo, number, states)
	if err != nil {
		return github.PullRequestStatus{}, nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	return status, AnnotateWithPolicy(raw), nil
}

// Next returns the oldest unresolved thread (sorted by firstCommentCreatedAt
// ASC, then threadID ASC for determinism), or nil.
func Next(ctx context.Context, api API, owner, repo string, number int) (github.PullRequestStatus, *ReviewThreadWithPolicy, *OpError) {
	status, raw, err := api.FetchPR(ctx, owner, repo, number, github.ThreadStateUnresolved)
	if err != nil {
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
	return status, AnnotateOneWithPolicy(&unresolved[0]), nil
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
		return github.ThreadComment{}, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	return c, nil
}
