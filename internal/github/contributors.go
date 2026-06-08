package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrContributorsTimeout is returned when the contributors endpoint keeps
// returning 202. GitHub computes contributor statistics asynchronously and
// returns 202 (Accepted) until the cache is warm; for forks that nobody has
// requested stats for, it can keep 202-ing indefinitely. Callers should treat
// this as "stats unavailable right now" and degrade gracefully — the
// contributors stage is optional enrichment, not a hard dependency.
//
// Use errors.Is(err, ErrContributorsTimeout) to detect it.
var ErrContributorsTimeout = errors.New("contributors endpoint unavailable (202 — GitHub still computing stats)")

const (
	// maxContribRetries is deliberately small. The first request primes
	// GitHub's async stats computation; a couple of short retries give it a
	// moment to settle. We do NOT sit and exhaust a long backoff per fork —
	// when stats stay cold, the right move is to skip and flag, not to block
	// the whole scan. See ErrContributorsTimeout.
	maxContribRetries  = 2
	initialContribWait = 2 * time.Second
	contribBackoff     = 1.5
	maxContribWait     = 5 * time.Second
)

// FetchContributors fetches contributor stats for a repository with 202 retry logic.
// GitHub returns 202 while computing stats; we retry with exponential backoff.
func (c *Client) FetchContributors(ctx context.Context, owner, repo string) ([]ContributorStats, error) {
	path := fmt.Sprintf("repos/%s/%s/stats/contributors", owner, repo)
	wait := initialContribWait

	for attempt := 0; attempt <= maxContribRetries; attempt++ {
		resp, err := c.GetRaw(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("fetching contributors: %w", err)
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("reading contributors response: %w", readErr)
		}

		switch resp.StatusCode {
		case 200:
			var stats []ContributorStats
			if err := json.Unmarshal(body, &stats); err != nil {
				return nil, fmt.Errorf("parsing contributors: %w", err)
			}
			return stats, nil

		case 202:
			// GitHub is still computing — wait and retry
			if attempt == maxContribRetries {
				return nil, ErrContributorsTimeout
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			wait = time.Duration(float64(wait) * contribBackoff)
			if wait > maxContribWait {
				wait = maxContribWait
			}

		case 204:
			// No content — repo has no contributor stats
			return nil, nil

		default:
			return nil, fmt.Errorf("contributors endpoint returned %d: %s", resp.StatusCode, string(body))
		}
	}

	return nil, ErrContributorsTimeout
}
