package threadsops

import (
	"context"

	"github.com/svnbjrn/spoon/internal/github"
)

// Resolve resolves a single thread. Behavior:
//   - Returns OpCodeNotFound if the thread isn't on the PR.
//   - If already resolved, returns the current state and does nothing (idempotent).
//   - If the thread requires a body and none is provided, returns OpCodePolicy
//     unless the body-satisfied case applies (see Task 11).
//   - If body is non-empty, posts the comment, then resolves. On partial
//     failure, returns a structured error (see Task 11).
func Resolve(ctx context.Context, api API, owner, repo string, number int, threadID, body string) (*ReviewThreadWithPolicy, *OpError) {
	_, all, err := api.FetchPR(ctx, owner, repo, number, github.ThreadStateAll)
	if err != nil {
		return nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	var target *github.ReviewThread
	for i := range all {
		if all[i].ID == threadID {
			target = &all[i]
			break
		}
	}
	if target == nil {
		return nil, &OpError{Code: OpCodeNotFound, Message: "thread " + threadID + " not found on PR", Details: map[string]any{"thread_id": threadID}}
	}
	annotated := AnnotateOneWithPolicy(target)
	if target.IsResolved {
		return annotated, nil
	}
	if annotated.RequiresBody && body == "" {
		return nil, &OpError{
			Code:    OpCodePolicy,
			Message: "thread has a human commenter; --body is required",
			Details: map[string]any{"thread_id": threadID},
		}
	}
	if body != "" {
		if _, rerr := api.ReplyToThread(ctx, threadID, body); rerr != nil {
			return nil, &OpError{Code: OpCodeUpstream, Message: "reply failed: " + rerr.Error(), Retryable: true}
		}
	}
	if rerr := api.ResolveThread(ctx, threadID); rerr != nil {
		return nil, &OpError{Code: OpCodeUpstream, Message: "resolve failed: " + rerr.Error(), Retryable: true}
	}
	annotated.IsResolved = true
	return annotated, nil
}
