// cmd/spn/repo_search.go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/config"
	gh "github.com/svnbjrn/spoon/internal/github"
)

// repoSearchUsage is the remediation for malformed invocations. It is spelled
// out because `spn repo search --help` is not a per-verb help route.
const repoSearchUsage = `Usage: spn repo search "<github-query>" [--limit 1-100] [--page N] [--sort best-match|stars|forks|updated] [--forge github]. See 'spn --help'.`

// repoSearchQueryRemediation points at GitHub's qualifier syntax, the usual
// cause of a 422.
const repoSearchQueryRemediation = "Fix the query's qualifier syntax (e.g. `language:Go stars:>100 topic:cli`); see https://docs.github.com/en/search-github/searching-on-github/searching-for-repositories"

// repoSearchSorts maps --sort values to the API's sort parameter. Best match is
// GitHub's default and is requested by sending no sort at all.
var repoSearchSorts = map[string]string{"best-match": "", "stars": "stars", "forks": "forks", "updated": "updated"}

// searchReposFn is indirected so tests can stub it without hitting GitHub.
var searchReposFn = func(ctx context.Context, c *gh.Client, query string, opts gh.RepoSearchOptions) (*gh.RepoSearchResult, error) {
	return c.SearchRepositories(ctx, query, opts)
}

// repoSearchArgs is a validated `repo search` invocation.
type repoSearchArgs struct {
	query   string
	perPage int
	page    int
	sort    string // API sort value; "" is best match
}

// parseRepoSearchArgs parses the hand-rolled flag grammar: one positional
// query and value-taking flags. Limits that belong to GitHub's search API are
// enforced by gh.ValidateRepoSearch in the caller, not repeated here.
func parseRepoSearchArgs(args []string) (repoSearchArgs, error) {
	out := repoSearchArgs{perPage: gh.RepoSearchDefaultPerPage, page: 1}
	haveQuery := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--limit":
			if i+1 >= len(args) {
				return out, errors.New("--limit requires a value")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return out, errors.New("--limit must be a positive integer")
			}
			out.perPage = n
		case "--page":
			if i+1 >= len(args) {
				return out, errors.New("--page requires a value")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return out, errors.New("--page must be a positive integer")
			}
			out.page = n
		case "--sort":
			if i+1 >= len(args) {
				return out, errors.New("--sort requires a value")
			}
			i++
			apiSort, ok := repoSearchSorts[strings.ToLower(args[i])]
			if !ok {
				return out, fmt.Errorf("unsupported --sort %q (want best-match, stars, forks or updated)", args[i])
			}
			out.sort = apiSort
		case "--forge":
			if i+1 >= len(args) {
				return out, errors.New("--forge requires a value")
			}
			i++
			if forge := strings.ToLower(args[i]); forge != "github" {
				return out, errors.New("repo search only supports GitHub; got --forge=" + forge)
			}
		default:
			if strings.HasPrefix(args[i], "--") {
				return out, errors.New("unknown flag: " + args[i])
			}
			if haveQuery {
				return out, errors.New("unexpected positional: " + args[i])
			}
			out.query, haveQuery = args[i], true
		}
	}
	if strings.TrimSpace(out.query) == "" {
		return out, errors.New("missing search query")
	}
	return out, nil
}

// repoSearchItemJSON is one candidate in the envelope. Field names follow spn's
// snake_case output rather than GitHub's wire names.
type repoSearchItemJSON struct {
	FullName    string   `json:"full_name"`
	URL         string   `json:"url"`
	Description string   `json:"description"`
	Language    string   `json:"language"`
	Stars       int      `json:"stars"`
	ForksCount  int      `json:"forks_count"`
	IsFork      bool     `json:"is_fork"`
	Archived    bool     `json:"archived"`
	PushedAt    string   `json:"pushed_at"`
	Topics      []string `json:"topics"`
}

// repoSearchEnvelope is the single JSON value `repo search` prints. NextPage is
// nil (JSON null) when no further page is reachable.
type repoSearchEnvelope struct {
	Query             string               `json:"query"`
	TotalCount        int                  `json:"total_count"`
	IncompleteResults bool                 `json:"incomplete_results"`
	Fetched           int                  `json:"fetched"`
	Page              int                  `json:"page"`
	NextPage          *int                 `json:"next_page"`
	Items             []repoSearchItemJSON `json:"items"`
}

// nextRepoSearchPage returns the page to request after page, or nil when there
// is none: either the reachable results (total_count, capped at GitHub's
// 1,000) end on this page, or the following page would reach past the cap and
// be rejected. It depends on the requested page size, never on how many items
// survived deduplication.
func nextRepoSearchPage(page, perPage, total int) *int {
	reachable := min(total, gh.RepoSearchMaxResults)
	if page*perPage >= reachable || (page+1)*perPage > gh.RepoSearchMaxResults {
		return nil
	}
	next := page + 1
	return &next
}

