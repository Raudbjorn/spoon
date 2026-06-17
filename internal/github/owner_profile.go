package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ownerProfileRecord is the on-disk cache shape. The fetch function
// returns a *forge.OwnerProfile-shaped value, but the cache layer keeps
// the persistence shape in this package to avoid pulling forge into
// the cache-key path.
type ownerProfileRecord struct {
	Login            string    `json:"login"`
	TotalPublicRepos int       `json:"total_public_repos"`
	ForkCount        int       `json:"fork_count"`
	SignalForkCount  int       `json:"signal_fork_count"`
	NonForkRepoCount int       `json:"non_fork_repo_count"`
	FetchedAt        time.Time `json:"fetched_at"`
}

// ownerProfileTTL bounds the on-disk cache freshness. 24h is the
// default; the forksops pipeline can override per run via
// Options.OwnerCacheTTL.
const ownerProfileTTL = 24 * time.Hour

// ownerProfilePaginationCap bounds how many pages the FetchUserRepos
// walker reads. 5 pages × 100 = 500 repos, which is enough to
// characterize the vast majority of GitHub users' fork-farmer signal
// before the cost outweighs the value. Larger accounts see the
// partial-window approximation; the deviation vs the full history is
// empirically small because the farmer signal is dominated by the
// top of the recent-pushed ordering.
const ownerProfilePaginationCap = 5

// loadOwnerProfile returns the cached record for login (case-insensitive)
// or nil when the cache is missing, expired, or unreadable. A ttl
// override of 0 disables the cache read entirely.
func loadOwnerProfile(login string, ttl time.Duration) *ownerProfileRecord {
	if login == "" {
		return nil
	}
	if ttl == 0 {
		return nil
	}
	path, err := ownerProfileCachePath(login)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec ownerProfileRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil
	}
	if ttl < 0 {
		// negative TTL = "expired"; reserved for tests.
		return nil
	}
	if time.Since(rec.FetchedAt) > ttl {
		return nil
	}
	return &rec
}

// saveOwnerProfile writes the record to disk. Returns an error so the
// caller can decide to log or ignore. The in-memory record is the
// source of truth; the disk write is purely a rate-budget optimization
// for the next run.
func saveOwnerProfile(rec *ownerProfileRecord) error {
	if rec == nil || rec.Login == "" {
		return nil
	}
	path, err := ownerProfileCachePath(rec.Login)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal owner profile: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write owner profile cache: %w", err)
	}
	return nil
}

// ownerProfileCachePath returns the per-owner file path under
// ~/.cache/spoon/owner-profiles/<login>.json. Login is lowercased so
// a renamed-account or case-mismatched lookup still hits the cache.
func ownerProfileCachePath(login string) (string, error) {
	cacheRoot, err := CacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cacheRoot, "owner-profiles")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(dir, strings.ToLower(login)+".json"), nil
}

// FetchUserRepos returns the owner profile for the given login,
// consulting the on-disk cache first. The second return value is
// true when the record was served from cache (and therefore did not
// consume rate-budget); the caller uses this to avoid charging
// cache hits against the per-run owner-cap. Returns (nil, false, nil)
// on rate-limit or 404 so the caller (the fork pipeline) can treat
// the absence as "no signal" rather than an error.
//
// ttl overrides the on-disk cache freshness; pass 0 to force a fresh
// fetch (the test/refresh path).
func (c *Client) FetchUserRepos(ctx context.Context, login string, ttl time.Duration) (*ownerProfileRecord, bool, error) {
	if login == "" {
		return nil, false, nil
	}
	if rec := loadOwnerProfile(login, ttl); rec != nil {
		return rec, true, nil
	}
	rec, err := c.fetchOwnerProfileLive(ctx, login)
	if err != nil {
		return nil, false, err
	}
	if rec == nil {
		return nil, false, nil
	}
	// Best-effort cache write. The in-memory record is still returned
	// to the caller; only the rate-budget optimization is lost.
	if err := saveOwnerProfile(rec); err != nil {
		slog.Warn("owner-profile: cache write failed", "login", login, "err", err)
	}
	return rec, false, nil
}

// fetchOwnerProfileLive walks /users/{login}/repos?type=owner paginated
// and tallies the farmer signal. Returns (nil, *RateLimitError) on
// rate-limit so the caller can decide to skip or retry; (nil, nil) on
// 404 (private/renamed user); (nil, err) for any other failure.
func (c *Client) fetchOwnerProfileLive(ctx context.Context, login string) (*ownerProfileRecord, error) {
	rec := &ownerProfileRecord{Login: login}
	called := false
	path := fmt.Sprintf("/users/%s/repos?type=owner&per_page=100", login)
	pages := 0
	err := c.GetPaginated(ctx, path, func(raw json.RawMessage) error {
		called = true
		pages++
		if pages > ownerProfilePaginationCap {
			return errStopPagination
		}
		var page []struct {
			Fork     bool   `json:"fork"`
			PushedAt string `json:"pushed_at"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("parsing user repos page: %w", err)
		}
		for _, r := range page {
			rec.TotalPublicRepos++
			if r.Fork {
				rec.ForkCount++
				if isRecentPush(r.PushedAt) {
					rec.SignalForkCount++
				}
			} else {
				rec.NonForkRepoCount++
			}
		}
		return nil
	})
	if err != nil && err != errStopPagination {
		// GetPaginated already converts rate-limit HTTP errors into
		// *RateLimitError. Pass them through unchanged.
		if called {
			var rl *RateLimitError
			if errors.As(err, &rl) {
				return nil, err
			}
			// 404 → no signal; nil error. Other errors propagate.
			if isNotFoundErr(err) {
				return nil, nil
			}
			return nil, err
		}
		// GetPaginated failed before the first page ran → likely 404.
		if isNotFoundErr(err) {
			return nil, nil
		}
		return nil, err
	}
	if !called {
		// No pages returned: empty user. Not an error.
		return nil, nil
	}
	rec.FetchedAt = time.Now().UTC()
	return rec, nil
}

// isRecentPush returns true when the pushed_at timestamp (RFC3339) is
// within the last year. The pushed_at field on /users/{login}/repos
// reflects the most-recent push to ANY branch, which is the
// "is the fork actually being maintained" signal the farmer
// detection relies on.
func isRecentPush(pushedAt string) bool {
	if pushedAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, pushedAt)
	if err != nil {
		return false
	}
	return time.Since(t) < 365*24*time.Hour
}

// isNotFoundErr reports whether err is an HTTP 404. The go-gh
// client wraps the status code in the error message, so a substring
// check is sufficient for the rate-limit / not-found fan-out
// without importing ghAPI here.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "HTTP 404")
}
