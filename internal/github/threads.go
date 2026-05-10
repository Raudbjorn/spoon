package github

import (
	"context"
	"fmt"
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
