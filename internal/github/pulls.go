package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// PullRequest is the minimal shape needed for the interactive picker. It
// mirrors the fields gh-pr-select / gh-pr-detect surface so the spoon
// picker can render rows identically.
type PullRequest struct {
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	Author     string    `json:"author"`     // login
	HeadBranch string    `json:"headBranch"` // ref name (no SHA)
	State      string    `json:"state"`      // "open" / "closed"
	URL        string    `json:"url"`        // html_url
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// pullRequestAPI is the on-the-wire shape from GET /repos/{owner}/{repo}/pulls.
// Only the fields we need are pulled out; the picker UI doesn't care about
// the rest.
type pullRequestAPI struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"html_url"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListOpenPRs returns up to `limit` open PRs in the given repo, ordered
// newest-updated first. Calls
//
//	GET /repos/{owner}/{repo}/pulls?state=open&sort=updated&direction=desc&per_page={limit}
//
// Default limit when limit <= 0 is 30 (matches gh-pr-select). Returns
// ([], nil) when the repo has no open PRs; returns nil + error on 404 / 403
// so the interactive picker surfaces the failure rather than presenting an
// empty list as if the repo were healthy.
func (c *Client) ListOpenPRs(ctx context.Context, owner, repo string, limit int) ([]PullRequest, error) {
	if limit <= 0 {
		limit = 30
	}
	path := fmt.Sprintf("repos/%s/%s/pulls?state=open&sort=updated&direction=desc&per_page=%d", owner, repo, limit)

	resp, err := c.GetRaw(ctx, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading pulls response: %w", err)
	}

	var raw []pullRequestAPI
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parsing pulls response: %w", err)
	}

	out := make([]PullRequest, 0, len(raw))
	for _, p := range raw {
		out = append(out, PullRequest{
			Number:     p.Number,
			Title:      p.Title,
			Author:     p.User.Login,
			HeadBranch: p.Head.Ref,
			State:      p.State,
			URL:        p.URL,
			CreatedAt:  p.CreatedAt,
			UpdatedAt:  p.UpdatedAt,
		})
	}
	return out, nil
}
