package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// maxContentBytes caps the returned file content. Anything larger is truncated
// at a UTF-8 rune boundary. Mirrors maxReadmeBytes but is significantly larger
// — code files can legitimately reach hundreds of KB.
const maxContentBytes = 1024 * 1024

// contentsResponse mirrors GitHub's repo contents endpoint shape.
type contentsResponse struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int    `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// FetchFileContent returns the decoded text of a file at the given ref.
// Calls GET /repos/{owner}/{repo}/contents/{path}?ref={ref}.
//
// Returns ("", nil) on 404 (file deleted/renamed at this ref).
// Returns ("", nil) on 403 (private fork / rate-limited; reuse isForbidden helper).
// Truncates to 1 MB at a UTF-8 rune boundary.
// encoding field must be "base64"; anything else is an error.
func (c *Client) FetchFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/contents/%s", owner, repo, path)
	if ref != "" {
		endpoint = fmt.Sprintf("%s?ref=%s", endpoint, ref)
	}
	resp, err := c.GetRaw(ctx, endpoint)
	if err != nil {
		// 404 (file missing) and 403 (private / rate-limited) both surface as
		// "unavailable, not broken" — callers treat both as "no content" and
		// skip the code-context rendering gracefully.
		if isNotFound(err) || isForbidden(err) {
			return "", nil
		}
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading contents response: %w", err)
	}

	var r contentsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("parsing contents response: %w", err)
	}

	if r.Encoding != "base64" {
		return "", fmt.Errorf("unexpected contents encoding %q (want base64)", r.Encoding)
	}

	// Strip embedded newlines that the GitHub API includes in the base64 payload.
	cleaned := strings.ReplaceAll(r.Content, "\n", "")
	cleaned = strings.ReplaceAll(cleaned, "\r", "")

	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return "", fmt.Errorf("decode contents base64: %w", err)
	}

	return truncateUTF8(decoded, maxContentBytes), nil
}

// ExtractLines returns lines [start, end] inclusive from the given text.
// Lines are 1-indexed. Returns an empty slice if start > total lines.
// If start < 1, clamps to 1. If end > total lines, clamps to total.
func ExtractLines(content string, start, end int) []string {
	if content == "" {
		return nil
	}
	// strings.Split("a\nb\n", "\n") returns ["a", "b", ""] — the trailing empty
	// element shouldn't be treated as a content line. Trim it iff content
	// ended on a newline.
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	total := len(lines)
	if total == 0 {
		return nil
	}
	if start < 1 {
		start = 1
	}
	if start > total {
		return nil
	}
	if end > total {
		end = total
	}
	if end < start {
		return nil
	}
	// Both endpoints are 1-indexed inclusive; slice [start-1, end] is exclusive at end.
	out := make([]string, end-start+1)
	copy(out, lines[start-1:end])
	return out
}
