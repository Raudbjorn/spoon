package gitea

import (
	"context"
	"os"

	"github.com/svnbjrn/spoon/internal/forge"
)

// tokenEnvVars are checked in order for a Gitea/Forgejo API token. Codeberg's
// public API works unauthenticated (with lower limits), so a token is optional.
var tokenEnvVars = []string{"FORGEJO_TOKEN", "CODEBERG_TOKEN", "GITEA_TOKEN"}

// DetectToken returns the first non-empty token from tokenEnvVars, or "".
func DetectToken() string {
	for _, k := range tokenEnvVars {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// DetectAuth probes the instance and returns AuthInfo. With a token it confirms
// the user via /user; without one it falls back to unauthenticated public
// access. A failed token probe degrades to unauthenticated rather than erroring,
// so a stale env token never blocks a public read.
func DetectAuth(ctx context.Context, host string) (forge.AuthInfo, *Client) {
	token := DetectToken()
	client := NewClient(host, token)

	info := forge.AuthInfo{
		Provider:    forge.ProviderGitea,
		Host:        host,
		Tier:        forge.AuthNone,
		Concurrency: 4,
		RateUnit:    "minute",
	}

	if token != "" {
		var u gtUser
		if _, err := client.Get(ctx, "/user", nil, &u); err == nil {
			info.Tier = forge.AuthToken
			info.Username = u.Login
			return info, client
		}
		// Token didn't validate — drop it and continue unauthenticated.
		client = NewClient(host, "")
	}
	// Unauthenticated: be gentle on the shared instance.
	info.Concurrency = 2
	return info, client
}