func newRepoSearchEnvelope(query string, page, perPage int, res *gh.RepoSearchResult) repoSearchEnvelope {
	// Non-nil slices throughout: an agent iterating .items[] or .topics[] must
	// never meet null.
	items := make([]repoSearchItemJSON, 0, len(res.Items))
	for _, it := range res.Items {
		topics := it.Topics
		if topics == nil {
			topics = []string{}
		}
		items = append(items, repoSearchItemJSON{
			FullName: it.FullName, URL: it.HTMLURL, Description: it.Description, Language: it.Language,
			Stars: it.Stars, ForksCount: it.Forks, IsFork: it.Fork, Archived: it.Archived,
			PushedAt: it.PushedAt, Topics: topics,
		})
	}
	return repoSearchEnvelope{
		Query: query, TotalCount: res.TotalCount, IncompleteResults: res.IncompleteResults,
		Fetched: len(items), Page: page, NextPage: nextRepoSearchPage(page, perPage, res.TotalCount), Items: items,
	}
}

// emitRepoSearchEmpty warns that the page held no items. It is not an error:
// the envelope still goes to stdout and the exit code stays 0, as with an empty
// `spn search`.
func emitRepoSearchEmpty(stderr io.Writer, page, total int) {
	message := "no repositories matched the query"
	remediation := "Broaden the query or drop qualifiers. Forks match only with fork:true or fork:only."
	if total > 0 {
		message = fmt.Sprintf("page %d has no items (total_count %d)", page, total)
		remediation = "Request an earlier page; next_page on the previous page names the last reachable one."
	}
	_ = writeDataNDJSON(stderr, map[string]any{"warning": map[string]any{
		"code": "no_results", "message": message, "remediation": remediation,
	}})
}

// emitRepoSearchError maps a failed search onto the agentio taxonomy. Only the
// caller's own mistakes are bad_input (exit 2, "do not retry"); a search 429 or
// 5xx is rate_limited or upstream_error, never bad_input.
func emitRepoSearchError(stderr io.Writer, err error) int {
	var rl *gh.RateLimitError
	if errors.As(err, &rl) {
		resetAt := rl.ResetAt.UTC().Format(time.RFC3339)
		secs := rl.RetryAfterSeconds()
		e := agentio.NewError(agentio.CodeRateLimited, err.Error(), agentio.RemediationRateLimited(resetAt, secs)).
			WithDetails(map[string]any{"reset_at": resetAt, "retry_after_seconds": secs, "remaining": rl.Remaining})
		if secs > 0 {
			e = e.WithRetryAfter(secs)
		}
		return emitDataError(stderr, e)
	}
	var rejected *gh.AllBackendsRejectedError
	if errors.As(err, &rejected) {
		// A permanent auth failure, not a rate limit: no reset window to wait for.
		return emitDataError(stderr, agentio.NewError(agentio.CodeAuthRequired, err.Error(), agentio.RemediationAuthRequired()))
	}
	if gh.IsQueryRejected(err) {
		return emitDataError(stderr, agentio.NewError(agentio.CodeBadInput, "GitHub rejected the search query: "+err.Error(), repoSearchQueryRemediation))
	}
	return emitDataError(stderr, agentio.NewError(agentio.CodeUpstream, err.Error(), agentio.RemediationUpstream()))
}

// doRepoSearch runs one bounded GitHub repository search and prints the
// envelope. It never pages on its own: the caller follows next_page.
func doRepoSearch(args []string, stdout, stderr io.Writer, effective config.EffectiveConfig) int {
	badInput := func(message string) int {
		return emitDataError(stderr, agentio.NewError(agentio.CodeBadInput, message, repoSearchUsage))
	}
	parsed, err := parseRepoSearchArgs(args)
	if err != nil {
		return badInput(err.Error())
	}
	opts := gh.RepoSearchOptions{Sort: parsed.sort, PerPage: parsed.perPage, Page: parsed.page}
	// Validated before auth: a typo should not need credentials to surface, and
	// the search window is too small to spend on a request that cannot succeed.
	if err := gh.ValidateRepoSearch(parsed.query, opts); err != nil {
		return badInput(err.Error())
	}

	client, _, err := repoCheckAuthWithEffective(effective)
	if err != nil {
		return emitDataError(stderr, agentio.NewError(agentio.CodeAuthRequired, "GitHub auth: "+err.Error(), agentio.RemediationAuthRequired()))
	}

	res, err := searchReposFn(context.Background(), client, parsed.query, opts)
	if err != nil {
		return emitRepoSearchError(stderr, err)
	}

	env := newRepoSearchEnvelope(parsed.query, parsed.page, parsed.perPage, res)
	if err := writeDataJSON(stdout, env); err != nil {
		return emitDataError(stderr, agentio.NewError(agentio.CodeInternal, "encode output: "+err.Error(), agentio.RemediationInternal()))
	}
	if env.Fetched == 0 {
		emitRepoSearchEmpty(stderr, parsed.page, res.TotalCount)
	}
	return 0
}
