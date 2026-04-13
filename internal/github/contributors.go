package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrContributorsTimeout is returned when the contributors endpoint keeps returning 202.
var ErrContributorsTimeout = errors.New("contributors endpoint timed out (202 retries exhausted)")

const (
	maxContribRetries   = 5
	initialContribWait  = 2 * time.Second
	contribBackoff      = 1.5
	maxContribWait      = 30 * time.Second
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
