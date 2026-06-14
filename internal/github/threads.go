package github

import (
	"context"
	"fmt"
	"sync"
)

// Thread state filters for FetchPR.
const (
	ThreadStateAll        = ""
	ThreadStateUnresolved = "UNRESOLVED"
	ThreadStateResolved   = "RESOLVED"
)

// ReviewThread is the public representation of a PR review thread.
type ReviewThread struct {
	ID            string          `json:"id"`
	IsResolved    bool            `json:"isResolved"`
	IsOutdated    bool            `json:"isOutdated"` // true if the line this thread anchors to has shifted since the thread was created
	Path          string          `json:"path"`
	Line          int             `json:"line"`
	StartLine     *int            `json:"startLine"`
	DiffSide      string          `json:"diffSide"`
	ReviewerType  string          `json:"reviewerType"`  // __typename of first comment's author
	ReviewerLogin string          `json:"reviewerLogin"` // login of first comment's author
	Comments      []ThreadComment `json:"comments"`
}

// ThreadComment is one comment on a review thread.
//
// Verbose-only fields (CreatedAt, UpdatedAt, AuthorURL) carry the JSON
// `omitempty` tag so the compact JSON output stays unchanged when callers clear
// them via threadsops.StripVerboseFields. The fields are always populated
// server-side; clearing them at the CLI layer is what gates verbose output.
type ThreadComment struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	AuthorType string `json:"authorType"`
	Body       string `json:"body"`
	CreatedAt  string `json:"createdAt,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
	AuthorURL  string `json:"authorUrl,omitempty"`
}

// PullRequestStatus carries the top-of-output mergeability summary.
// Empty-string fields mean "not applicable" (e.g. no checks configured).
type PullRequestStatus struct {
	Title             string `json:"title"`
	IsDraft           bool   `json:"isDraft"`
	Merged            bool   `json:"merged"`
	Mergeable         string `json:"mergeable"`        // MERGEABLE | CONFLICTING | UNKNOWN
	MergeStateStatus  string `json:"mergeStateStatus"` // CLEAN | BEHIND | DIRTY | BLOCKED | DRAFT | HAS_HOOKS | UNSTABLE | UNKNOWN
	ReviewDecision    string `json:"reviewDecision"`   // APPROVED | REVIEW_REQUIRED | CHANGES_REQUESTED | ""
	ChecksState       string `json:"checksState"`      // SUCCESS | FAILURE | PENDING | ERROR | EXPECTED | ""
	UnresolvedThreads int    `json:"unresolvedThreads"`
	OutdatedThreads   int    `json:"outdatedThreads"`   // count of threads whose anchor lines have shifted (across the whole PR)
	HeadSHA           string `json:"headSHA,omitempty"` // PR head commit SHA, used by code-context lookups
}

// listThreadsData mirrors the GraphQL response under data.
type listThreadsData struct {
	Repository struct {
		PullRequest struct {
			Title            string `json:"title"`
			IsDraft          bool   `json:"isDraft"`
			Merged           bool   `json:"merged"`
			Mergeable        string `json:"mergeable"`
			MergeStateStatus string `json:"mergeStateStatus"`
			ReviewDecision   string `json:"reviewDecision"`
			HeadRefOid       string `json:"headRefOid"`
			Commits          struct {
				Nodes []struct {
					Commit struct {
						StatusCheckRollup *struct {
							State string `json:"state"`
						} `json:"statusCheckRollup"`
					} `json:"commit"`
				} `json:"nodes"`
			} `json:"commits"`
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
	IsOutdated bool   `json:"isOutdated"`
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
	UpdatedAt string `json:"updatedAt"`
	Author    struct {
		Typename string `json:"__typename"`
		Login    string `json:"login"`
		URL      string `json:"url"`
	} `json:"author"`
}

func parseListThreadsResponse(data listThreadsData) []ReviewThread {
	nodes := data.Repository.PullRequest.ReviewThreads.Nodes
	out := make([]ReviewThread, 0, len(nodes))
	for _, n := range nodes {
		t := ReviewThread{
			ID:         n.ID,
			IsResolved: n.IsResolved,
			IsOutdated: n.IsOutdated,
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
				UpdatedAt:  c.UpdatedAt,
				AuthorURL:  c.Author.URL,
			})
		}
		if len(t.Comments) > 0 {
			t.ReviewerType = t.Comments[0].AuthorType
			t.ReviewerLogin = t.Comments[0].Author
		}
		out = append(out, t)
	}
	return out
}

// parseFetchPRResponse extracts both the PR status and the threads from a
// FetchPR response. UnresolvedThreads is computed from all threads (not
// affected by client-side state filtering).
func parseFetchPRResponse(data listThreadsData) (PullRequestStatus, []ReviewThread) {
	pr := data.Repository.PullRequest
	status := PullRequestStatus{
		Title:            pr.Title,
		IsDraft:          pr.IsDraft,
		Merged:           pr.Merged,
		Mergeable:        pr.Mergeable,
		MergeStateStatus: pr.MergeStateStatus,
		ReviewDecision:   pr.ReviewDecision,
		HeadSHA:          pr.HeadRefOid,
	}
	if len(pr.Commits.Nodes) > 0 && pr.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		status.ChecksState = pr.Commits.Nodes[0].Commit.StatusCheckRollup.State
	}
	threads := parseListThreadsResponse(data)
	for _, t := range threads {
		if !t.IsResolved {
			status.UnresolvedThreads++
		}
		if t.IsOutdated {
			status.OutdatedThreads++
		}
	}
	return status, threads
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
// comment with body, author, and createdAt populated.
func (c *Client) ReplyToThread(ctx context.Context, threadID, body string) (ThreadComment, error) {
	if c.gql == nil {
		return ThreadComment{}, fmt.Errorf("GraphQL client not available (auth required)")
	}
	const mutation = `
