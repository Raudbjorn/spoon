package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
)

const forksGraphQLQuery = `
query($owner: String!, $name: String!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    forks(first: 50, after: $cursor, orderBy: {field: STARGAZERS, direction: DESC}) {
      pageInfo { hasNextPage endCursor }
      nodes {
        databaseId
        nameWithOwner
        name
        description
        stargazerCount
        pushedAt
        isArchived
        isDisabled
        forkCount
        diskUsage
        primaryLanguage { name }
        defaultBranchRef { name }
        owner { login avatarUrl }
        pullRequests(states: OPEN, first: 1) { totalCount }
        releases(first: 1) { totalCount }
        refs(refPrefix: "refs/heads/", first: 10, orderBy: {field: ALPHABETICAL, direction: ASC}) {
          nodes {
            name
            target {
              ... on Commit { committedDate }
            }
          }
        }
      }
    }
  }
}
`

// gqlResponse maps the GraphQL JSON response.
type gqlResponse struct {
	Repository struct {
		Forks struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []gqlForkNode `json:"nodes"`
		} `json:"forks"`
	} `json:"repository"`
}

type gqlForkNode struct {
	DatabaseID      int64  `json:"databaseId"`
	NameWithOwner   string `json:"nameWithOwner"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	StargazerCount  int    `json:"stargazerCount"`
	PushedAt        string `json:"pushedAt"`
	IsArchived      bool   `json:"isArchived"`
	IsDisabled      bool   `json:"isDisabled"`
	ForkCount       int    `json:"forkCount"`
	DiskUsage       int    `json:"diskUsage"`
	PrimaryLanguage *struct {
		Name string `json:"name"`
	} `json:"primaryLanguage"`
	DefaultBranchRef *struct {
		Name string `json:"name"`
	} `json:"defaultBranchRef"`
	Owner struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatarUrl"`
	} `json:"owner"`
	PullRequests struct {
		TotalCount int `json:"totalCount"`
	} `json:"pullRequests"`
	Releases struct {
		TotalCount int `json:"totalCount"`
	} `json:"releases"`
	Refs struct {
		Nodes []gqlRefNode `json:"nodes"`
	} `json:"refs"`
}

type gqlRefNode struct {
	Name   string `json:"name"`
	Target struct {
		CommittedDate string `json:"committedDate"`
	} `json:"target"`
}

// FetchForksGraphQL fetches forks using the batched GraphQL query.
// Returns ForkInfo (REST-compatible) and T1Extra for each fork.
// onPage is called with each batch for progressive display.
func (c *Client) FetchForksGraphQL(ctx context.Context, owner, repo string, onPage func(forks []ForkInfo, extras []T1Extra, page int)) ([]ForkInfo, []T1Extra, error) {
	if c.gql == nil {
		return nil, nil, fmt.Errorf("GraphQL client not available")
	}

	var allForks []ForkInfo
	var allExtras []T1Extra
	var cursor *string
	page := 0

	for {
		variables := map[string]interface{}{
			"owner": owner,
			"name":  repo,
		}
		if cursor != nil {
			variables["cursor"] = *cursor
		}

		var resp gqlResponse
		err := c.doGraphQLWithRetry(ctx, forksGraphQLQuery, variables, &resp)
		if err != nil {
			return allForks, allExtras, fmt.Errorf("GraphQL query: %w", err)
		}

		page++
		var pageForks []ForkInfo
		var pageExtras []T1Extra

		for _, node := range resp.Repository.Forks.Nodes {
			fork, extra := gqlForkToForkInfo(node)
			pageForks = append(pageForks, fork)
			pageExtras = append(pageExtras, extra)
		}

		allForks = append(allForks, pageForks...)
		allExtras = append(allExtras, pageExtras...)

		if onPage != nil {
			onPage(pageForks, pageExtras, page)
		}

		if !resp.Repository.Forks.PageInfo.HasNextPage {
			break
		}
		endCursor := resp.Repository.Forks.PageInfo.EndCursor
		cursor = &endCursor
	}

	return allForks, allExtras, nil
}

// gqlMaxAttempts bounds retries for transient GraphQL failures. GitHub returns
// HTTP 502/503/504 intermittently for the expensive batched forks query when a
// repository has a very large fork network (tens of thousands of forks).
const gqlMaxAttempts = 3

