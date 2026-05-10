package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// CurrentUserLogin returns the authenticated user's GitHub login (e.g. "octocat").
// The first call hits the API; subsequent calls return the cached value.
// Returns an error when unauthenticated or when the API call fails.
func (c *Client) CurrentUserLogin(ctx context.Context) (string, error) {
	if !c.authenticated {
		return "", fmt.Errorf("not authenticated")
	}
	c.mu.Lock()
	cached := c.currentUserLogin
	c.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	resp, err := c.GetRaw(ctx, "user")
	if err != nil {
		return "", fmt.Errorf("fetch current user: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read user body: %w", err)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return "", fmt.Errorf("parse user: %w", err)
	}
	if u.Login == "" {
		return "", fmt.Errorf("empty login in user response")
	}
	c.mu.Lock()
	c.currentUserLogin = u.Login
	c.mu.Unlock()
	return u.Login, nil
}