mutation($threadId: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: { pullRequestReviewThreadId: $threadId, body: $body }) {
    comment {
      id
      body
      createdAt
      updatedAt
      author { __typename login url }
    }
  }
}`
	// go-gh's DoWithContext unmarshals the GraphQL "data" field directly into
	// the target — no outer wrapper needed (mirrors FetchForksGraphQL /
	// FetchPR pattern).
	var resp struct {
		AddPullRequestReviewThreadReply struct {
			Comment struct {
				ID        string `json:"id"`
				Body      string `json:"body"`
				CreatedAt string `json:"createdAt"`
				UpdatedAt string `json:"updatedAt"`
				Author    struct {
					Typename string `json:"__typename"`
					Login    string `json:"login"`
					URL      string `json:"url"`
				} `json:"author"`
			} `json:"comment"`
		} `json:"addPullRequestReviewThreadReply"`
	}
	vars := map[string]interface{}{"threadId": threadID, "body": body}
	if err := c.gql.DoWithContext(ctx, mutation, vars, &resp); err != nil {
		return ThreadComment{}, fmt.Errorf("reply to thread %s: %w", threadID, err)
	}
	c2 := resp.AddPullRequestReviewThreadReply.Comment
	return ThreadComment{
		ID:         c2.ID,
		Body:       c2.Body,
		CreatedAt:  c2.CreatedAt,
		UpdatedAt:  c2.UpdatedAt,
		Author:     c2.Author.Login,
		AuthorType: c2.Author.Typename,
		AuthorURL:  c2.Author.URL,
	}, nil
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
	resp := struct{}{}
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
	_, threads, err := c.FetchPR(ctx, owner, repo, number, ThreadStateUnresolved)
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
	_, threads, err := c.FetchPR(ctx, owner, repo, number, ThreadStateResolved)
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

// FetchPR fetches the PR status and review threads in one GraphQL round trip.
// resolvedStates should be one of ThreadStateAll, ThreadStateUnresolved, or
// ThreadStateResolved (filtering is client-side; the server does not expose a
// resolvedStates filter on reviewThreads). UnresolvedThreads in the returned
// status is computed from ALL threads, not just the filtered subset.
func (c *Client) FetchPR(ctx context.Context, owner, repo string, number int, resolvedStates string) (PullRequestStatus, []ReviewThread, error) {
	if c.gql == nil {
		return PullRequestStatus{}, nil, fmt.Errorf("GraphQL client not available (auth required)")
	}
	const query = `
query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      title
      isDraft
      merged
      mergeable
      mergeStateStatus
      reviewDecision
      headRefOid
      commits(last: 1) {
        nodes {
          commit {
            statusCheckRollup { state }
          }
        }
      }
      reviewThreads(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          isOutdated
          path
          line
          startLine
          diffSide
          comments(first: 100) {
            nodes {
              id
              body
              createdAt
              updatedAt
              author { __typename login url }
            }
          }
        }
      }
    }
  }
}`
	var status PullRequestStatus
	var all []ReviewThread
	var cursor *string
	firstPage := true
	for {
		vars := map[string]interface{}{
			"owner":  owner,
			"name":   repo,
			"number": number,
			"after":  cursor,
		}
		var resp listThreadsData
		if err := c.gql.DoWithContext(ctx, query, vars, &resp); err != nil {
			return PullRequestStatus{}, nil, fmt.Errorf("fetch PR: %w", err)
		}
		pageStatus, pageThreads := parseFetchPRResponse(resp)
		if firstPage {
			status = pageStatus
			firstPage = false
		} else {
			// Accumulate per-page thread counts across pagination (other status
			// fields like Title/Mergeable are PR-level and stable across pages).
			status.UnresolvedThreads += pageStatus.UnresolvedThreads
			status.OutdatedThreads += pageStatus.OutdatedThreads
		}
		all = append(all, pageThreads...)
		page := resp.Repository.PullRequest.ReviewThreads.PageInfo
		if !page.HasNextPage || page.EndCursor == nil {
			break
		}
		cursor = page.EndCursor
	}
	// Apply client-side state filter (the server does not expose a thread-state filter).
	// Note: status.UnresolvedThreads was set by parseFetchPRResponse on the unfiltered set;
	// we do NOT overwrite it here.
	if resolvedStates == ThreadStateResolved {
		filtered := all[:0]
		for _, t := range all {
			if t.IsResolved {
				filtered = append(filtered, t)
			}
		}
		all = filtered
	} else if resolvedStates == ThreadStateUnresolved {
		filtered := all[:0]
		for _, t := range all {
			if !t.IsResolved {
				filtered = append(filtered, t)
			}
		}
		all = filtered
	}
	return status, all, nil
}