// gqlRetryBackoff is the base linear backoff between GraphQL retry attempts.
const gqlRetryBackoff = 500 * time.Millisecond

// doGraphQLWithRetry runs a GraphQL query, retrying on transient server errors
// (HTTP 502/503/504) with linear backoff. Context cancellation aborts early.
// Non-transient errors (auth, rate limit, malformed query) are returned
// immediately without retrying.
func (c *Client) doGraphQLWithRetry(ctx context.Context, query string, variables map[string]interface{}, out interface{}) error {
	var err error
	for attempt := 1; attempt <= gqlMaxAttempts; attempt++ {
		err = c.gql.DoWithContext(ctx, query, variables, out)
		if err == nil || !isTransientServerError(err) {
			return err
		}
		if attempt == gqlMaxAttempts {
			break
		}
		// Debug, not Warn: in `spn forks list` stderr carries structured NDJSON
		// envelopes, and an unstructured warning here would interleave with them
		// and break machine consumers. The CLI layer surfaces a structured
		// warning if the run ultimately degrades.
		slog.Debug("forks: transient GraphQL error, retrying",
			"attempt", attempt, "max", gqlMaxAttempts, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * gqlRetryBackoff):
		}
	}
	return err
}

// isTransientServerError reports whether err is a retryable upstream failure
// (HTTP 502 Bad Gateway, 503 Service Unavailable, or 504 Gateway Timeout).
func isTransientServerError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *ghAPI.HTTPError
	if asHTTPError(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	// go-gh does not always surface the GraphQL HTTP status as a typed error;
	// fall back to matching the status code in the message. Match the "HTTP 50x"
	// prefix go-gh uses rather than a bare "50x" substring, which would false-
	// positive on port numbers, IDs, or timestamps that happen to contain those
	// digits.
	msg := err.Error()
	return strings.Contains(msg, "HTTP 502") ||
		strings.Contains(msg, "HTTP 503") ||
		strings.Contains(msg, "HTTP 504")
}

// gqlForkToForkInfo converts a GraphQL fork node to ForkInfo + T1Extra.
func gqlForkToForkInfo(node gqlForkNode) (ForkInfo, T1Extra) {
	defaultBranch := "main"
	if node.DefaultBranchRef != nil {
		defaultBranch = node.DefaultBranchRef.Name
	}

	language := ""
	if node.PrimaryLanguage != nil {
		language = node.PrimaryLanguage.Name
	}

	parts := strings.SplitN(node.NameWithOwner, "/", 2)
	htmlURL := "https://github.com/" + node.NameWithOwner

	fork := ForkInfo{
		ID:            node.DatabaseID,
		FullName:      node.NameWithOwner,
		Name:          node.Name,
		Description:   node.Description,
		DefaultBranch: defaultBranch,
		Stars:         node.StargazerCount,
		Forks:         node.ForkCount,
		Size:          node.DiskUsage,
		Language:      language,
		Archived:      node.IsArchived,
		Disabled:      node.IsDisabled,
		PushedAt:      node.PushedAt,
		HTMLURL:       htmlURL,
		Owner: OwnerInfo{
			Login:     node.Owner.Login,
			AvatarURL: node.Owner.AvatarURL,
		},
	}
	if len(parts) == 2 {
		fork.Owner.Login = parts[0]
	}

	// Sort branches by committedDate DESC, keep top 5 (excluding default)
	branches := sortBranches(node.Refs.Nodes, defaultBranch)

	extra := T1Extra{
		OpenPRCount:  node.PullRequests.TotalCount,
		ReleaseCount: node.Releases.TotalCount,
		TopBranches:  branches,
	}

	return fork, extra
}

// sortBranches sorts refs by committedDate descending and returns top 5 non-default branches.
func sortBranches(refs []gqlRefNode, defaultBranch string) []BranchInfo {
	// Filter out the default branch and refs without dates
	var branches []BranchInfo
	for _, ref := range refs {
		if ref.Name == defaultBranch {
			continue
		}
		if ref.Target.CommittedDate == "" {
			continue
		}
		branches = append(branches, BranchInfo{
			Name:         ref.Name,
			LastCommitAt: ref.Target.CommittedDate,
		})
	}

	// Sort by committedDate descending
	sort.Slice(branches, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, branches[i].LastCommitAt)
		tj, _ := time.Parse(time.RFC3339, branches[j].LastCommitAt)
		return ti.After(tj)
	})

	// Keep top 5
	if len(branches) > 5 {
		branches = branches[:5]
	}

	return branches
}

