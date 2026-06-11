package threadsops

import (
	"context"

	"github.com/svnbjrn/spoon/internal/github"
)

// API is the subset of *github.Client that threadsops needs. *github.Client
// satisfies this directly; tests pass a fake implementation.
type API interface {
	FetchPR(ctx context.Context, owner, repo string, number int, states string) (github.PullRequestStatus, []github.ReviewThread, error)
	ReplyToThread(ctx context.Context, threadID, body string) (github.ThreadComment, error)
	ResolveThread(ctx context.Context, threadID string) error
	UnresolveThread(ctx context.Context, threadID string) error
	CurrentUserLogin(ctx context.Context) (string, error)
}

// Compile-time check that *github.Client satisfies API.
var _ API = (*github.Client)(nil)
