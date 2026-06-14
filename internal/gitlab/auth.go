package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

const (
	concurrencyAuthed   = 10
	concurrencyUnauthed = 3 // 500 req/min unauthed is more forgiving than GitHub's 60/hr

	rateLimitAuthed   = 2000 // req/min on gitlab.com (authenticated)
	rateLimitUnauthed = 500  // req/min on gitlab.com (unauthenticated)
)

// DetectAuth discovers GitLab credentials in preference order:
//
//  1. GITLAB_TOKEN / GITLAB_PAT / CI_JOB_TOKEN environment variables.
//  2. glab CLI ("glab auth status --hostname <host> --show-token").
//  3. Unauthenticated fallback.
//
// host should be "gitlab.com" or a self-hosted hostname.
func DetectAuth(ctx context.Context, host string) (forge.AuthInfo, error) {
	base := forge.AuthInfo{
		Provider: forge.ProviderGitLab,
		Host:     host,
		RateUnit: "minute",
	}

	// 1. Environment variable.
	for _, key := range []string{"GITLAB_TOKEN", "GITLAB_PAT", "CI_JOB_TOKEN"} {
		tok := os.Getenv(key)
		if tok == "" {
			continue
		}
		username, err := resolveUsername(ctx, host, tok)
		if err != nil {
			slog.Warn("GitLab token found but validation failed; trying next source",
				"env_key", key,
				"host", host,
				"error", err,
			)
			continue
		}
		slog.Info("GitLab auth detected via environment variable",
			"env_key", key,
			"host", host,
			"username", username,
		)
		return forge.AuthInfo{
			Provider:    forge.ProviderGitLab,
			Tier:        forge.AuthToken,
			Host:        host,
			Username:    username,
			Concurrency: concurrencyAuthed,
			RateLimit:   rateLimitAuthed,
			RateUnit:    "minute",
		}, nil
	}

	// 2. glab CLI.
	if info, ok := detectGlab(ctx, host); ok {
		slog.Info("GitLab auth detected via glab CLI",
			"host", host,
			"username", info.username,
		)
		return forge.AuthInfo{
			Provider:    forge.ProviderGitLab,
			Tier:        forge.AuthCLI,
			Host:        host,
			Username:    info.username,
			Concurrency: concurrencyAuthed,
			RateLimit:   rateLimitAuthed,
			RateUnit:    "minute",
		}, nil
	}

	// 3. Unauthenticated fallback.
	slog.Warn("no GitLab credentials found; using unauthenticated access",
		"host", host,
		"rate_limit_per_min", rateLimitUnauthed,
		"concurrency", concurrencyUnauthed,
	)
	base.Tier = forge.AuthNone
	base.Concurrency = concurrencyUnauthed
	base.RateLimit = rateLimitUnauthed
	return base, nil
}

// TokenFromAuth extracts the token from detected auth info by re-checking env/glab.
// Returns empty string for unauthenticated.
func TokenFromAuth(ctx context.Context, host string) string {
	for _, key := range []string{"GITLAB_TOKEN", "GITLAB_PAT", "CI_JOB_TOKEN"} {
		tok := os.Getenv(key)
		if tok != "" {
			return tok
		}
	}
	if info, ok := detectGlab(ctx, host); ok {
		return info.token
	}
	return ""
}

// glabInfo holds the parsed output of "glab auth status".
type glabInfo struct {
	username string
	token    string
}

// detectGlab runs "glab auth status" and parses its human-readable output.
func detectGlab(ctx context.Context, host string) (glabInfo, bool) {
	cmd := exec.CommandContext(ctx, "glab", "auth", "status", "--hostname", host, "--show-token")
	out, err := cmd.Output()
	if err != nil {
		slog.Debug("glab auth status unavailable", "host", host, "error", err)
		return glabInfo{}, false
	}

	var info glabInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, " as "); idx != -1 {
			after := line[idx+4:]
			if sp := strings.IndexByte(after, ' '); sp != -1 {
				info.username = after[:sp]
			} else {
				info.username = after
			}
		}
		if after, found := strings.CutPrefix(line, "Token: "); found {
			info.token = strings.TrimSpace(after)
		}
	}

	if info.username == "" {
		slog.Debug("glab auth status: could not parse username", "host", host, "output", string(out))
		return glabInfo{}, false
	}
	return info, true
}

// resolveUsername calls GET /api/v4/user to verify the token and return the
// authenticated username.
func resolveUsername(ctx context.Context, host, token string) (string, error) {
	apiURL := fmt.Sprintf("https://%s/api/v4/user", host)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Accept", "application/json")

	cl := &http.Client{Timeout: 10 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", apiURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned %d (token may be invalid or lack api scope)", apiURL, resp.StatusCode)
	}

	var user struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("decode /api/v4/user response: %w", err)
	}
	if user.Username == "" {
		return "", fmt.Errorf("GET %s: empty username in response", apiURL)
	}
	return user.Username, nil
}
