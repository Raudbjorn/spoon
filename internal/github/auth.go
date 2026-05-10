package github

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
)

// CheckAuth creates a client and probes its authentication state.
func CheckAuth() (*Client, AuthStatus, error) {
	client, err := NewClient()
	if err != nil {
		return nil, AuthStatus{}, err
	}

	status := AuthStatus{
		Authenticated: client.IsAuthenticated(),
	}

	if client.authenticated {
		status.TokenSource = "gh"
	} else {
		status.TokenSource = "none"
	}

	// Probe rate limit to get actual numbers
	var rl struct {
		Resources struct {
			Core struct {
				Limit     int `json:"limit"`
				Remaining int `json:"remaining"`
				Reset     int64 `json:"reset"`
				Used      int `json:"used"`
			} `json:"core"`
		} `json:"resources"`
	}
	if resp, err := client.GetRaw(context.Background(), "rate_limit"); err == nil {
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr == nil {
			if err := json.Unmarshal(body, &rl); err == nil {
				status.RateLimit = RateLimit{
					Limit:     rl.Resources.Core.Limit,
					Remaining: rl.Resources.Core.Remaining,
					Used:      rl.Resources.Core.Used,
				}
				client.mu.Lock()
				client.rateLimit = status.RateLimit
				client.mu.Unlock()
			}
		}
		// Parse OAuth scopes from response header.
		if scopesHeader := resp.Header.Get("X-OAuth-Scopes"); scopesHeader != "" {
			for _, s := range strings.Split(scopesHeader, ",") {
				s = strings.TrimSpace(s)
				if s != "" {
					status.Scopes = append(status.Scopes, s)
				}
			}
		}
	}

	return client, status, nil
}

// IsGHInstalled checks if the gh CLI is available on PATH.
func IsGHInstalled() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}
