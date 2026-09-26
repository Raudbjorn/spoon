package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
	"github.com/svnbjrn/spoon/internal/forge"
)

// defaultBranchTipQuery is a one-off GraphQL query used to fetch the
// upstream default branch's tip SHA. Used by FetchParent to populate
// ParentData.HeadSHA so the MDG cache (cluster.PipelineOptions) can pin
// entries against the right revision. Authentication is required for
// private repos; for public ones the unauthenticated path is still allowed.
const defaultBranchTipQuery = `query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    defaultBranchRef { target { oid } }
  }
  rateLimit { limit remaining used resetAt cost }
}`

// defaultBranchTipSHA returns the upstream default-branch tip SHA using
// the GraphQL client. Returns the empty string and no error when the
// repository has no default branch (e.g., empty repo) — the caller treats
// that as a soft miss and continues without pinning. Other errors
// (network, 5xx) are propagated.
type defaultBranchTipResponse struct {
	Repository struct {
		DefaultBranchRef *struct {
			Target struct {
				OID string `json:"oid"`
			} `json:"target"`
		} `json:"defaultBranchRef"`
	} `json:"repository"`
	RateLimit gqlRateLimit `json:"rateLimit"`
}

func (r *defaultBranchTipResponse) graphqlRateLimit() *gqlRateLimit { return &r.RateLimit }

func (c *Client) defaultBranchTipSHA(ctx context.Context, owner, repo string) (string, error) {
	if !c.HasGraphQL() {
		return "", nil
	}
	var resp defaultBranchTipResponse
	if err := c.doGraphQLWithRetry(ctx, defaultBranchTipQuery, map[string]interface{}{
		"owner": owner,
		"name":  repo,
	}, &resp); err != nil {
		return "", err
	}
	if resp.Repository.DefaultBranchRef == nil {
		return "", nil
	}
	return resp.Repository.DefaultBranchRef.Target.OID, nil
}

// forksGraphQLQuery pages by CREATED_AT, not STARGAZERS: most forks tie at 0
// stars and cursor paging over a heavily tied key is unstable (pages overlap
// and others are skipped). See fetchForksREST for the measured impact.
const forksGraphQLQuery = `
query($owner: String!, $name: String!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    forkCount
    forks(first: 50, after: $cursor, orderBy: {field: CREATED_AT, direction: ASC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes {
        databaseId
        nameWithOwner
        name
        description
        stargazerCount
        pushedAt
        createdAt
        isArchived
        isDisabled
        forkCount
        diskUsage
        primaryLanguage { name }
        defaultBranchRef { name target { ... on Commit { oid committedDate } } }
        owner { login avatarUrl }
        pullRequests(states: OPEN, first: 1) { totalCount }
        releases(first: 1) { totalCount }
        repositoryTopics(first: 20) { nodes { topic { name } } }
        refs(refPrefix: "refs/heads/", first: 10, orderBy: {field: ALPHABETICAL, direction: ASC}) {
          nodes {
            name
            target {
              ... on Commit { oid committedDate }
            }
          }
        }
        parent { nameWithOwner databaseId }
      }
    }
  }
  rateLimit { limit remaining used resetAt cost }
}
`

// gqlResponse maps the GraphQL JSON response.
type gqlResponse struct {
	Repository struct {
		ForkCount int `json:"forkCount"`
		Forks     struct {
			TotalCount int `json:"totalCount"`
			PageInfo   struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []gqlForkNode `json:"nodes"`
		} `json:"forks"`
	} `json:"repository"`
	RateLimit gqlRateLimit `json:"rateLimit"`
}

func (r *gqlResponse) graphqlRateLimit() *gqlRateLimit { return &r.RateLimit }

