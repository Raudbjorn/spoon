package threadsops

import (
	"context"
	"time"

	"github.com/svnbjrn/spoon/internal/github"
)

const bodySatisfiedRecencyWindow = 60 * time.Second

// Resolve resolves a single thread. See spec for the full state machine:
//   - Thread not found → OpCodeNotFound
//   - Already resolved → return current state (idempotent)
//   - RequiresBody && body == "" → OpCodePolicy unless body-satisfied
//   - Body provided → post comment, then resolve; partial failure flagged
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
		if !bodySatisfied(ctx, api, target) {
			return nil, &OpError{
				Code:    OpCodePolicy,
				Message: "thread has a human commenter; --body is required",
				Details: map[string]any{"thread_id": threadID},
			}
		}
	}
	commentID := ""
	if body != "" {
		c, rerr := api.ReplyToThread(ctx, threadID, body)
		if rerr != nil {
			return nil, &OpError{Code: OpCodeUpstream, Message: "reply failed: " + rerr.Error(), Retryable: true}
		}
		commentID = c.ID
	}
	if rerr := api.ResolveThread(ctx, threadID); rerr != nil {
		details := map[string]any{"thread_id": threadID}
		if commentID != "" {
			details["comment_posted"] = true
			details["comment_id"] = commentID
		}
		return nil, &OpError{
			Code:      OpCodeUpstream,
			Message:   "comment posted but resolve failed: " + rerr.Error(),
			Retryable: true,
			Details:   details,
		}
	}
	annotated.IsResolved = true
	return annotated, nil
}

// bodySatisfied returns true when the body-required gate should be skipped
// because the agent has already explained itself on this thread.
//
// Primary signal: most recent comment is authored by the current authenticated
// user. Fallback (when CurrentUserLogin fails or returns empty): most recent
// comment is within bodySatisfiedRecencyWindow.
func bodySatisfied(ctx context.Context, api API, t *github.ReviewThread) bool {
	if len(t.Comments) == 0 {
		return false
	}
	last := t.Comments[len(t.Comments)-1]

	if login, err := api.CurrentUserLogin(ctx); err == nil && login != "" {
		return last.Author == login
	}

	createdAt, err := time.Parse(time.RFC3339, last.CreatedAt)
	if err != nil {
		return false
	}
	return time.Since(createdAt) <= bodySatisfiedRecencyWindow
}
