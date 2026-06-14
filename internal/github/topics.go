package github

import (
	"context"
	"fmt"
	"net/url"
)

// TopicRepo is one repository from a topic search.
type TopicRepo struct {
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Language    string `json:"language"`
	Stars       int    `json:"stargazers_count"`
	Forks       int    `json:"forks_count"`
	PushedAt    string `json:"pushed_at"`
	Archived    bool   `json:"archived"`
	IsFork      bool   `json:"fork"`
}

type topicSearchResponse struct {
	TotalCount int         `json:"total_count"`
	Items      []TopicRepo `json:"items"`
}

// SearchTopicRepos returns up to limit repositories carrying the given
// GitHub topic (github.com/topics/<topic>), ordered by stars. Forks are
// excluded server-side — topic mode prospects the fork networks of original
// repos. Note the search API has its own rate window (30 req/min
// authenticated); one call here is one search request.
func (c *Client) SearchTopicRepos(ctx context.Context, topic string, limit int) ([]TopicRepo, error) {
	if topic == "" {
		return nil, fmt.Errorf("empty topic")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	q := url.QueryEscape(fmt.Sprintf("topic:%s fork:false", topic))
	path := fmt.Sprintf("search/repositories?q=%s&sort=stars&order=desc&per_page=%d", q, limit)
	var resp topicSearchResponse
	if err := c.Get(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("topic search %q: %w", topic, err)
	}
	return resp.Items, nil
}
