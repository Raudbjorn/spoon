// cmd/spn/repo_search_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v90/github"
	gh "github.com/svnbjrn/spoon/internal/github"
)

// searchCall records what the command handed the searchReposFn seam.
type searchCall struct {
	calls int
	query string
	opts  gh.RepoSearchOptions
}

// stubRepoSearch replaces the auth and search seams for one test. Auth returns
// a nil client, which is fine because searchReposFn never touches it.
func stubRepoSearch(t *testing.T, fn func(query string, opts gh.RepoSearchOptions) (*gh.RepoSearchResult, error)) *searchCall {
	t.Helper()
	prevAuth, prevSearch := repoCheckAuthFn, searchReposFn
	t.Cleanup(func() { repoCheckAuthFn, searchReposFn = prevAuth, prevSearch })
	repoCheckAuthFn = func() (*gh.Client, gh.AuthStatus, error) { return nil, gh.AuthStatus{}, nil }
	rec := &searchCall{}
	searchReposFn = func(_ context.Context, _ *gh.Client, query string, opts gh.RepoSearchOptions) (*gh.RepoSearchResult, error) {
		rec.calls++
		rec.query, rec.opts = query, opts
		return fn(query, opts)
	}
	return rec
}

func resultWith(total int, items ...gh.RepoSearchItem) func(string, gh.RepoSearchOptions) (*gh.RepoSearchResult, error) {
	return func(string, gh.RepoSearchOptions) (*gh.RepoSearchResult, error) {
		return &gh.RepoSearchResult{TotalCount: total, Items: items}, nil
	}
}

func runRepoSearch(t *testing.T, args ...string) (exit int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	exit = runRepoWith(append([]string{"search"}, args...), &out, &errBuf)
	return exit, out.String(), errBuf.String()
}

func errorBody(t *testing.T, stderr string) map[string]any {
	t.Helper()
	var env map[string]map[string]any
	if err := json.Unmarshal([]byte(stderr), &env); err != nil {
		t.Fatalf("stderr not a JSON error envelope: %v\n%s", err, stderr)
	}
	return env["error"]
}

func decodeEnvelope(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout not a JSON object: %v\n%s", err, stdout)
	}
	return got
}

func TestSpnRepoSearch_EmitsEnvelope(t *testing.T) {
	rec := stubRepoSearch(t, resultWith(412,
		gh.RepoSearchItem{
			FullName: "alice/term", HTMLURL: "https://github.com/alice/term",
			Description: "a terminal", Language: "Go", Stars: 812, Forks: 97,
			PushedAt: "2026-09-01T00:00:00Z", Topics: []string{"cli", "tui"},
		},
		gh.RepoSearchItem{
			FullName: "bob/term-fork", HTMLURL: "https://github.com/bob/term-fork",
			Stars: 1, Fork: true, Archived: true, PushedAt: "2025-01-01T00:00:00Z", // nil Topics
		},
	))

	exit, stdout, stderr := runRepoSearch(t, "terminal language:Go", "--limit", "2")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
	if rec.calls != 1 {
		t.Fatalf("searchReposFn called %d times, want exactly 1 (one bounded request)", rec.calls)
	}
	if rec.query != "terminal language:Go" || rec.opts.PerPage != 2 || rec.opts.Page != 1 || rec.opts.Sort != "" {
		t.Errorf("seam got query=%q opts=%+v", rec.query, rec.opts)
	}

	got := decodeEnvelope(t, stdout)
	for key, want := range map[string]any{
		"query": "terminal language:Go", "total_count": float64(412), "incomplete_results": false,
		"fetched": float64(2), "page": float64(1), "next_page": float64(2),
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	items, ok := got["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items = %#v, want 2 entries", got["items"])
	}
	first := items[0].(map[string]any)
	for key, want := range map[string]any{
		"full_name": "alice/term", "url": "https://github.com/alice/term", "description": "a terminal",
		"language": "Go", "stars": float64(812), "forks_count": float64(97), "is_fork": false,
		"archived": false, "pushed_at": "2026-09-01T00:00:00Z",
	} {
		if first[key] != want {
			t.Errorf("items[0].%s = %v, want %v", key, first[key], want)
		}
	}
	if topics, ok := first["topics"].([]any); !ok || len(topics) != 2 {
		t.Errorf("items[0].topics = %#v", first["topics"])
	}
	second := items[1].(map[string]any)
	if second["is_fork"] != true || second["archived"] != true {
		t.Errorf("items[1] = %+v, want fork+archived", second)
	}
	// A repo with no topics must still expose an array an agent can iterate.
	if !strings.Contains(stdout, `"topics": []`) {
		t.Errorf("nil Topics must marshal as [], not null:\n%s", stdout)
	}
}

