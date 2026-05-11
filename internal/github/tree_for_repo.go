package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// TreeEntry is one entry returned by GitHub's git tree endpoint.
type TreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // "blob" or "tree"
	SHA  string `json:"sha"`
	Size int    `json:"size,omitempty"`
}

// treeResponse mirrors the shape returned by /repos/{o}/{r}/git/trees/{sha}?recursive=1
type treeResponse struct {
	SHA       string      `json:"sha"`
	Tree      []TreeEntry `json:"tree"`
	Truncated bool        `json:"truncated"`
}

// FetchTree returns the recursive file list (blob paths only) at the named ref
// for the given repository, in the order returned by the GitHub API. This is
// the cheap proxy for "give me the full file tree of HEAD" — implementations
// of repo.TreeSource use it.
//
// `ref` may be a branch name, tag, or SHA. The endpoint resolves the ref to a
// tree SHA internally. Pass the default branch name (e.g., "main") for the
// canonical upstream tree.
//
// Returns ("", err) on 404 — the caller should treat this as "tree
// unavailable" and skip the centrality pass.
func (c *Client) FetchTree(ctx context.Context, owner, repo, ref string) ([]string, error) {
	path := fmt.Sprintf("repos/%s/%s/git/trees/%s?recursive=1", owner, repo, ref)
	resp, err := c.GetRaw(ctx, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading tree response: %w", err)
	}
	var r treeResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parsing tree response: %w", err)
	}
	out := make([]string, 0, len(r.Tree))
	for _, e := range r.Tree {
		if e.Type != "blob" {
			continue
		}
		if e.Path == "" {
			continue
		}
		out = append(out, e.Path)
	}
	return out, nil
}

// TreeSourceForRepo adapts a Client into a repo.TreeSource pinned to a single
// ref (typically the upstream default branch). The Ref field is used as the
// {sha} component of the GitHub tree URL — passing a branch name like "main"
// is supported; GitHub resolves it server-side.
//
// A new adapter is constructed per pipeline run because the ref depends on
// the parent's default branch, which is known only after Parent() resolves.
type TreeSourceForRepo struct {
	Client *Client
	Ref    string
}

// Tree implements repo.TreeSource.
func (t *TreeSourceForRepo) Tree(ctx context.Context, owner, repo string) ([]string, error) {
	if t == nil || t.Client == nil {
		return nil, fmt.Errorf("nil TreeSourceForRepo")
	}
	ref := t.Ref
	if ref == "" {
		ref = "HEAD"
	}
	return t.Client.FetchTree(ctx, owner, repo, ref)
}
