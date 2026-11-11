package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	ghauth "github.com/cli/go-gh/v2/pkg/auth"
)

func CheckAuth() (*Client, AuthStatus, error) {
	return CheckAuthWithOptions(ClientOptions{})
}

// CheckAuthWithOptions constructs and probes every explicit identity before it
// becomes dispatchable. Tokens resolving to the same GitHub login share a
// primary budget and are therefore collapsed to one backend.
func CheckAuthWithOptions(opts ClientOptions) (*Client, AuthStatus, error) {
	client, err := NewClientWithOptions(opts)
	if err != nil {
		return nil, AuthStatus{}, err
	}
	host, _, _ := githubEnvironmentAuth()
	status := AuthStatus{
		Authenticated: client.IsAuthenticated(),
		Host:          host,
		TokenSource:   client.tokenSource,
	}
	if len(opts.Tokens) > 0 {
		if err := client.probeExplicitBackends(context.Background(), &status); err != nil {
			return nil, AuthStatus{}, err
		}
	} else {
		client.probeDefaultBackend(context.Background(), &status)
	}
	client.duplicateIdentities = status.DuplicateIdentities
	status.RateLimit = client.GetRateLimit()
	status.AuthScopeID = client.authScopeID
	status.APIVersion = StoredAPIVersion
	status.AuthMode = client.AuthMode()
	return client, status, nil
}

func (c *Client) probeExplicitBackends(ctx context.Context, status *AuthStatus) error {
	probed := make([]*backend, 0, len(c.backends))
	for _, b := range c.backends {
		login, err := c.probeLoginWithRetry(ctx, b)
		if err != nil || login == "" {
			// Permanent is documented as a confirmed auth failure and is never
			// rehabilitated, so reserve it for an actual rejection. A transient
			// 500, gateway error or dropped connection must not burn a valid
			// token for the life of the process.
			if isAuthRejection(err) {
				c.pool.disableUntil(b, time.Time{}, true)
			}
			continue
		}
		b.Login = login
		probed = append(probed, b)
	}
	unique, duplicates := dedupeBackendsByLogin(probed)
	status.DuplicateIdentities = duplicates
	for _, b := range unique {
		c.probeRateLimit(ctx, b, status)
	}
	if len(unique) == 0 {
		return fmt.Errorf("no configured GitHub identity passed /user and /rate_limit probes")
	}
	c.installBackends(unique)
	c.authenticated = true
	return nil
}

func dedupeBackendsByLogin(backends []*backend) ([]*backend, int) {
	unique := make([]*backend, 0, len(backends))
	seen := make(map[string]bool, len(backends))
	duplicates := 0
	for _, b := range backends {
		key := strings.ToLower(strings.TrimSpace(b.Login))
		if seen[key] {
			b.Disabled = true
			duplicates++
			continue
		}
		seen[key] = true
		unique = append(unique, b)
	}
	return unique, duplicates
}

// isAuthRejection reports whether err is GitHub refusing the credential itself,
// as opposed to a transient failure. A 403 carrying rate-limit headers is
// exhaustion, not rejection, and must stay recoverable.
func isAuthRejection(err error) bool {
	if err == nil {
		return false
	}
	if detectRateLimitFromHTTPError(err) != nil {
		return false
	}
	code := statusCode(err)
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

// probeLoginWithRetry retries the identity probe on transient failures. Startup
// is exactly when a proxy blip is most likely, and dropping a token here removes
// it from the pool for the whole run.
func (c *Client) probeLoginWithRetry(ctx context.Context, b *backend) (string, error) {
	const attempts = 3
	var lastErr error
	for attempt := range attempts {
		login, err := c.probeLogin(ctx, b)
		if err == nil {
			return login, nil
		}
		lastErr = err
		if isAuthRejection(err) {
			return "", err
		}
		if attempt < attempts-1 {
			if serr := proxyBackoff(ctx, c.sleep(), attempt); serr != nil {
				return "", serr
			}
		}
	}
	return "", lastErr
}

func (c *Client) probeLogin(ctx context.Context, b *backend) (string, error) {
	if err := c.waitRequest(ctx, b.REST.Limiter); err != nil {
		return "", err
	}
	resp, err := restGet(ctx, b.Rest, "user", "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return "", fmt.Errorf("decode GitHub user probe: %w", err)
	}
	return strings.TrimSpace(user.Login), nil
}

func (c *Client) probeRateLimit(ctx context.Context, b *backend, status *AuthStatus) {
	if err := c.waitRequest(ctx, b.REST.Limiter); err != nil {
		return
	}
	resp, err := restGet(ctx, b.Rest, "rate_limit", "")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var envelope struct {
		Resources struct {
			Core struct {
				Limit, Remaining, Used int
				Reset                  int64
			} `json:"core"`
		} `json:"resources"`
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr == nil && json.Unmarshal(body, &envelope) == nil {
		core := envelope.Resources.Core
		b.REST.RateLimit = RateLimit{Limit: core.Limit, Remaining: core.Remaining, Used: core.Used, Reset: time.Unix(core.Reset, 0)}
		b.REST.Limiter.SetRPM(restRPM(b))
	}
	for _, raw := range strings.Split(resp.Header.Get("X-OAuth-Scopes"), ",") {
		if scope := strings.TrimSpace(raw); scope != "" {
			status.Scopes = append(status.Scopes, scope)
		}
	}
}

func (c *Client) probeDefaultBackend(ctx context.Context, status *AuthStatus) {
	if len(c.backends) == 0 {
		return
	}
	c.probeRateLimit(ctx, c.backends[0], status)
}

// githubEnvironmentAuth does not consult gh: the caller must first give saved
// Spoon tokens and environment credentials priority over gh's active login.
func githubEnvironmentAuth() (host, token, source string) {
	host = strings.TrimSpace(os.Getenv("GH_HOST"))
	if host == "" {
		host = defaultHost
	}
	keys := []string{"GH_TOKEN", "GITHUB_TOKEN"}
	if ghauth.IsEnterprise(host) {
		keys = []string{"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
	}
	for _, key := range keys {
		if token = strings.TrimSpace(os.Getenv(key)); token != "" {
			return host, token, key
		}
	}
	return host, "", "none"
}

const ghAuthLookupTimeout = 5 * time.Second

// githubCLIToken lets gh read its active account from either config or keyring.
// Bound the subprocess so a locked keyring cannot hold startup indefinitely.
func githubCLIToken(ctx context.Context, host string) string {
	ctx, cancel := context.WithTimeout(ctx, ghAuthLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", host)
	// This fallback must read the saved login, even when an environment token
	// was present but blank after trimming.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.WaitDelay = time.Second
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		slog.Debug("github: gh credential lookup unavailable", "host", host, "error", err)
		return ""
	}
	return strings.TrimSpace(string(out))
}