func TestSpnRepoSearch_DefaultsAndSortMapping(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		wantSort string
	}{
		{nil, ""},
		{[]string{"--sort", "best-match"}, ""}, // best match omits the sort parameter
		{[]string{"--sort", "stars"}, "stars"},
		{[]string{"--sort", "forks"}, "forks"},
		{[]string{"--sort", "updated"}, "updated"},
		{[]string{"--forge", "github"}, ""},
		{[]string{"--forge", "GitHub"}, ""},
	} {
		rec := stubRepoSearch(t, resultWith(1, gh.RepoSearchItem{FullName: "a/b"}))
		exit, _, stderr := runRepoSearch(t, append([]string{"terminal"}, tc.args...)...)
		if exit != 0 {
			t.Fatalf("args %v: exit=%d stderr=%s", tc.args, exit, stderr)
		}
		if rec.opts.Sort != tc.wantSort || rec.opts.PerPage != 30 || rec.opts.Page != 1 {
			t.Errorf("args %v: opts = %+v, want sort=%q per_page=30 page=1", tc.args, rec.opts, tc.wantSort)
		}
	}
}

func TestSpnRepoSearch_EqualsFormAndEndOfFlags(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantQ    string
		wantSort string
		wantPage int
		wantPer  int
	}{
		{"limit equals", []string{"terminal", "--limit=2"}, "terminal", "", 1, 2},
		{"page and sort equals", []string{"--page=2", "--sort=stars", "terminal", "--limit=10"}, "terminal", "stars", 2, 10},
		{"forge equals", []string{"terminal", "--forge=GitHub"}, "terminal", "", 1, 30},
		{"end of flags", []string{"--", "terminal language:Go"}, "terminal language:Go", "", 1, 30},
		{"end of flags after limit", []string{"--limit=5", "--", "fork:true"}, "fork:true", "", 1, 5},
		{"dashed query after end of flags", []string{"--", "--sort"}, "--sort", "", 1, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := stubRepoSearch(t, resultWith(1, gh.RepoSearchItem{FullName: "a/b"}))
			exit, _, stderr := runRepoSearch(t, tc.args...)
			if exit != 0 {
				t.Fatalf("exit=%d stderr=%s", exit, stderr)
			}
			if rec.query != tc.wantQ || rec.opts.Sort != tc.wantSort || rec.opts.Page != tc.wantPage || rec.opts.PerPage != tc.wantPer {
				t.Errorf("query=%q opts=%+v, want q=%q sort=%q page=%d per=%d", rec.query, rec.opts, tc.wantQ, tc.wantSort, tc.wantPage, tc.wantPer)
			}
		})
	}
}

func TestSpnRepoSearch_BadInputNeverReachesGitHub(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no query", nil},
		{"empty query", []string{""}},
		{"blank query", []string{"   "}},
		{"second positional", []string{"terminal", "extra"}},
		{"empty query then a real one", []string{"", "terminal"}},
		{"unknown flag", []string{"terminal", "--bogus"}},
		{"unknown flag with value", []string{"terminal", "--bogus", "1"}},
		{"limit missing value", []string{"terminal", "--limit"}},
		{"limit zero", []string{"terminal", "--limit", "0"}},
		{"limit negative", []string{"terminal", "--limit", "-3"}},
		{"limit above 100", []string{"terminal", "--limit", "101"}},
		{"limit not a number", []string{"terminal", "--limit", "many"}},
		{"page missing value", []string{"terminal", "--page"}},
		{"page zero", []string{"terminal", "--page", "0"}},
		{"page not a number", []string{"terminal", "--page", "two"}},
		{"page past the 1000-result cap", []string{"terminal", "--page", "34", "--limit", "30"}},
		{"page past the cap at default limit", []string{"terminal", "--page", "34"}},
		{"page past the cap at max limit", []string{"terminal", "--page", "11", "--limit", "100"}},
		{"sort missing value", []string{"terminal", "--sort"}},
		{"sort unknown", []string{"terminal", "--sort", "readme"}},
		{"forge missing value", []string{"terminal", "--forge"}},
		{"forge gitlab", []string{"terminal", "--forge", "gitlab"}},
		{"limit equals above 100", []string{"terminal", "--limit=101"}},
		{"unknown equals flag", []string{"terminal", "--bogus=1"}},
		{"end of flags then two positionals", []string{"--", "a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := stubRepoSearch(t, resultWith(0))
			exit, stdout, stderr := runRepoSearch(t, tc.args...)
			if exit != 2 {
				t.Fatalf("exit=%d, want 2; stderr=%s", exit, stderr)
			}
			if code := errorBody(t, stderr)["code"]; code != "bad_input" {
				t.Errorf("code=%v, want bad_input", code)
			}
			if stdout != "" {
				t.Errorf("stdout must stay clean on failure, got %q", stdout)
			}
			if rec.calls != 0 {
				t.Errorf("searchReposFn called %d times for bad input, want 0", rec.calls)
			}
		})
	}
}

