package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// maxReadmeBytes caps the returned README content. Anything larger is truncated
// at a UTF-8 rune boundary.
const maxReadmeBytes = 64 * 1024

// readmeResponse mirrors the shape of GitHub's repo readme endpoint.
type readmeResponse struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int    `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// FetchReadme returns the contents of a repository's README on the default
// branch. Tries the GitHub readme API endpoint (which auto-resolves common
// names like README, README.md, README.rst) and returns the decoded content.
//
// Returns ("", nil) on 404 (no README) — callers should treat this as
// "no README available" rather than an error.
//
// Returns ("", err) on any other API failure (auth, rate limit, network).
//
// Gated by the caller, who should check c.Headroom() > some threshold
// before invoking. FetchReadme itself does NOT check headroom; it just
// makes the call.
//
// Max returned content: 64 KB. If the upstream is larger, it's truncated
// at a UTF-8 rune boundary with no error.
func (c *Client) FetchReadme(ctx context.Context, owner, repo string) (string, error) {
	path := fmt.Sprintf("repos/%s/%s/readme", owner, repo)
	resp, err := c.GetRaw(ctx, path)
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading readme response: %w", err)
	}

	var r readmeResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("parsing readme response: %w", err)
	}

	if r.Encoding != "base64" {
		return "", fmt.Errorf("unexpected readme encoding %q (want base64)", r.Encoding)
	}

	// Strip embedded newlines that the GitHub API includes in the base64 payload.
	cleaned := strings.ReplaceAll(r.Content, "\n", "")
	cleaned = strings.ReplaceAll(cleaned, "\r", "")

	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return "", fmt.Errorf("decode readme base64: %w", err)
	}

	return truncateUTF8(decoded, maxReadmeBytes), nil
}

// truncateUTF8 returns at most n bytes of s, ending on a valid UTF-8 rune
// boundary. If s is already ≤ n bytes, it's returned unchanged.
func truncateUTF8(s []byte, n int) string {
	if len(s) <= n {
		return string(s)
	}
	// Back off until we land on a rune boundary. s[cut] is the first byte that
	// will NOT be included, so it must start a new rune (or we'd be splitting
	// one mid-sequence).
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return string(s[:cut])
}
