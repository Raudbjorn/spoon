package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
	gogithub "github.com/google/go-github/v90/github"
)

// FetchCompare fetches the comparison between a parent branch and a fork branch.
// Returns an empty result (not error) if the compare 404s (e.g., deleted fork).
func (c *Client) FetchCompare(ctx context.Context, parentOwner, parentRepo, parentBranch, forkOwner, forkBranch string) (CompareResult, error) {
	path := fmt.Sprintf("repos/%s/%s/compare/%s...%s:%s",
		parentOwner, parentRepo, parentBranch, forkOwner, forkBranch)

	var result CompareResult
	err := c.Get(ctx, path, &result)
	if err != nil {
		// 404 means the fork is inaccessible (deleted, private, DMCA'd) — that
		// is not a run-ending error, so it stays a nil error. It is emphatically
		// not a comparison that found no divergence, so Performed stays false
		// and callers must not persist or score it.
		if isNotFound(err) {
			return CompareResult{}, nil
		}
		return CompareResult{}, err
	}
	result.Performed = true
	return result, nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if status, _, ok := httpFailure(err); ok {
		return status == http.StatusNotFound
	}
	return strings.Contains(err.Error(), "404")
}

// isForbidden reports whether err represents a 403 from the GitHub API. A
// 403 typically means the resource exists but the caller cannot reach it —
// the most common causes are private forks and per-resource rate limiting.
// Both are "expected unavailability" rather than "broken", so callers that
// handle 404 as a graceful empty result should also handle 403 the same way.
func isForbidden(err error) bool {
	if err == nil {
		return false
	}
	if status, _, ok := httpFailure(err); ok {
		return status == http.StatusForbidden
	}
	return strings.Contains(err.Error(), "403")
}

// isNotAcceptable reports whether err represents a 406 from the GitHub API.
// FetchCompareDiff treats it as fail-soft alongside 404: the compare exists,
// but this fork/branch pair cannot be rendered as a diff (GitHub returns 406
// for a small number of pathological compares that its diff renderer
// refuses, distinct from the 200 "Binary files ... differ" it gives for an
// ordinary binary file).
func isNotAcceptable(err error) bool {
	if err == nil {
		return false
	}
	if status, _, ok := httpFailure(err); ok {
		return status == http.StatusNotAcceptable
	}
	return strings.Contains(err.Error(), "406")
}

// isUnprocessableEntity reports whether err represents a 422 from the GitHub
// API. FetchCompareDiff treats it as fail-soft alongside 404/406: GitHub
// returns 422 for a compare it cannot compute at all (e.g. one side of the
// comparison is unreachable), which is unrelated to forge.CompareFilesCap
// and not worth escalating as an error.
func isUnprocessableEntity(err error) bool {
	if err == nil {
		return false
	}
	if status, _, ok := httpFailure(err); ok {
		return status == http.StatusUnprocessableEntity
	}
	return strings.Contains(err.Error(), "422")
}

// httpFailure extracts the HTTP status and response headers from err,
// whichever client produced it: go-github for REST (an *ErrorResponse, or one of
// its rate-limit specialisations) or go-gh for GraphQL (an *HTTPError). ok is
// false when err carries no HTTP response at all, e.g. a transport failure.
func httpFailure(err error) (status int, header http.Header, ok bool) {
	var ghHTTP *ghAPI.HTTPError
	if errors.As(err, &ghHTTP) {
		return ghHTTP.StatusCode, ghHTTP.Headers, true
	}
	var resp *http.Response
	var errResp *gogithub.ErrorResponse
	var rateErr *gogithub.RateLimitError
	var abuseErr *gogithub.AbuseRateLimitError
	switch {
	case errors.As(err, &errResp):
		resp = errResp.Response
	case errors.As(err, &rateErr):
		resp = rateErr.Response
	case errors.As(err, &abuseErr):
		resp = abuseErr.Response
	}
	if resp == nil {
		return 0, nil, false
	}
	return resp.StatusCode, resp.Header, true
}
