package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
)

// FetchCompare fetches the comparison between a parent branch and a fork branch.
// Returns an empty result (not error) if the compare 404s (e.g., deleted fork).
func (c *Client) FetchCompare(ctx context.Context, parentOwner, parentRepo, parentBranch, forkOwner, forkBranch string) (CompareResult, error) {
	path := fmt.Sprintf("repos/%s/%s/compare/%s...%s:%s",
		parentOwner, parentRepo, parentBranch, forkOwner, forkBranch)

	var result CompareResult
	err := c.Get(ctx, path, &result)
	if err != nil {
		// 404 means the fork is inaccessible — return empty, not error
		if isNotFound(err) {
			return CompareResult{}, nil
		}
		return CompareResult{}, err
	}
	return result, nil
}

// UniqueAuthors extracts unique author logins from compare commits.
func UniqueAuthors(compare CompareResult) []string {
	seen := make(map[string]bool)
	var authors []string
	for _, commit := range compare.Commits {
		login := ""
		if commit.Author != nil {
			login = commit.Author.Login
		}
		if login == "" {
			login = commit.CommitDet.Author.Name
		}
		if login != "" && !seen[login] {
			seen[login] = true
			authors = append(authors, login)
		}
	}
	return authors
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *ghAPI.HTTPError
	if ok := asHTTPError(err, &httpErr); ok {
		return httpErr.StatusCode == http.StatusNotFound
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
	var httpErr *ghAPI.HTTPError
	if ok := asHTTPError(err, &httpErr); ok {
		return httpErr.StatusCode == http.StatusForbidden
	}
	return strings.Contains(err.Error(), "403")
}

// asHTTPError attempts to extract an HTTPError from the error chain.
func asHTTPError(err error, target **ghAPI.HTTPError) bool {
	type httpErrorer interface {
		StatusCode() int
	}
	// go-gh returns *api.HTTPError; use errors.As pattern
	for err != nil {
		if he, ok := err.(*ghAPI.HTTPError); ok {
			*target = he
			return true
		}
		if uw, ok := err.(interface{ Unwrap() error }); ok {
			err = uw.Unwrap()
		} else {
			break
		}
	}
	return false
}
