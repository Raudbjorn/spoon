package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/svnbjrn/spoon/internal/forge"
)

func (c *Client) FetchCommitFiles(ctx context.Context, owner, repo, sha string) ([]FileChange, error) {
	path := fmt.Sprintf("repos/%s/%s/commits/%s?per_page=100&page=1", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(sha))
	var files []FileChange
	err := c.GetPaginated(ctx, path, func(raw json.RawMessage) error {
		var page struct {
			Files []FileChange `json:"files"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("decode commit files: %w", err)
		}
		files = append(files, page.Files...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("commit files unavailable for %s: %w", sha, err)
	}
	return files, nil
}

func (p *GHProvider) CommitFiles(ctx context.Context, fork forge.T1Data, sha string) ([]forge.FileDiff, error) {
	files, err := p.client.FetchCommitFiles(ctx, fork.Owner, fork.Name, sha)
	if err != nil {
		return nil, err
	}
	out := make([]forge.FileDiff, 0, len(files))
	for _, file := range files {
		out = append(out, forge.FileDiff{
			Path: file.Filename, PreviousPath: file.PreviousFilename, Status: file.Status,
			Additions: file.Additions, Deletions: file.Deletions,
			Patch: file.Patch, PatchSource: "commit_rest",
		})
	}
	return out, nil
}