func TestSpnRepoSearch_ValidationNamesLimitFlag(t *testing.T) {
	for _, args := range [][]string{
		{"terminal", "--limit", "101"},
		{"terminal", "--limit=101"},
		{"terminal", "--page", "34", "--limit", "30"},
	} {
		rec := stubRepoSearch(t, resultWith(0))
		exit, _, stderr := runRepoSearch(t, args...)
		if exit != 2 {
			t.Fatalf("args %v: exit=%d stderr=%s", args, exit, stderr)
		}
		msg, _ := errorBody(t, stderr)["message"].(string)
		if strings.Contains(msg, "per_page") {
			t.Errorf("args %v: message %q names per_page", args, msg)
		}
		if !strings.Contains(msg, "--limit") {
			t.Errorf("args %v: message %q should name --limit", args, msg)
		}
		if rec.calls != 0 {
			t.Errorf("args %v: searchReposFn called", args)
		}
	}
}

func TestSpnRepoSearch_PageAtCapBoundaryAccepted(t *testing.T) {
	// 33*30 = 990 and 10*100 = 1000 are the last reachable pages.
	for _, args := range [][]string{{"--page", "33", "--limit", "30"}, {"--page", "10", "--limit", "100"}} {
		rec := stubRepoSearch(t, resultWith(5000, gh.RepoSearchItem{FullName: "a/b"}))
		exit, _, stderr := runRepoSearch(t, append([]string{"terminal"}, args...)...)
		if exit != 0 {
			t.Fatalf("args %v: exit=%d stderr=%s", args, exit, stderr)
		}
		if rec.calls != 1 {
			t.Errorf("args %v: searchReposFn called %d times, want 1", args, rec.calls)
		}
	}
}

func TestNextRepoSearchPage(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		page, perPage, total int
		want                 int // 0 = null
	}{
		{"more results follow", 1, 30, 412, 2},
		{"last partial page", 14, 30, 412, 0}, // 420 >= 412
		{"exact last page", 2, 30, 60, 0},
		{"empty result", 1, 30, 0, 0},
		{"one short of the end", 1, 100, 999, 2},
		{"total far above the cap, mid-range", 32, 30, 5000, 33}, // 33*30 = 990 is reachable
		{"page 33 at 30: next would be 34*30=1020 > 1000", 33, 30, 5000, 0},
		{"total exactly the cap", 10, 100, 1000, 0},
		{"total above the cap, last reachable page", 10, 100, 1500, 0},
		{"total above the cap, one before last", 9, 100, 1500, 10}, // 10*100 = 1000 is reachable
		{"per_page 1 near the cap", 999, 1, 5000, 1000},
		{"per_page 1 at the cap", 1000, 1, 5000, 0},
	} {
		got := nextRepoSearchPage(tc.page, tc.perPage, tc.total)
		switch {
		case tc.want == 0 && got != nil:
			t.Errorf("%s: next_page = %d, want null", tc.name, *got)
		case tc.want != 0 && (got == nil || *got != tc.want):
			t.Errorf("%s: next_page = %v, want %d", tc.name, got, tc.want)
		}
	}
}

// The cap rule is driven by the requested page size, never by how many items
// survived deduplication, so a page that came back short still pages on.
func TestSpnRepoSearch_NextPageIgnoresDedupedCount(t *testing.T) {
	stubRepoSearch(t, resultWith(412, gh.RepoSearchItem{FullName: "a/b"}))
	exit, stdout, stderr := runRepoSearch(t, "terminal", "--limit", "30")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
	got := decodeEnvelope(t, stdout)
	if got["fetched"] != float64(1) || got["next_page"] != float64(2) {
		t.Errorf("fetched=%v next_page=%v, want 1 and 2", got["fetched"], got["next_page"])
	}
}

