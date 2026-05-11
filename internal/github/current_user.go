package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// CurrentUserLogin returns the authenticated user's GitHub login (e.g. "octocat").
// The first call hits the API; subsequent calls return the cached value or
// cached error. Returns an error when unauthenticated or when the API call fails.
func (c *Client) CurrentUserLogin(ctx context.Context) (string, error) {
	if !c.authenticated {
		return "", fmt.Errorf("not authenticated")
	}
	c.currentUserLoginOnce.Do(func() {
		// Honor a pre-populated login (tests construct Client literals with
		// currentUserLogin set to skip the API hop). Without this, the Once
		// would always race to the API even when the field is already set.
		if c.currentUserLogin != "" {
			return
		}
		resp, err := c.GetRaw(ctx, "user")
		if err != nil {
			c.currentUserLoginErr = fmt.Errorf("fetch current user: %w", err)
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			c.currentUserLoginErr = fmt.Errorf("read user body: %w", err)
			return
		}
		var u struct {
			Login string `json:"login"`
		}
		if err := json.Unmarshal(body, &u); err != nil {
			c.currentUserLoginErr = fmt.Errorf("parse user: %w", err)
			return
		}
		if u.Login == "" {
			c.currentUserLoginErr = fmt.Errorf("empty login in user response")
			return
		}
		c.currentUserLogin = u.Login
	})
	if c.currentUserLoginErr != nil {
		return "", c.currentUserLoginErr
	}
	return c.currentUserLogin, nil
}