// FetchForksAuto uses GraphQL if authenticated, REST fallback otherwise.
// Returns forks and T1Extras (nil for REST path).
func (c *Client) FetchForksAuto(ctx context.Context, owner, repo string, onPage func(forks []ForkInfo, page int)) ([]ForkInfo, map[int64]T1Extra, error) {
	if c.HasGraphQL() {
		// Track fork IDs already streamed via the GraphQL onPage callback. If the
		// GraphQL query fails partway through (after emitting some pages) and we
		// fall back to REST below, REST restarts from page 1 and would otherwise
		// re-emit those same forks to onPage — duplicating them in the TUI/output.
		streamed := make(map[int64]struct{})
		forks, extras, err := c.FetchForksGraphQL(ctx, owner, repo, func(forks []ForkInfo, extras []T1Extra, page int) {
			if onPage != nil {
				for _, f := range forks {
					streamed[f.ID] = struct{}{}
				}
				onPage(forks, page)
			}
		})
		if err != nil {
			// The batched GraphQL query can still fail on very large fork
			// networks even after retries (GitHub 502/timeout). Fall back to the
			// REST forks endpoint so the user gets the fork list — just without
			// the T1 extras (open PRs, releases, top branches).
			//
			// Debug, not Warn: stderr is reserved for structured envelopes in the
			// agent-facing `spn` command; the CLI layer surfaces a structured
			// degraded-run warning instead.
			slog.Debug("forks: GraphQL failed, falling back to REST",
				"owner", owner, "repo", repo, "err", err)
			// Filter already-streamed forks out of the fallback's per-page
			// callback so callers don't see duplicates, while still returning the
			// complete REST fork list.
			dedupOnPage := onPage
			if onPage != nil && len(streamed) > 0 {
				dedupOnPage = func(forks []ForkInfo, page int) {
					fresh := forks[:0:0]
					for _, f := range forks {
						if _, seen := streamed[f.ID]; seen {
							continue
						}
						fresh = append(fresh, f)
					}
					if len(fresh) > 0 {
						onPage(fresh, page)
					}
				}
			}
			forks, restErr := c.FetchForks(ctx, owner, repo, dedupOnPage)
			if restErr != nil {
				return nil, nil, fmt.Errorf("graphql failed (%v); rest fallback failed: %w", err, restErr)
			}
			return forks, nil, nil
		}

		// Build extras map
		extrasMap := make(map[int64]T1Extra, len(extras))
		for i, f := range forks {
			if i < len(extras) {
				extrasMap[f.ID] = extras[i]
			}
		}
		return forks, extrasMap, nil
	}

	// REST fallback — no extras
	forks, err := c.FetchForks(ctx, owner, repo, onPage)
	return forks, nil, err
}

// ParseGitHubURL extracts owner/repo from a GitHub URL or owner/repo string.
func ParseGitHubURL(input string) (owner, repo string, err error) {
	input = strings.TrimSpace(input)
	input = strings.TrimSuffix(input, ".git")
	input = strings.TrimSuffix(input, "/")

	// Try owner/repo format
	if parts := strings.SplitN(input, "/", 3); len(parts) == 2 {
		return parts[0], parts[1], nil
	}

	// Try full URL
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "git@github.com:"} {
		if strings.HasPrefix(input, prefix) {
			rest := strings.TrimPrefix(input, prefix)
			parts := strings.SplitN(rest, "/", 3)
			if len(parts) >= 2 {
				return parts[0], parts[1], nil
			}
		}
	}

	return "", "", fmt.Errorf("cannot parse %q as owner/repo", input)
}

// FormatStars formats a star count with K/M suffixes.
func FormatStars(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 10_000 {
		return fmt.Sprintf("%.0fK", float64(n)/1_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return strconv.Itoa(n)
}
