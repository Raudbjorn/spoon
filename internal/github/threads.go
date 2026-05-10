package github

import (
	"context"
	"fmt"
	"sync"
)

// Thread state filters for ListThreads.
const (
	ThreadStateAll        = ""
	ThreadStateUnresolved = "UNRESOLVED"
	ThreadStateResolved   = "RESOLVED"
)

// ReviewThread is the public representation of a PR review thread.
type ReviewThread struct {
	ID         string          `json:"id"`
	IsResolved bool            `json:"isResolved"`
	Path       string          `json:"path"`
	Line       int             `json:"line"`
	StartLine  *int            `json:"startLine"`
	DiffSide   string          `json:"diffSide"`
	Comments   []ThreadComment `json:"comments"`
}

// ThreadComment is one comment on a review thread.
type ThreadComment struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	AuthorType string `json:"authorType"`
	Body       string `json:"body"`
	CreatedAt  string `json:"createdAt"`
}

// listThreadsData mirrors the GraphQL response under data.
type listThreadsData struct {
	Repository struct {
		PullRequest struct {
			ReviewThreads struct {
				PageInfo struct {
					HasNextPage bool    `json:"hasNextPage"`
					EndCursor   *string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []rawThread `json:"nodes"`
			} `json:"reviewThreads"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

type rawThread struct {
	ID         string `json:"id"`
	IsResolved bool   `json:"isResolved"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	StartLine  *int   `json:"startLine"`
	DiffSide   string `json:"diffSide"`
	Comments   struct {
		Nodes []rawComment `json:"nodes"`
	} `json:"comments"`
}

type rawComment struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
	Author    struct {
		Typename string `json:"__typename"`
		Login    string `json:"login"`
	} `json:"author"`
}

func parseListThreadsResponse(data listThreadsData) []ReviewThread {
	nodes := data.Repository.PullRequest.ReviewThreads.Nodes
	out := make([]ReviewThread, 0, len(nodes))
	for _, n := range nodes {
		t := ReviewThread{
			ID:         n.ID,
			IsResolved: n.IsResolved,
			Path:       n.Path,
			Line:       n.Line,
			StartLine:  n.StartLine,
			DiffSide:   n.DiffSide,
		}
		for _, c := range n.Comments.Nodes {
			t.Comments = append(t.Comments, ThreadComment{
				ID:         c.ID,
				Author:     c.Author.Login,
				AuthorType: c.Author.Typename,
				Body:       c.Body,
				CreatedAt:  c.CreatedAt,
			})
		}
		out = append(out, t)
	}
	return out
}

// RequiresBody reports whether resolving this thread requires a reply body.
// Returns true unless every comment is authored by a Bot.
func (t ReviewThread) RequiresBody() bool {
	if len(t.Comments) == 0 {
		return true
	}
	for _, c := range t.Comments {
		if c.AuthorType != "Bot" {
			return true
		}
	}
	return false
}

// ReplyToThread appends a reply comment to a review thread. Returns the new
// comment id.
func (c *Client) ReplyToThread(ctx context.Context, threadID, body string) (string, error) {
	if c.gql == nil {
		return "", fmt.Errorf("GraphQL client not available (auth required)")
	}
	const mutation = `
mutation($threadId: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: { pullRequestReviewThreadId: $threadId, body: $body }) {
    comment { id }
  }
}`
	// go-gh's DoWithContext unmarshals the GraphQL "data" field directly into
	// the target — no outer wrapper needed (mirrors FetchForksGraphQL /
	// ListThreads pattern).
	var resp struct {
		AddPullRequestReviewThreadReply struct {
			Comment struct {
				ID string `json:"id"`
			} `json:"comment"`
		} `json:"addPullRequestReviewThreadReply"`
	}
	vars := map[string]interface{}{"threadId": threadID, "body": body}
	if err := c.gql.DoWithContext(ctx, mutation, vars, &resp); err != nil {
		return "", fmt.Errorf("reply to thread %s: %w", threadID, err)
	}
	return resp.AddPullRequestReviewThreadReply.Comment.ID, nil
}

// ResolveThread marks a review thread as resolved.
func (c *Client) ResolveThread(ctx context.Context, threadID string) error {
	return c.flipResolve(ctx, threadID, true)
}

// UnresolveThread marks a review thread as unresolved.
func (c *Client) UnresolveThread(ctx context.Context, threadID string) error {
	return c.flipResolve(ctx, threadID, false)
}

func (c *Client) flipResolve(ctx context.Context, threadID string, resolved bool) error {
	if c.gql == nil {
		return fmt.Errorf("GraphQL client not available (auth required)")
	}
	mutation := `
mutation($threadId: ID!) {
  resolveReviewThread(input: { threadId: $threadId }) { thread { id isResolved } }
}`
	verb := "resolve"
	if !resolved {
		mutation = `
mutation($threadId: ID!) {
  unresolveReviewThread(input: { threadId: $threadId }) { thread { id isResolved } }
}`
		verb = "unresolve"
	}
	var resp struct{}
	vars := map[string]interface{}{"threadId": threadID}
	if err := c.gql.DoWithContext(ctx, mutation, vars, &resp); err != nil {
		return fmt.Errorf("%s thread %s: %w", verb, threadID, err)
	}
	return nil
}

// BulkResult aggregates the outcome of a bulk thread operation.
type BulkResult struct {
	mu        sync.Mutex
	Succeeded []string
	Failed    []BulkFailure
}

// BulkFailure captures a single mutation error.
type BulkFailure struct {
	ID  string
	Err error
}

func (r *BulkResult) AddSuccess(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Succeeded = append(r.Succeeded, id)
}

func (r *BulkResult) AddFailure(id string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Failed = append(r.Failed, BulkFailure{ID: id, Err: err})
}

// ResolveAllThreads resolves every currently-unresolved thread on a PR.
func (c *Client) ResolveAllThreads(ctx context.Context, owner, repo string, number, workers int) (*BulkResult, error) {
	threads, err := c.ListThreads(ctx, owner, repo, number, ThreadStateUnresolved)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(threads))
	for i, t := range threads {
		ids[i] = t.ID
	}
	return c.bulkFlip(ctx, ids, true, workers), nil
}

// UnresolveAllThreads unresolves every currently-resolved thread on a PR.
func (c *Client) UnresolveAllThreads(ctx context.Context, owner, repo string, number, workers int) (*BulkResult, error) {
	threads, err := c.ListThreads(ctx, owner, repo, number, ThreadStateResolved)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(threads))
	for i, t := range threads {
		ids[i] = t.ID
	}
	return c.bulkFlip(ctx, ids, false, workers), nil
}