type gqlForkNode struct {
	DatabaseID      int64  `json:"databaseId"`
	NameWithOwner   string `json:"nameWithOwner"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	StargazerCount  int    `json:"stargazerCount"`
	PushedAt        string `json:"pushedAt"`
	CreatedAt       string `json:"createdAt"`
	IsArchived      bool   `json:"isArchived"`
	IsDisabled      bool   `json:"isDisabled"`
	ForkCount       int    `json:"forkCount"`
	DiskUsage       int    `json:"diskUsage"`
	PrimaryLanguage *struct {
		Name string `json:"name"`
	} `json:"primaryLanguage"`
	DefaultBranchRef *struct {
		Name   string `json:"name"`
		Target struct {
			OID           string `json:"oid"`
			CommittedDate string `json:"committedDate"`
		} `json:"target"`
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
	RepositoryTopics struct {
		Nodes []struct {
			Topic struct {
				Name string `json:"name"`
			} `json:"topic"`
		} `json:"nodes"`
	} `json:"repositoryTopics"`
	Refs struct {
		Nodes []gqlRefNode `json:"nodes"`
	} `json:"refs"`

	// Parent is the fork's direct parent repository. Populated via the
	// forks query's parent { nameWithOwner databaseId } extension. Nil
	// when the parent field is absent (legacy response, REST path, or
	// the root repo itself).
	Parent *struct {
		NameWithOwner string `json:"nameWithOwner"`
		DatabaseID    int64  `json:"databaseId"`
	} `json:"parent"`
}

type gqlRefNode struct {
	Name   string `json:"name"`
	Target struct {
		OID           string `json:"oid"`
		CommittedDate string `json:"committedDate"`
	} `json:"target"`
}

// FetchForksGraphQL fetches forks using the batched GraphQL query.
// Returns ForkInfo (REST-compatible), T1Extra for each fork, and an
// AcquisitionReport describing the GraphQL acquisition. The repoForkCount and
// directTotalCount in the report are captured from the very first page so they
// remain populated even if a later page fails and we partial-fall back to REST.
// onPage is called with each batch for progressive display.
func (c *Client) FetchForksGraphQL(ctx context.Context, owner, repo string, onPage func(forks []ForkInfo, extras []T1Extra, page int)) ([]ForkInfo, []T1Extra, *forge.AcquisitionReport, error) {
	if !c.HasGraphQL() {
		return nil, nil, nil, fmt.Errorf("GraphQL client not available")
	}

	authMode := "authenticated"
	if !c.IsAuthenticated() {
		authMode = "anonymous"
	}

	var allForks []ForkInfo
	var allExtras []T1Extra
	var cursor *string
	page := 0
	firstResponse := true
	var repoForkCount, directTotalCount int
	seen := make(map[int64]struct{}, 256)
	rawRows := 0

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
			report := &forge.AcquisitionReport{
				Method:        "graphql",
				Scope:         "direct",
				APIVersion:    defaultRESTVersion,
				AuthMode:      authMode,
				FallbackChain: []string{"graphql"},
				Pages:         page,
				RawRows:       rawRows,
				UniqueRows:    len(seen),
				DuplicateRows: rawRows - len(seen),
				ExpectedRows:  directTotalCount,
				CaptureAt:     time.Now(),
				AuthScopeID:   c.AuthScopeID(),
			}
			// Annotate lineage on the partial data we have so far before returning.
			// DirectParent/DepthFromRoot are unknown when parent data is missing.
			allExtras = annotateDepths(allForks, allExtras, owner+"/"+repo)
			sortForksByStars(allForks, allExtras)
			return allForks, allExtras, report, fmt.Errorf("GraphQL query: %w", err)
		}

		// Capture root repo metadata from the first response so a later failure
		// can still produce a report with the true totals. Page counts represent
		// non-empty fork batches, not merely successful HTTP responses.
		if firstResponse {
			repoForkCount = resp.Repository.ForkCount
			directTotalCount = resp.Repository.Forks.TotalCount
			firstResponse = false
		}

		var pageForks []ForkInfo
		var pageExtras []T1Extra

		for _, node := range resp.Repository.Forks.Nodes {
			fork, extra := gqlForkToForkInfo(node, repoForkCount, directTotalCount, authMode, defaultRESTVersion)
			rawRows++
			// Cursor paging can still repeat a row (e.g. a fork created or
			// deleted mid-walk); every consumer downstream assumes one row
			// per fork, so a repeat is counted and dropped here.
			if _, dup := seen[fork.ID]; dup {
				continue
			}
			seen[fork.ID] = struct{}{}
			pageForks = append(pageForks, fork)
			pageExtras = append(pageExtras, extra)
		}

		allForks = append(allForks, pageForks...)
		allExtras = append(allExtras, pageExtras...)

		// Pages counts non-empty upstream batches, so a page whose rows were
		// all repeats of earlier pages still counts; only the callback is
		// skipped when nothing on the page is new.
		if len(resp.Repository.Forks.Nodes) > 0 {
			page++
			if onPage != nil && len(pageForks) > 0 {
				onPage(pageForks, pageExtras, page)
			}
		}

		if !resp.Repository.Forks.PageInfo.HasNextPage {
			break
		}
		endCursor := resp.Repository.Forks.PageInfo.EndCursor
		cursor = &endCursor
	}

	unique := len(seen)
	report := &forge.AcquisitionReport{
		Method:        "graphql",
		Scope:         "direct",
		APIVersion:    defaultRESTVersion,
		AuthMode:      authMode,
		FallbackChain: []string{"graphql"},
		Pages:         page,
		RawRows:       rawRows,
		UniqueRows:    unique,
		DuplicateRows: rawRows - unique,
		ExpectedRows:  directTotalCount,
		CaptureAt:     time.Now(),
		AuthScopeID:   c.AuthScopeID(),
	}
	// Annotate lineage on the full set. annotateDepths is cycle-safe and
	// deterministic regardless of page order.
	allExtras = annotateDepths(allForks, allExtras, owner+"/"+repo)
	sortForksByStars(allForks, allExtras)

	return allForks, allExtras, report, nil
}

// BoundedOptions controls a bounded whole-network traversal.
type BoundedOptions struct {
	MaxNodes   int
	MaxDepth   int
	MaxPages   int
	MaxElapsed time.Duration
}

type CapReason int

const (
	CapReasonNone     CapReason = 0
	CapReasonMaxNodes CapReason = iota
	CapReasonMaxDepth
	CapReasonMaxPages
	CapReasonMaxElapsed
)

func (c CapReason) String() string {
	switch c {
	case CapReasonMaxNodes:
		return "max_nodes"
	case CapReasonMaxDepth:
		return "max_depth"
	case CapReasonMaxPages:
		return "max_pages"
	case CapReasonMaxElapsed:
		return "max_elapsed"
	default:
		return ""
	}
}

// capReason returns the report's CapReason, preferring the explicit outer
// cap when set and falling back to the depth-boundary flag. depthCap fires
// inside the fork-processing loop while the queue may still hold shallower
// entries, so it must NOT terminate the outer loop (that would truncate the
// network prematurely); it is surfaced here for the report only.
func capReason(outer CapReason, depthCap bool) string {
	if outer != CapReasonNone {
		return outer.String()
	}
	if depthCap {
		return CapReasonMaxDepth.String()
	}
	return ""
}

func (c *Client) FetchForksBounded(ctx context.Context, owner, repo string, onBatch func(forks []ForkInfo, extras []T1Extra, depth int), opts BoundedOptions) ([]ForkInfo, map[int64]T1Extra, *forge.AcquisitionReport, error) {
	if !c.HasGraphQL() {
		return nil, nil, nil, fmt.Errorf("GraphQL client not available")
	}
	if opts.MaxNodes <= 0 {
		opts.MaxNodes = 5000
	}
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 3
	}
	if opts.MaxPages <= 0 {
		opts.MaxPages = 200
	}
	if opts.MaxElapsed <= 0 {
		opts.MaxElapsed = 2 * time.Minute
	}
	authMode := "authenticated"
	if !c.IsAuthenticated() {
		authMode = "anonymous"
	}

	type queueEntry struct {
		owner string
		repo  string
		depth int
	}
	queue := []queueEntry{{owner: owner, repo: repo}}
	seen := make(map[int64]struct{})
	var allForks []ForkInfo
	var allExtras []T1Extra
	totalPages := 0
	cap := CapReasonNone
	// depthCap records that the traversal refused to queue children of a
	// fork at the MaxDepth boundary. It is independent of `cap` so the
	// outer loop can continue draining shallower queue entries instead of
	// terminating after the current batch.
	var depthCap bool
	rootForkCount := 0
	// rootDirectTotal is the root repo's forks.totalCount (direct child count),
	// captured from the depth-zero alias's first response. It is the value
	// every fork's T1Extra.DirectTotalCount must carry, independent of how
	// many forks the traversal actually discovered (which can be capped by
	// MaxNodes / MaxPages / MaxDepth / MaxElapsed).
	rootDirectTotal := 0
	start := time.Now()
	// deadline is the absolute wall-clock instant at which MaxElapsed is hit.
	// The outer loop and every in-flight GraphQL call derive their per-call
	// context from this so a slow request (with retries) cannot blow past
	// the user's MaxElapsed.
	deadline := start.Add(opts.MaxElapsed)

	makeReport := func(errCode string) *forge.AcquisitionReport {
		unique := len(seen)
		return &forge.AcquisitionReport{
			Method:        "graphql",
			Scope:         "all",
			APIVersion:    defaultRESTVersion,
			AuthMode:      authMode,
			FallbackChain: []string{"graphql"},
			Pages:         totalPages,
			RawRows:       len(allForks),
			UniqueRows:    unique,
			DuplicateRows: len(allForks) - unique,
			CaptureAt:     time.Now(),
			AuthScopeID:   c.AuthScopeID(),
			VisitedNodes:  unique,
			MaxNodes:      opts.MaxNodes,
			MaxDepth:      opts.MaxDepth,
			CapReason:     capReason(cap, depthCap),
			Unresolved:    max(rootForkCount-unique, 0),
			Error:         errCode,
		}
	}

	for len(queue) > 0 && cap == CapReasonNone {
		if time.Since(start) > opts.MaxElapsed {
			cap = CapReasonMaxElapsed
			break
		}
		if totalPages >= opts.MaxPages {
			cap = CapReasonMaxPages
			break
		}

		batch := queue
		if len(batch) > 4 {
			batch = batch[:4]
		}
		queue = queue[len(batch):]
		totalPages++

		aliases := make([]string, len(batch))
		vars := make(map[string]interface{})
		for j, e := range batch {
			aliases[j] = fmt.Sprintf("r%d", j)
			vars[fmt.Sprintf("owner%d", j)] = e.owner
			vars[fmt.Sprintf("name%d", j)] = e.repo
		}

		query := buildBatchedForksQuery(aliases)
		var resp gqlAliasBatch
		// Pass a context bounded by the traversal deadline so an in-flight
		// GraphQL request that times out (and retries) cannot run past
		// MaxElapsed. Without this, a 60-second HTTP timeout retried up to
		// three times can blow a 30-second user limit by ~3x.
		callCtx, cancel := context.WithDeadline(ctx, deadline)
		callErr := c.doGraphQLWithRetry(callCtx, query, vars, &resp)
		cancel()
		if callErr != nil {
			// The bounded call bailed mid-walk. Build the same extras map
			// the happy path produces so callers that iterate the partial
			// forks can still recover lineage and coverage. Distinguish a
			// user-configured time limit (deadline expired mid-request) from
			// an upstream GraphQL outage: the former is a cap, not an error.
			allExtras = annotateDepths(allForks, allExtras, owner+"/"+repo)
			for i := range allExtras {
				allExtras[i].WholeNetworkForkCount = rootForkCount
				allExtras[i].DirectTotalCount = rootDirectTotal
			}
			extrasMap := make(map[int64]T1Extra, len(allForks))
			for i, f := range allForks {
				if i < len(allExtras) {
					extrasMap[f.ID] = allExtras[i]
				}
			}
			if errors.Is(callErr, context.DeadlineExceeded) {
				cap = CapReasonMaxElapsed
				return allForks, extrasMap, makeReport(""), callErr
			}
			return allForks, extrasMap, makeReport("graphql_failed"), fmt.Errorf("graphql query: %w", callErr)
		}

		for j, alias := range aliases {
			raw, ok := resp.Aliases[alias]
			if !ok {
				continue
			}
			var node struct {
				ForkCount int `json:"forkCount"`
				Forks     struct {
					TotalCount int           `json:"totalCount"`
					Nodes      []gqlForkNode `json:"nodes"`
				} `json:"forks"`
			}
			if err := json.Unmarshal(raw, &node); err != nil {
				continue
			}
			depth := batch[j].depth
			if depth == 0 {
				rootForkCount = node.ForkCount
				rootDirectTotal = node.Forks.TotalCount
			}
			var pageForks []ForkInfo
			var pageExtras []T1Extra
			for _, f := range node.Forks.Nodes {
				if _, duplicate := seen[f.DatabaseID]; duplicate {
					continue
				}
				if len(seen) >= opts.MaxNodes {
					cap = CapReasonMaxNodes
					continue
				}
				fork, extra := gqlForkToForkInfo(f, 0, 0, authMode, defaultRESTVersion)
				seen[fork.ID] = struct{}{}
				allForks = append(allForks, fork)
				allExtras = append(allExtras, extra)
				pageForks = append(pageForks, fork)
				pageExtras = append(pageExtras, extra)
				if depth+1 < opts.MaxDepth {
					if f.ForkCount > 0 {
						parts := strings.SplitN(fork.FullName, "/", 2)
						if len(parts) == 2 {
							queue = append(queue, queueEntry{owner: parts[0], repo: parts[1], depth: depth + 1})
						}
					}
				} else if f.ForkCount > 0 {
					// depth+1 >= opts.MaxDepth: do not queue this fork's children,
					// but DO NOT terminate the outer loop. The queue may still
					// hold entries at shallower depths whose descendants are
					// within MaxDepth. Flag the depth truncation in the report
					// without stopping work that is already enqueued.
					depthCap = true
				}
			}
			if onBatch != nil && len(pageForks) > 0 {
				onBatch(pageForks, pageExtras, depth+1)
			}
		}
	}

	report := makeReport("")
	allExtras = annotateDepths(allForks, allExtras, owner+"/"+repo)
	for i := range allExtras {
		// WholeNetworkForkCount is the root repo's forkCount (GraphQL
		// repository.forkCount) -- the whole-network total. DirectTotalCount
		// is the root repo's forks.totalCount -- the number of direct children.
		// They are independent GraphQL fields and must not be conflated.
		allExtras[i].WholeNetworkForkCount = rootForkCount
		allExtras[i].DirectTotalCount = rootDirectTotal
	}
	extrasMap := make(map[int64]T1Extra, len(allForks))
	for i, f := range allForks {
		if i < len(allExtras) {
			extrasMap[f.ID] = allExtras[i]
		}
	}
	return allForks, extrasMap, report, nil
}

func buildBatchedForksQuery(aliases []string) string {
	var sb strings.Builder
	sb.WriteString("query(")
	args := make([]string, 0, len(aliases)*2)
	for i := range aliases {
		args = append(args, fmt.Sprintf("$owner%d: String!", i), fmt.Sprintf("$name%d: String!", i))
	}
	sb.WriteString(strings.Join(args, ", "))
	sb.WriteString(`) {`)
	forkFrag := `forkCount forks(first:50)` +
		`{totalCount nodes{databaseId nameWithOwner name description stargazerCount` +
		` pushedAt createdAt isArchived isDisabled forkCount diskUsage` +
		` primaryLanguage{name} defaultBranchRef{name target{... on Commit{oid committedDate}}} owner{login avatarUrl}` +
		` pullRequests(states:OPEN,first:1){totalCount}` +
		` releases(first:1){totalCount}` +
		` repositoryTopics(first:20){nodes{topic{name}}}` +
		` refs(refPrefix:"refs/heads/",first:10,orderBy:{field:ALPHABETICAL,direction:ASC})` +
		`{nodes{name target{... on Commit{oid committedDate}}}}` +
		` parent{nameWithOwner databaseId}}}`
	for i := range aliases {
		sb.WriteString(fmt.Sprintf(` r%d: repository(owner: $owner%d, name: $name%d) {%s}`, i, i, i, forkFrag))
	}
	sb.WriteString(` rateLimit{limit remaining used resetAt cost}}`)
	return sb.String()
}

// gqlMaxAttempts bounds retries for transient GraphQL failures. GitHub returns
// HTTP 502/503/504 intermittently for the expensive batched forks query when a
// repository has a very large fork network (tens of thousands of forks).
const gqlMaxAttempts = 3

// gqlRetryBackoff is the base linear backoff between GraphQL retry attempts.
const gqlRetryBackoff = time.Second

// doGraphQLWithRetry runs a GraphQL query, retrying on transient server errors
// (HTTP 502/503/504) with linear backoff. Context cancellation aborts early.
// Non-transient errors (auth, rate limit, malformed query) are returned
// immediately without retrying.
func (c *Client) doGraphQLWithRetry(ctx context.Context, query string, variables map[string]interface{}, out interface{}) error {
	var err error
	for attempt := range gqlMaxAttempts {
		err = c.doGraphQL(ctx, query, variables, out)
		if err == nil || !isTransientServerError(err) {
			return err
		}
		if attempt == gqlMaxAttempts-1 {
			break
		}
		// Debug, not Warn: in `spn forks list` stderr carries structured NDJSON
		// envelopes, and an unstructured warning here would interleave with them
		// and break machine consumers. The CLI layer surfaces a structured
		// warning if the run ultimately degrades.
		slog.Debug("forks: transient GraphQL error, retrying",
			"attempt", attempt+1, "max", gqlMaxAttempts, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * gqlRetryBackoff):
		}
	}
	return err
}

// extrasByID keys GraphQL T1 extras by fork ID. extras is parallel to forks;
// nil when there are none.
func extrasByID(forks []ForkInfo, extras []T1Extra) map[int64]T1Extra {
	if len(extras) == 0 {
		return nil
	}
	m := make(map[int64]T1Extra, len(extras))
	for i, f := range forks {
		if i < len(extras) {
			m[f.ID] = extras[i]
		}
	}
	return m
}

// isTransientServerError reports whether err is a retryable upstream failure
// (HTTP 502 Bad Gateway, 503 Service Unavailable, or 504 Gateway Timeout).
func isTransientServerError(err error) bool {
	if err == nil {
		return false
	}
	// A response body cut off mid-JSON is a server/transport hiccup, not a
	// bad query: the same request succeeds on retry. Observed 2026-09-26 on
	// ggml-org/llama.cpp page 371 of the fork listing, between runs of
	// 502/504s; treating it as fatal aborted a 65-minute GraphQL walk.
	if errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "unexpected end of JSON input") {
		return true
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
// repoForkCount and directTotalCount are taken from the parent repository
// root (forkCount and forks.totalCount respectively); they are identical
// for every fork in a single run. authMode and apiVersion describe the
// acquisition that produced this record.
func gqlForkToForkInfo(node gqlForkNode, repoForkCount, directTotalCount int, authMode, apiVersion string) (ForkInfo, T1Extra) {
	defaultBranch := "main"
	defaultTipSHA := ""
	if node.DefaultBranchRef != nil {
		defaultBranch = node.DefaultBranchRef.Name
		defaultTipSHA = node.DefaultBranchRef.Target.OID
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
		CreatedAt:     node.CreatedAt,
		HTMLURL:       htmlURL,
		Fork:          true,
		Topics:        extractTopicNames(node.RepositoryTopics.Nodes),
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
		OpenPRCount:           node.PullRequests.TotalCount,
		ReleaseCount:          node.Releases.TotalCount,
		TopBranches:           branches,
		ForkCount:             node.ForkCount,
		DirectTotalCount:      directTotalCount,
		WholeNetworkForkCount: repoForkCount,
		AuthMode:              authMode,
		APIVersion:            apiVersion,
		DefaultTipSHA:         defaultTipSHA,
	}

	if node.Parent != nil {
		extra.ParentFullPath = node.Parent.NameWithOwner
		extra.ParentDatabaseID = node.Parent.DatabaseID
	}

	return fork, extra
}

// annotateDepths walks the parallel (forks, extras) slices and computes
// DepthFromRoot and DirectParent relative to the requested network root.
// It builds a parent-to-children index, then breadth-first propagates depth from
// direct children. Cycles and missing parents are never reached, so stay at the
// zero value (unknown).
func annotateDepths(forks []ForkInfo, extras []T1Extra, root string) []T1Extra {
	if len(forks) == 0 {
		return nil
	}
	if len(forks) != len(extras) {
		return extras
	}

	children := make(map[int64][]int, len(forks))
	queue := make([]int, 0, len(forks))
	for i := range extras {
		if strings.EqualFold(extras[i].ParentFullPath, root) {
			extras[i].DirectParent = 1
			extras[i].DepthFromRoot = 1
			queue = append(queue, i)
		}
		if extras[i].ParentDatabaseID != 0 {
			children[extras[i].ParentDatabaseID] = append(children[extras[i].ParentDatabaseID], i)
		}
	}

	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		if forks[i].ID == 0 {
			continue
		}
		for _, child := range children[forks[i].ID] {
			candidate := extras[i].DepthFromRoot + 1
			if extras[child].DepthFromRoot == 0 || extras[child].DepthFromRoot > candidate {
				extras[child].DepthFromRoot = candidate
				queue = append(queue, child)
			}
		}
	}
	return extras
}

func extractTopicNames(nodes []struct {
	Topic struct {
		Name string `json:"name"`
	} `json:"topic"`
}) []string {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.Topic.Name != "" {
			out = append(out, n.Topic.Name)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
			TipSHA:       ref.Target.OID,
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
// Returns forks, T1Extras (nil for REST path), and an AcquisitionReport.
// Successful reports count raw identities and non-empty upstream batches. Pure
// REST uses Method="rest"/["rest"]; a successful partial fallback uses
// Method="graphql+rest"/["graphql","rest"]. If that REST fallback fails, the
// returned report remains the partial GraphQL snapshot with
// Method="graphql"/["graphql"] and Error="rest_fallback_failed".
func (c *Client) FetchForksAuto(ctx context.Context, owner, repo string, onPage func(forks []ForkInfo, page int)) ([]ForkInfo, map[int64]T1Extra, *forge.AcquisitionReport, error) {
	authMode := "authenticated"
	if !c.IsAuthenticated() {
		authMode = "anonymous"
	}

	if c.HasGraphQL() {
		forks, extras, gqlReport, err := c.FetchForksGraphQL(ctx, owner, repo, func(forks []ForkInfo, extras []T1Extra, page int) {
			if onPage != nil {
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
			// FetchForksGraphQL returns the forks it accumulated before failing —
			// the same set it streamed via onPage above. Build the already-streamed
			// ID set from that partial result (only here, on the failure path, so
			// the common success path pays nothing) and filter those IDs out of the
			// REST fallback's per-page callback so callers don't see duplicates,
			// while still returning the complete REST fork list.
			streamed := make(map[int64]struct{}, len(forks))
			for _, f := range forks {
				streamed[f.ID] = struct{}{}
			}
			dedupOnPage := onPage
			if onPage != nil && len(streamed) > 0 {
				dedupOnPage = func(forks []ForkInfo, page int) {
					fresh := make([]ForkInfo, 0, len(forks))
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
			restOnPage := func(pageForks []ForkInfo, page int) {
				if dedupOnPage != nil {
					dedupOnPage(pageForks, page)
				}
			}
			restForks, restStats, restErr := c.fetchForksREST(ctx, owner, repo, restOnPage)
			restPages, restRawRows := restStats.pages, restStats.raw
			if restErr != nil {
				// REST fallback failed too: retain the partial GraphQL snapshot.
				if gqlReport == nil {
					gqlReport = &forge.AcquisitionReport{Method: "graphql", Scope: "direct", APIVersion: defaultRESTVersion, AuthMode: authMode, FallbackChain: []string{"graphql"}, AuthScopeID: c.AuthScopeID(), CaptureAt: time.Now()}
				}
				gqlReport.Method = "graphql"
				gqlReport.FallbackChain = []string{"graphql"}
				gqlReport.Error = "rest_fallback_failed"
				return nil, nil, gqlReport, fmt.Errorf("graphql failed (%v); rest fallback failed: %w", err, restErr)
			}
			// REST fallback succeeded: dedup the REST result against the GraphQL
			// partial list, compute the combined report, and return the merged
			// fork set with no extras (REST has no T1 extras).
			merged := restForks
			if len(streamed) > 0 {
				filtered := make([]ForkInfo, 0, len(restForks))
				for _, f := range restForks {
					if _, seen := streamed[f.ID]; seen {
						continue
					}
					filtered = append(filtered, f)
				}
				merged = append(forks, filtered...)
			}
			report := gqlReport
			if report == nil {
				report = &forge.AcquisitionReport{
					Method:        "graphql+rest",
					Scope:         "direct",
					APIVersion:    defaultRESTVersion,
					AuthMode:      authMode,
					FallbackChain: []string{"graphql", "rest"},
					AuthScopeID:   c.AuthScopeID(),
					CaptureAt:     time.Now(),
					Pages:         restPages,
					RawRows:       restRawRows,
				}
			} else {
				report.Method = "graphql+rest"
				report.FallbackChain = []string{"graphql", "rest"}
				report.Pages += restPages
				report.RawRows += restRawRows
			}
			seen := make(map[int64]struct{}, len(merged))
			for _, f := range merged {
				seen[f.ID] = struct{}{}
			}
			report.UniqueRows = len(seen)
			report.DuplicateRows = report.RawRows - report.UniqueRows
			// Each half arrives stars-sorted; the concatenation is not.
			sortForksByStars(merged, nil)
			// Keep the T1 extras (branches, tip SHA, releases, open PRs) for
			// every fork GraphQL did return. Dropping them all because a late
			// page failed erased branch data network-wide: on llama.cpp the
			// walk returned 18,500 forks before failing, and every one lost
			// its branches, hiding side-branch work from the divergence batch.
			return merged, extrasByID(forks, extras), report, nil
		}

		return forks, extrasByID(forks, extras), gqlReport, nil
	}

	// REST fallback — no extras. fetchForksREST counts raw, non-empty REST
	// batches before dedup, so report accounting never depends on
	// dedup/display.
	restForks, restStats, restErr := c.fetchForksREST(ctx, owner, repo, onPage)
	restPages, restRawRows := restStats.pages, restStats.raw
	if restErr != nil {
		// Pure REST failure: still surface a report so callers know what we
		// tried. Method stays "rest" and Error marks the failure.
		report := &forge.AcquisitionReport{
			Method:        "rest",
			Scope:         "direct",
			APIVersion:    defaultRESTVersion,
			AuthMode:      authMode,
			FallbackChain: []string{"rest"},
			AuthScopeID:   c.AuthScopeID(),
			CaptureAt:     time.Now(),
			Error:         "rest_failed",
		}
		return nil, nil, report, restErr
	}
	seen := make(map[int64]struct{}, len(restForks))
	for _, f := range restForks {
		seen[f.ID] = struct{}{}
	}
	report := &forge.AcquisitionReport{
		Method:        "rest",
		Scope:         "direct",
		APIVersion:    defaultRESTVersion,
		AuthMode:      authMode,
		FallbackChain: []string{"rest"},
		Pages:         restPages,
		RawRows:       restRawRows,
		UniqueRows:    len(seen),
		DuplicateRows: restRawRows - len(seen),
		CaptureAt:     time.Now(),
		AuthScopeID:   c.AuthScopeID(),
	}
	return restForks, nil, report, nil
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
