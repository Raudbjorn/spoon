package forge

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

// ParseRepoURL parses a repository URL or shorthand and returns the detected
// Provider, host, owner, and repo name.
//
// Accepted input formats:
//   - https://github.com/owner/repo
//   - https://gitlab.com/group/subgroup/repo   (nested GitLab groups)
//   - https://gitlab.example.com/owner/repo    (self-hosted)
//   - github.com/owner/repo                    (no scheme)
//   - owner/repo                               (resolved via defaultHost)
//
// forceProvider overrides the heuristic when non-zero (pass 0 to auto-detect).
func ParseRepoURL(raw, defaultHost string, forceProvider Provider) (provider Provider, host, owner, repo string, err error) {
	input := raw

	// Normalise scheme-less input. The first path segment decides whether this is
	// shorthand ("owner/repo") or a host-prefixed URL ("github.com/owner/repo").
	// A host segment contains a "." (domain) or ":" (port); an owner segment does
	// not. Checking only the first segment — rather than the whole string — means
	// a repo name with a dot (e.g. "ggml-org/llama.cpp") is still treated as
	// shorthand instead of being mistaken for a hostname.
	if !strings.Contains(raw, "://") {
		firstSeg := raw
		if i := strings.IndexByte(raw, '/'); i >= 0 {
			firstSeg = raw[:i]
		}
		if strings.ContainsAny(firstSeg, ".:") {
			input = "https://" + raw
		} else {
			input = "https://" + defaultHost + "/" + raw
		}
	}

	u, parseErr := url.Parse(input)
	if parseErr != nil {
		return 0, "", "", "", fmt.Errorf("parse repo URL %q: %w", raw, parseErr)
	}

	host = strings.ToLower(u.Hostname())
	if host == "" {
		host = defaultHost
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case len(parts) < 2:
		return 0, "", "", "", fmt.Errorf("repo URL %q: path must contain at least owner/repo", raw)
	case len(parts) == 2:
		owner, repo = parts[0], parts[1]
	default:
		// Nested groups: last segment is the repo; everything before is the namespace.
		repo = parts[len(parts)-1]
		owner = strings.Join(parts[:len(parts)-1], "/")
	}
	repo = strings.TrimSuffix(repo, ".git")

	switch {
	case forceProvider != 0:
		provider = forceProvider
	case host == "github.com":
		provider = ProviderGitHub
	case host == "gitlab.com":
		provider = ProviderGitLab
	case strings.Contains(host, "gitlab"):
		provider = ProviderGitLab
	default:
		provider = ProviderGitHub
		slog.Warn("forge provider ambiguous for custom host; defaulting to GitHub -- use --forge gitlab to override",
			"host", host,
		)
	}

	return provider, host, owner, repo, nil
}