func (c *Client) bulkFlip(ctx context.Context, ids []string, resolved bool, workers int) *BulkResult {
	if workers < 1 {
		workers = 4
	}
	res := &BulkResult{}
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				if err := c.flipResolve(ctx, id, resolved); err != nil {
					res.AddFailure(id, err)
				} else {
					res.AddSuccess(id)
				}
			}
		}()
	}
	for _, id := range ids {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
	return res
}

// ListThreads fetches review threads for a PR. resolvedStates should be one of
// ThreadStateAll, ThreadStateUnresolved, or ThreadStateResolved.
func (c *Client) ListThreads(ctx context.Context, owner, repo string, number int, resolvedStates string) ([]ReviewThread, error) {
	if c.gql == nil {
		return nil, fmt.Errorf("GraphQL client not available (auth required)")
	}
	const query = `
query($owner: String!, $name: String!, $number: Int!, $after: String, $states: [PullRequestReviewThreadState!]) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $after, resolvedStates: $states) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          path
          line
          startLine
          diffSide
          comments(first: 100) {
            nodes {
              id
              body
              createdAt
              author { __typename login }
            }
          }
        }
      }
    }
  }
}`
	var all []ReviewThread
	var cursor *string
	for {
		vars := map[string]interface{}{
			"owner":  owner,
			"name":   repo,
			"number": number,
			"after":  cursor,
		}
		if resolvedStates != "" {
			vars["states"] = []string{resolvedStates}
		} else {
			vars["states"] = nil
		}
		// go-gh's DoWithContext unmarshals the GraphQL "data" field directly
		// into the target — no outer wrapper needed (mirrors FetchForksGraphQL).
		var resp listThreadsData
		if err := c.gql.DoWithContext(ctx, query, vars, &resp); err != nil {
			return nil, fmt.Errorf("list threads: %w", err)
		}
		all = append(all, parseListThreadsResponse(resp)...)
		page := resp.Repository.PullRequest.ReviewThreads.PageInfo
		if !page.HasNextPage || page.EndCursor == nil {
			break
		}
		cursor = page.EndCursor
	}
	return all, nil
}
