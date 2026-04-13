package github

import (
	"context"
	"encoding/json"
	"fmt"
)

// FetchParent fetches metadata for the parent repository.
func (c *Client) FetchParent(ctx context.Context, owner, repo string) (RepoInfo, error) {
	var info RepoInfo
	path := fmt.Sprintf("repos/%s/%s", owner, repo)
	err := c.Get(ctx, path, &info)
	return info, err
}

// FetchForks fetches all forks of a repository, paginated.
// onPage is called with each batch of forks and the current page number.
func (c *Client) FetchForks(ctx context.Context, owner, repo string, onPage func(forks []ForkInfo, page int)) ([]ForkInfo, error) {
	var all []ForkInfo
	page := 0
	path := fmt.Sprintf("repos/%s/%s/forks?sort=stargazers&per_page=100", owner, repo)

	err := c.GetPaginated(ctx, path, func(raw json.RawMessage) error {
		var forks []ForkInfo
		if err := json.Unmarshal(raw, &forks); err != nil {
			return fmt.Errorf("parsing forks page: %w", err)
		}
		page++
		all = append(all, forks...)
		if onPage != nil {
			onPage(forks, page)
		}
		return nil
	})

	return all, err
}