func TestSpnRepoSearch_NextPageNullAtThousandResultBoundary(t *testing.T) {
	stubRepoSearch(t, resultWith(5000, gh.RepoSearchItem{FullName: "a/b"}))
	exit, stdout, stderr := runRepoSearch(t, "terminal", "--page", "33", "--limit", "30")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
	// Assert on the raw bytes: a decoded map cannot tell null from absent.
	if !strings.Contains(stdout, `"next_page": null`) {
		t.Errorf("page 33 at per_page 30 must report next_page null:\n%s", stdout)
	}
	if got := decodeEnvelope(t, stdout); got["page"] != float64(33) || got["total_count"] != float64(5000) {
		t.Errorf("page=%v total_count=%v", got["page"], got["total_count"])
	}
}

func TestSpnRepoSearch_IncompleteResultsPreserved(t *testing.T) {
	stubRepoSearch(t, func(string, gh.RepoSearchOptions) (*gh.RepoSearchResult, error) {
		return &gh.RepoSearchResult{TotalCount: 3, IncompleteResults: true, Items: []gh.RepoSearchItem{{FullName: "a/b"}}}, nil
	})
	exit, stdout, stderr := runRepoSearch(t, "terminal")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
	if got := decodeEnvelope(t, stdout); got["incomplete_results"] != true {
		t.Errorf("incomplete_results = %v, want true", got["incomplete_results"])
	}
}

func TestSpnRepoSearch_ZeroResultsWarnsButSucceeds(t *testing.T) {
	stubRepoSearch(t, resultWith(0))
	exit, stdout, stderr := runRepoSearch(t, "zzzz-no-such-repo")
	if exit != 0 {
		t.Fatalf("exit=%d, want 0 for an empty result; stderr=%s", exit, stderr)
	}
	// stdout still carries the envelope, with an array (not null) an agent can iterate.
	got := decodeEnvelope(t, stdout)
	if got["total_count"] != float64(0) || got["fetched"] != float64(0) || got["next_page"] != nil {
		t.Errorf("envelope = %+v", got)
	}
	if !strings.Contains(stdout, `"items": []`) {
		t.Errorf("zero results must marshal items as [], not null:\n%s", stdout)
	}
	var warn map[string]map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &warn); err != nil {
		t.Fatalf("stderr not a single warning line: %v\n%s", err, stderr)
	}
	if warn["warning"]["code"] != "no_results" || warn["warning"]["remediation"] == "" {
		t.Errorf("warning = %+v", warn)
	}
}

func TestSpnRepoSearch_NonEmptyResultHasNoWarning(t *testing.T) {
	stubRepoSearch(t, resultWith(1, gh.RepoSearchItem{FullName: "a/b"}))
	exit, _, stderr := runRepoSearch(t, "terminal")
	if exit != 0 || stderr != "" {
		t.Errorf("exit=%d stderr=%q, want a quiet success", exit, stderr)
	}
}

func TestSpnRepoSearch_RateLimited(t *testing.T) {
	stubRepoSearch(t, func(string, gh.RepoSearchOptions) (*gh.RepoSearchResult, error) {
		return nil, &gh.RateLimitError{ResetAt: time.Now().Add(90 * time.Second)}
	})
	exit, stdout, stderr := runRepoSearch(t, "terminal")
	if exit != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%s", exit, stderr)
	}
	env := errorBody(t, stderr)
	if env["code"] != "rate_limited" || env["retryable"] != true {
		t.Errorf("envelope = %+v", env)
	}
	secs, ok := env["retry_after_seconds"].(float64)
	if !ok || secs < 1 || secs > 90 {
		t.Errorf("retry_after_seconds = %v, want within (0, 90]", env["retry_after_seconds"])
	}
	if stdout != "" {
		t.Errorf("stdout must stay clean on failure, got %q", stdout)
	}
}

