package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// commitsListEntry is a single element of GET /repos/{o}/{r}/commits.
// Only the message is needed for the centrality keyword-frequency pass.
type commitsListEntry struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

// errStopPagination is a sentinel returned from GetPaginated callbacks to
// terminate pagination early without bubbling up as a real error.
var errStopPagination = errors.New("stop pagination")

// FetchRecentCommitMessages returns up to `limit` recent commit messages from
// the default branch of the given repository. Pagination uses per_page=100 and
// Link-header next pages, capped at limit.
//
// Returns (nil, nil) on 404 (empty / unknown repo). Caller can treat this as
// "no messages available".
func (c *Client) FetchRecentCommitMessages(ctx context.Context, owner, repo string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	perPage := 100
	if limit < perPage {
		perPage = limit
	}
	path := fmt.Sprintf("repos/%s/%s/commits?per_page=%d", owner, repo, perPage)

	out := make([]string, 0, limit)
	err := c.GetPaginated(ctx, path, func(raw json.RawMessage) error {
		var entries []commitsListEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			return fmt.Errorf("parsing commits page: %w", err)
		}
		for _, e := range entries {
			out = append(out, e.Commit.Message)
			if len(out) >= limit {
				return errStopPagination
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopPagination) {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}

// CommitSourceForRepo adapts a Client into a repo.CommitSource.
type CommitSourceForRepo struct {
	Client *Client
}

// CommitMessages implements repo.CommitSource.
func (s *CommitSourceForRepo) CommitMessages(ctx context.Context, owner, repo string, limit int) ([]string, error) {
	if s == nil || s.Client == nil {
		return nil, fmt.Errorf("nil CommitSourceForRepo")
	}
	return s.Client.FetchRecentCommitMessages(ctx, owner, repo, limit)
}
