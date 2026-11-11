package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// ownerProfileRecord is the on-disk cache shape. Profile() projects it
// into forge.OwnerProfile for scoring and NDJSON; SchemaVersion stays
// here so loadOwnerProfile can reject older sampling semantics without
// putting a cache gate on the forge type.
type ownerProfileRecord struct {
	// SchemaVersion lets loadOwnerProfile reject files written under
	// older sampling semantics; saveOwnerProfile stamps it.
	SchemaVersion int    `json:"schema_version"`
	Login         string `json:"login"`
	// TotalPublicRepos counts the repositories observed in the sample,
	// not GitHub's public_repos; it equals the account total only when
	// Complete is true.
	TotalPublicRepos int `json:"total_public_repos"`
	ForkCount        int `json:"fork_count"`
	SignalForkCount  int `json:"signal_fork_count"`
	NonForkRepoCount int `json:"non_fork_repo_count"`
	// Complete is false when the account had more repositories than the
	// page cap allowed us to read, so the counts describe only the
	// SampleOrder-ordered prefix.
	Complete    bool      `json:"complete"`
	SampleOrder string    `json:"sample_order"`
	FetchedAt   time.Time `json:"fetched_at"`
}

// Profile converts the cached record into the provider-neutral
// forge.OwnerProfile, carrying the completeness and sample-order
// fields so consumers can tell a full account from a window of one.
func (r *ownerProfileRecord) Profile() *forge.OwnerProfile {
	if r == nil {
		return nil
	}
	return &forge.OwnerProfile{
		Login:            r.Login,
		TotalPublicRepos: r.TotalPublicRepos,
		ForkCount:        r.ForkCount,
		SignalForkCount:  r.SignalForkCount,
		NonForkRepoCount: r.NonForkRepoCount,
		Complete:         r.Complete,
		SampleOrder:      r.SampleOrder,
		FetchedAt:        r.FetchedAt,
	}
}

// ownerProfileTTL bounds the on-disk cache freshness. 24h is the
// default; the forksops pipeline can override per run via
// Options.OwnerCacheTTL.
const ownerProfileTTL = 24 * time.Hour

// ownerProfileSchemaVersion is bumped when the sampling semantics
// change in a way that makes older cache files untrustworthy. v2 added
// the explicit sample order and the Complete flag.
const ownerProfileSchemaVersion = 2

// ownerProfileSampleOrder names the ordering requested from GitHub,
// recorded with the sample so a reader knows which window was kept.
const ownerProfileSampleOrder = "pushed_desc"

// ownerProfilePaginationCap bounds how many pages the FetchUserRepos
// walker reads. 5 pages × 100 = 500 repos, which is enough to
// characterize the vast majority of GitHub users' fork-farmer signal
// before the cost outweighs the value. Larger accounts keep only the
// 500 most recently pushed repositories and are recorded as
// incomplete: the counts are a window, so consumers must not infer
// that repositories outside it do not exist.
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
	if rec.SchemaVersion != ownerProfileSchemaVersion {
		// Written under older sampling semantics (no completeness or
		// sample order): refetch rather than trust it as a full history.
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
	stamped := *rec
	stamped.SchemaVersion = ownerProfileSchemaVersion
	data, err := json.MarshalIndent(&stamped, "", "  ")
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

// LoadCachedOwnerProfile returns the fresh on-disk record for login, or
// nil on a miss (absent, stale, unreadable, or written under an older
// schema). It performs no network I/O, so callers can consult it
// without spending rate budget or a per-run fetch cap. A ttl of 0
// disables the read.
func LoadCachedOwnerProfile(login string, ttl time.Duration) *ownerProfileRecord {
	return loadOwnerProfile(login, ttl)
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

// fetchOwnerProfileLive walks /users/{login}/repos?type=owner, newest
// push first, for at most ownerProfilePaginationCap pages and tallies
// the farmer signal. The sort is explicit because GitHub's default is
// full_name ascending, which would keep an alphabetical window rather
// than the recent one. When more pages existed the record is marked
// incomplete. Returns (nil, *RateLimitError) on rate-limit so the
// caller can decide to skip or retry; (nil, nil) on 404
// (private/renamed user); (nil, err) for any other failure.
func (c *Client) fetchOwnerProfileLive(ctx context.Context, login string) (*ownerProfileRecord, error) {
	rec := &ownerProfileRecord{Login: login, SampleOrder: ownerProfileSampleOrder}
	called := false
	// No leading slash: go-gh joins it onto the API root, so "/users/..."
	// goes out as "//users/..." and GitHub answers 404, which this fetch
	// reads as "no signal" -- the profile would silently never load.
	path := fmt.Sprintf("users/%s/repos?type=owner&sort=pushed&direction=desc&per_page=100", login)
	truncated, err := c.getPaginated(ctx, path, ownerProfilePaginationCap, func(raw json.RawMessage) error {
		called = true
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
	if err != nil {
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
	rec.Complete = !truncated
	rec.FetchedAt = time.Now().UTC()
	return rec, nil
}

// isRecentPush returns true when the pushed_at timestamp (RFC3339) is
// within the last year. The pushed_at field on /users/{login}/repos
// reflects the most-recent push to ANY branch, which is the
// "is the fork actually being maintained" signal the farmer
// detection relies on. It shows the repository is active, not who
// authored the pushed changes.
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

// isNotFoundErr reports whether err is an HTTP 404, by typed status.
func isNotFoundErr(err error) bool {
	return statusCode(err) == http.StatusNotFound
}
