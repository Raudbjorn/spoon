package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	gogithub "github.com/google/go-github/v90/github"
)

// ArchiveLink resolves a source snapshot using the active Spoon credentials.
// The signed download URL must be fetched without forwarding the API token.
func (c *Client) ArchiveLink(ctx context.Context, owner, repo, ref string) (*url.URL, error) {
	c.ensurePool()
	var link *url.URL
	err := c.doWithRetry(ctx, func() error {
		b, err := c.pool.nextBackend(time.Now())
		if err != nil {
			return err
		}
		if b.Rest == nil {
			return fmt.Errorf("github: no REST client configured")
		}
		if err := c.waitRequest(ctx, b.REST.Limiter); err != nil {
			return err
		}
		// GetArchiveLink only decodes HTTP errors with rate checking enabled.
		// Use a fresh client over the same authenticated transport so the
		// backend pool remains the owner of pacing (no stale local budget).
		rc, err := gogithub.NewClient(
			gogithub.WithHTTPClient(b.Rest.Client()),
			gogithub.WithURLs(gogithub.Ptr(b.Rest.BaseURL()), nil),
			gogithub.WithUserAgent(restUserAgent),
			gogithub.WithRateLimitRedirectionalEndpoints(),
		)
		if err != nil {
			return err
		}
		var resp *gogithub.Response
		// Stop at the first redirect so the token never reaches codeload.
		link, resp, err = rc.Repositories.GetArchiveLink(ctx, owner, repo, gogithub.Tarball,
			&gogithub.RepositoryContentGetOptions{Ref: ref}, 0)
		if resp != nil {
			c.updateRateLimitFor(b, resp.Response)
		}
		if err != nil {
			if rl := detectRateLimitFromHTTPError(err); rl != nil {
				c.pool.disableUntil(b, rl.ResetAt, false)
				return rl
			}
			if statusCode(err) == http.StatusUnauthorized {
				c.pool.disableUntil(b, time.Time{}, true)
			} else {
				c.pool.reportFailure(b)
			}
			return err
		}
		c.pool.reportSuccess(b)
		return nil
	})
	return link, err
}
