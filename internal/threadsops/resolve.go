package threadsops

import (
	"context"
	"time"

	"github.com/svnbjrn/spoon/internal/github"
)

const bodySatisfiedRecencyWindow = 60 * time.Second

// Resolve fetches the PR and resolves the named thread. See the package docs
// for the full state machine: not-found, idempotent, body-required gate,
// body-satisfied dedup, partial-failure handling.
//
// Returns:
//
//	thread: the (now-)resolved thread, or nil on error
//	wasAlreadyResolved: true if the thread was already resolved on entry
//	  (no API mutations performed) — useful for callers that want to
//	  surface an idempotency warning.
//	opErr: error envelope or nil
func Resolve(ctx context.Context, api API, owner, repo string, number int, threadID, body string) (*ReviewThreadWithPolicy, bool, *OpError) {
	_, all, err := api.FetchPR(ctx, owner, repo, number, github.ThreadStateAll)
	if err != nil {
		return nil, false, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	return ResolveWithThreads(ctx, api, all, threadID, body)
}

// ResolveWithThreads is like Resolve but accepts a pre-fetched thread slice,
// skipping the internal FetchPR call. Use this when the caller has already
// fetched the threads for another reason (e.g., spoon fetches them for the
// status header).
func ResolveWithThreads(ctx context.Context, api API, threads []github.ReviewThread, threadID, body string) (*ReviewThreadWithPolicy, bool, *OpError) {
	var target *github.ReviewThread
	for i := range threads {
		if threads[i].ID == threadID {
			target = &threads[i]
			break
		}
	}
	if target == nil {
		return nil, false, &OpError{Code: OpCodeNotFound, Message: "thread " + threadID + " not found on PR", Details: map[string]any{"thread_id": threadID}}
	}
	return resolveTarget(ctx, api, target, threadID, body)
}

// resolveTarget runs the post-fetch state machine. Shared by Resolve and
// ResolveWithThreads.
func resolveTarget(ctx context.Context, api API, target *github.ReviewThread, threadID, body string) (*ReviewThreadWithPolicy, bool, *OpError) {
	annotated := AnnotateOneWithPolicy(target)
	if target.IsResolved {
		return annotated, true, nil
	}
	if annotated.RequiresBody && body == "" {
		if !bodySatisfied(ctx, api, target) {
			return nil, false, &OpError{
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
			return nil, false, &OpError{Code: OpCodeUpstream, Message: "reply failed: " + rerr.Error(), Retryable: true}
		}
		commentID = c.ID
	}
	if rerr := api.ResolveThread(ctx, threadID); rerr != nil {
		details := map[string]any{"thread_id": threadID}
		if commentID != "" {
			details["comment_posted"] = true
			details["comment_id"] = commentID
		}
		return nil, false, &OpError{
			Code:      OpCodeUpstream,
			Message:   "comment posted but resolve failed: " + rerr.Error(),
			Retryable: true,
			Details:   details,
		}
	}
	annotated.IsResolved = true
	return annotated, false, nil
}

// bodySatisfied returns true when the body-required gate should be skipped
// because the agent has already explained itself on this thread.
//
// Primary signal: most recent comment is authored by the current authenticated
// user.
//
// Fallback (only when CurrentUserLogin succeeds but returns empty login):
// most recent comment is within bodySatisfiedRecencyWindow. If
// CurrentUserLogin returns an error, return false without falling back —
// we can't safely assume identity.
func bodySatisfied(ctx context.Context, api API, t *github.ReviewThread) bool {
	if len(t.Comments) == 0 {
		return false
	}
	last := t.Comments[len(t.Comments)-1]
	login, err := api.CurrentUserLogin(ctx)
	if err != nil {
		return false
	}
	if login != "" {
		return last.Author == login
	}
	// err == nil && login == "": fall back to recency heuristic.
	createdAt, parseErr := time.Parse(time.RFC3339, last.CreatedAt)
	if parseErr != nil {
		return false
	}
	return time.Since(createdAt) <= bodySatisfiedRecencyWindow
}