// A rate limit that arrives wrapped (as SearchRepositories wraps it) must
// still map to rate_limited rather than a generic upstream error.
func TestSpnRepoSearch_WrappedRateLimitStillRateLimited(t *testing.T) {
	stubRepoSearch(t, func(string, gh.RepoSearchOptions) (*gh.RepoSearchResult, error) {
		return nil, fmt.Errorf("repo search: %w", &gh.RateLimitError{ResetAt: time.Now().Add(30 * time.Second)})
	})
	exit, _, stderr := runRepoSearch(t, "terminal")
	if exit != 1 || errorBody(t, stderr)["code"] != "rate_limited" {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
}

func TestSpnRepoSearch_QueryRejectedIsBadInput(t *testing.T) {
	stubRepoSearch(t, func(string, gh.RepoSearchOptions) (*gh.RepoSearchResult, error) {
		return nil, fmt.Errorf("repo search: %w", &gogithub.ErrorResponse{Response: &http.Response{StatusCode: http.StatusUnprocessableEntity}, Message: "Validation Failed"})
	})
	exit, stdout, stderr := runRepoSearch(t, "user:nobody")
	if exit != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", exit, stderr)
	}
	env := errorBody(t, stderr)
	if env["code"] != "bad_input" || env["retryable"] != false {
		t.Errorf("envelope = %+v", env)
	}
	if rem, _ := env["remediation"].(string); !strings.Contains(rem, "qualifier") {
		t.Errorf("remediation should point at query syntax, got %q", rem)
	}
	if stdout != "" {
		t.Errorf("stdout must stay clean on failure, got %q", stdout)
	}
}

// Anything that is not the caller's fault must not be reported as bad_input:
// exit 2 tells an agent not to retry.
func TestSpnRepoSearch_OtherErrorsAreNotBadInput(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantCode string
	}{
		{"server error", &gogithub.ErrorResponse{Response: &http.Response{StatusCode: http.StatusInternalServerError}, Message: "boom"}, "upstream_error"},
		{"bad gateway", &gogithub.ErrorResponse{Response: &http.Response{StatusCode: http.StatusBadGateway}}, "upstream_error"},
		{"plain error", errors.New("connection reset"), "upstream_error"},
		{"all tokens rejected", &gh.AllBackendsRejectedError{Rejected: 1}, "auth_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubRepoSearch(t, func(string, gh.RepoSearchOptions) (*gh.RepoSearchResult, error) { return nil, tc.err })
			exit, stdout, stderr := runRepoSearch(t, "terminal")
			code := errorBody(t, stderr)["code"]
			if code == "bad_input" {
				t.Fatalf("code = bad_input for %v", tc.err)
			}
			if code != tc.wantCode {
				t.Errorf("code = %v, want %s", code, tc.wantCode)
			}
			if want := map[string]int{"upstream_error": 1, "auth_required": 2}[tc.wantCode]; exit != want {
				t.Errorf("exit = %d, want %d", exit, want)
			}
			if stdout != "" {
				t.Errorf("stdout must stay clean on failure, got %q", stdout)
			}
		})
	}
}

func TestSpnRepoSearch_AuthFailureShortCircuits(t *testing.T) {
	rec := stubRepoSearch(t, resultWith(0))
	repoCheckAuthFn = func() (*gh.Client, gh.AuthStatus, error) {
		return nil, gh.AuthStatus{}, errors.New("no token")
	}
	exit, _, stderr := runRepoSearch(t, "terminal")
	if exit != 2 || errorBody(t, stderr)["code"] != "auth_required" {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
	if rec.calls != 0 {
		t.Errorf("searchReposFn called %d times without auth", rec.calls)
	}
}

// Validation runs before auth: a caller with a typo learns about it without
// needing credentials, and no client is built for a request that cannot run.
func TestSpnRepoSearch_ValidatesBeforeAuth(t *testing.T) {
	prevAuth := repoCheckAuthFn
	t.Cleanup(func() { repoCheckAuthFn = prevAuth })
	repoCheckAuthFn = func() (*gh.Client, gh.AuthStatus, error) {
		t.Error("auth consulted for a request that fails validation")
		return nil, gh.AuthStatus{}, nil
	}
	if exit, _, stderr := runRepoSearch(t, "terminal", "--limit", "0"); exit != 2 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
}

func TestSpnRepo_MissingVerbListsSearch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := runRepoWith(nil, &stdout, &stderr); exit != 2 {
		t.Fatalf("exit=%d", exit)
	}
	if msg, _ := errorBody(t, stderr.String())["message"].(string); !strings.Contains(msg, "search") {
		t.Errorf("missing-verb message %q should list search", msg)
	}
}

func TestHelpDocumentsRepoSearch(t *testing.T) {
	help := helpText()
	for _, want := range []string{
		"repo search",
		"1000-result",
		"fork:true",
		"next_page",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q", want)
		}
	}
}
