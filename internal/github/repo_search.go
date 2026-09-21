package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GitHub's repository-search limits. The search API serves at most the first
// RepoSearchMaxResults results of any query however it is paged, and a page
// that reaches past them is rejected with a 422.
const (
	RepoSearchMaxResults     = 1000
	RepoSearchMaxPerPage     = 100
	RepoSearchDefaultPerPage = 30
)

// ErrInvalidRepoSearch marks a search that repoSearchPath refused to build
// (empty query, out-of-range paging, unknown sort). No request was made.
var ErrInvalidRepoSearch = errors.New("invalid repository search")

// RepoSearchOptions selects one page of a repository search. Zero values mean
// "GitHub's best-match order", page 1 and RepoSearchDefaultPerPage.
type RepoSearchOptions struct {
	Sort    string // "", "stars", "forks" or "updated"; "" is best match
	PerPage int    // 1-RepoSearchMaxPerPage
	Page    int    // >= 1, with Page*PerPage <= RepoSearchMaxResults
}

// RepoSearchItem is one repository from a search page.
type RepoSearchItem struct {
	FullName    string   `json:"full_name"`
	HTMLURL     string   `json:"html_url"`
	Description string   `json:"description"`
	Language    string   `json:"language"`
	Stars       int      `json:"stargazers_count"`
	Forks       int      `json:"forks_count"`
	Fork        bool     `json:"fork"`
	Archived    bool     `json:"archived"`
	PushedAt    string   `json:"pushed_at"`
	Topics      []string `json:"topics"`
}

// RepoSearchResult is one search page with the envelope fields GitHub reports
// beside the items. TotalCount can exceed RepoSearchMaxResults; only the first
// RepoSearchMaxResults are reachable. IncompleteResults means GitHub timed out
// server-side, so the page may be missing matches.
type RepoSearchResult struct {
	TotalCount        int
	IncompleteResults bool
	Items             []RepoSearchItem
}

type repoSearchResponse struct {
	TotalCount        int              `json:"total_count"`
	IncompleteResults bool             `json:"incomplete_results"`
	Items             []RepoSearchItem `json:"items"`
}

// repoSearchPath builds the GitHub repository-search path for one page. The
// query is escaped and otherwise sent verbatim: whether forks match is the
// caller's to say with fork:true or fork:only, so nothing is appended here.
func repoSearchPath(query string, opts RepoSearchOptions) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("%w: empty query", ErrInvalidRepoSearch)
	}
	perPage, page := opts.PerPage, opts.Page
	if perPage == 0 {
		perPage = RepoSearchDefaultPerPage
	}
	if page == 0 {
		page = 1
	}
	if perPage < 1 || perPage > RepoSearchMaxPerPage {
		return "", fmt.Errorf("%w: per_page %d outside 1-%d", ErrInvalidRepoSearch, perPage, RepoSearchMaxPerPage)
	}
	if page < 1 {
		return "", fmt.Errorf("%w: page %d below 1", ErrInvalidRepoSearch, page)
	}
	if page*perPage > RepoSearchMaxResults {
		return "", fmt.Errorf("%w: page %d at per_page %d is past GitHub's %d-result search cap", ErrInvalidRepoSearch, page, perPage, RepoSearchMaxResults)
	}
	path := fmt.Sprintf("search/repositories?q=%s&per_page=%d&page=%d", url.QueryEscape(query), perPage, page)
	switch opts.Sort {
	case "":
	case "stars", "forks", "updated":
		path += fmt.Sprintf("&sort=%s&order=desc", opts.Sort)
	default:
		return "", fmt.Errorf("%w: unsupported sort %q", ErrInvalidRepoSearch, opts.Sort)
	}
	return path, nil
}

// ValidateRepoSearch reports whether SearchRepositories would accept these
// arguments, without making a request. It lets callers reject bad input before
// spending auth or a search-window slot, against the same limits the builder
// enforces. Every error it returns satisfies errors.Is(err, ErrInvalidRepoSearch).
func ValidateRepoSearch(query string, opts RepoSearchOptions) error {
	_, err := repoSearchPath(query, opts)
	return err
}

// SearchRepositories runs one GitHub repository-search request and returns the
// requested page. It costs one request against the search API's own rate window
// (30/min authenticated), separate from the core budget, and never pages on its
// own. Items are deduplicated by lowercased full_name, first occurrence kept;
// an item with no name cannot be chained into anything and is dropped.
// Rate limits surface as *RateLimitError and malformed queries satisfy
// IsQueryRejected, both through the %w wrapping.
func (c *Client) SearchRepositories(ctx context.Context, query string, opts RepoSearchOptions) (*RepoSearchResult, error) {
	if c == nil {
		return nil, errors.New("github client is nil")
	}
	path, err := repoSearchPath(query, opts)
	if err != nil {
		return nil, err
	}
	var resp repoSearchResponse
	if err := c.Get(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("repo search: %w", err)
	}
	items := make([]RepoSearchItem, 0, len(resp.Items))
	seen := make(map[string]struct{}, len(resp.Items))
	for i := range resp.Items {
		it := &resp.Items[i]
		key := strings.ToLower(it.FullName)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, *it)
	}
	return &RepoSearchResult{TotalCount: resp.TotalCount, IncompleteResults: resp.IncompleteResults, Items: items}, nil
}

// IsQueryRejected reports whether err is GitHub's 422 for a query it cannot
// run (bad qualifier syntax, a user: qualifier that cannot be searched, ...).
// Unlike isUnprocessableEntity it trusts only the typed status: the wrapped
// message embeds the request URL, so a text match on "422" would misread a 5xx
// for a query whose page or text happens to contain those digits.
func IsQueryRejected(err error) bool {
	return statusCode(err) == http.StatusUnprocessableEntity
}
