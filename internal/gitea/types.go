// Package gitea implements forge.Forge for Gitea and its fork Forgejo
// (codeberg.org) over the shared /api/v1 REST surface.
//
// Gitea has two API limitations spoon works around:
//
//   - Cross-repo compare is broken/unsupported (the upstream/fork compare 500s
//     on codeberg.org). So divergence is computed with a merge-base walk —
//     cheap commit-existence checks against upstream find the fork's base, then
//     INTRA-repo compares (which work) yield the ahead/behind/diff data. See
//     compare.go.
//   - There is no contributors-stats endpoint. T3 is derived from the fork's
//     commit list instead. See contributors.go.
package gitea

import "time"

// gtRepo is a Gitea repository object (/repos/{owner}/{repo} and fork lists).
type gtRepo struct {
	FullName      string    `json:"full_name"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	DefaultBranch string    `json:"default_branch"`
	StarsCount    int       `json:"stars_count"`
	ForksCount    int       `json:"forks_count"`
	OpenIssues    int       `json:"open_issues_count"`
	OpenPRs       int       `json:"open_pr_counter"`
	Size          int       `json:"size"`
	Language      string    `json:"language"`
	Archived      bool      `json:"archived"`
	HTMLURL       string    `json:"html_url"`
	UpdatedAt     time.Time `json:"updated_at"`
	CreatedAt     time.Time `json:"created_at"`
	Fork          bool      `json:"fork"`
	Parent        *gtRepo   `json:"parent"`
	Owner         gtUser    `json:"owner"`
}

// gtUser is the minimal owner/author shape.
type gtUser struct {
	Login string `json:"login"`
	Email string `json:"email"`
}

// gtBranch is one entry from /repos/{o}/{r}/branches.
type gtBranch struct {
	Name   string         `json:"name"`
	Commit gtBranchCommit `json:"commit"`
}

type gtBranchCommit struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
}

// gtCommit is one entry from /repos/{o}/{r}/commits (list).
type gtCommit struct {
	SHA    string         `json:"sha"`
	Commit gtCommitDetail `json:"commit"`
	Author *gtUser        `json:"author"` // may be null when the email isn't a known user
}

type gtCommitDetail struct {
	Message   string         `json:"message"`
	Author    gtCommitAuthor `json:"author"`
	Committer gtCommitAuthor `json:"committer"`
}

type gtCommitAuthor struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

// gtCompare is the response from /repos/{o}/{r}/compare/{base}...{head}. The
// files array carries filenames + status only (no per-file counts), so per-file
// additions/deletions come from parsing the .diff (see compare.go).
type gtCompare struct {
	TotalCommits int         `json:"total_commits"`
	Commits      []gtCommit  `json:"commits"`
	Files        []gtCmpFile `json:"files"`
}

type gtCmpFile struct {
	Filename string `json:"filename"`
	Status   string `json:"status"`
}
