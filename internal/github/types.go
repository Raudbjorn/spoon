package github

import "time"

// RepoInfo represents a GitHub repository (used for the parent repo).
type RepoInfo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	Stars         int    `json:"stargazers_count"`
	Forks         int    `json:"forks_count"`
	OpenIssues    int    `json:"open_issues_count"`
	Size          int    `json:"size"`
	Language      string `json:"language"`
	Archived      bool   `json:"archived"`
	Disabled      bool   `json:"disabled"`
	PushedAt      string `json:"pushed_at"`
	CreatedAt     string `json:"created_at"`
	HTMLURL       string `json:"html_url"`

	Owner OwnerInfo `json:"owner"`
}

// ForkInfo represents a fork from the forks list endpoint.
type ForkInfo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	Stars         int    `json:"stargazers_count"`
	Forks         int    `json:"forks_count"`
	OpenIssues    int    `json:"open_issues_count"`
	Watchers      int    `json:"watchers_count"`
	Size          int    `json:"size"`
	Language      string `json:"language"`
	Archived      bool   `json:"archived"`
	Disabled      bool   `json:"disabled"`
	PushedAt      string `json:"pushed_at"`
	CreatedAt     string `json:"created_at"`
	HTMLURL       string `json:"html_url"`

	Owner OwnerInfo `json:"owner"`
}

// OwnerInfo represents the owner of a repo or fork.
type OwnerInfo struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

// CompareResult represents the response from the compare endpoint.
type CompareResult struct {
	Status       string       `json:"status"` // "ahead", "behind", "diverged", "identical"
	AheadBy      int          `json:"ahead_by"`
	BehindBy     int          `json:"behind_by"`
	TotalCommits int          `json:"total_commits"`
	Files        []FileChange `json:"files"`
	Commits      []Commit     `json:"commits"`
	HTMLURL      string       `json:"html_url"`
}

// FileChange represents a changed file in a compare response.
type FileChange struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"` // added, removed, modified, renamed, copied
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Changes   int    `json:"changes"`
}

// Commit represents a commit in a compare response.
type Commit struct {
	SHA       string     `json:"sha"`
	CommitDet CommitInfo `json:"commit"`
	Author    *OwnerInfo `json:"author"`
}

// CommitInfo is the inner commit object.
type CommitInfo struct {
	Author  CommitAuthor `json:"author"`
	Message string       `json:"message"`
}

// CommitAuthor is the author within a commit.
type CommitAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

// ContributorStats represents a contributor from the stats endpoint.
type ContributorStats struct {
	Author OwnerInfo          `json:"author"`
	Total  int                `json:"total"`
	Weeks  []ContributorWeek  `json:"weeks"`
}

// ContributorWeek is weekly contribution data.
type ContributorWeek struct {
	Week      int `json:"w"` // unix timestamp
	Additions int `json:"a"`
	Deletions int `json:"d"`
	Commits   int `json:"c"`
}

// RateLimit holds the current rate limit state.
type RateLimit struct {
	Limit     int
	Remaining int
	Reset     time.Time
	Used      int
}

// AuthStatus describes the authentication state.
type AuthStatus struct {
	Authenticated bool
	Host          string
	TokenSource   string // "gh", "env", "none"
	RateLimit     RateLimit
}

// T1Extra holds additional data from the GraphQL T1 query not in the REST ForkInfo.
type T1Extra struct {
	OpenPRCount  int
	ReleaseCount int
	TopBranches  []BranchInfo
}

// BranchInfo describes a branch with its last commit timestamp.
type BranchInfo struct {
	Name         string
	LastCommitAt string // RFC3339 timestamp
}
