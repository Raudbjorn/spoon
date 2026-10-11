package github

import "time"

// RepoInfo represents a GitHub repository (used for the parent repo).
// Source and Parent are populated only for forks (GitHub REST returns them
// only when Fork=true). For non-fork roots both are zero/nil.
type RepoInfo struct {
	ID            int64    `json:"id"`
	FullName      string   `json:"full_name"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	DefaultBranch string   `json:"default_branch"`
	Stars         int      `json:"stargazers_count"`
	Forks         int      `json:"forks_count"`
	OpenIssues    int      `json:"open_issues_count"`
	Size          int      `json:"size"`
	Language      string   `json:"language"`
	Archived      bool     `json:"archived"`
	Disabled      bool     `json:"disabled"`
	PushedAt      string   `json:"pushed_at"`
	CreatedAt     string   `json:"created_at"`
	HTMLURL       string   `json:"html_url"`
	Topics        []string `json:"topics"`
	Fork          bool     `json:"fork"`

	Owner OwnerInfo `json:"owner"`
	// Parent is the immediate forked-from repo. Nil when this repo is not a fork
	// or the API omitted the object.
	Parent *RepoRef `json:"parent,omitempty"`
	// Source is the ultimate network root. Nil when this repo is not a fork
	// or the API omitted the object.
	Source *RepoRef `json:"source,omitempty"`
}

// RepoRef is the nested repository object on GET /repos parent/source.
type RepoRef struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

// ForkInfo represents a fork from the forks list endpoint.
type ForkInfo struct {
	ID            int64    `json:"id"`
	FullName      string   `json:"full_name"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	DefaultBranch string   `json:"default_branch"`
	Stars         int      `json:"stargazers_count"`
	Forks         int      `json:"forks_count"`
	OpenIssues    int      `json:"open_issues_count"`
	Watchers      int      `json:"watchers_count"`
	Size          int      `json:"size"`
	Language      string   `json:"language"`
	Archived      bool     `json:"archived"`
	Disabled      bool     `json:"disabled"`
	PushedAt      string   `json:"pushed_at"`
	CreatedAt     string   `json:"created_at"`
	HTMLURL       string   `json:"html_url"`
	Topics        []string `json:"topics"`
	Fork          bool     `json:"fork"`

	Owner OwnerInfo `json:"owner"`
}

// OwnerInfo represents the owner of a repo or fork.
type OwnerInfo struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

// CompareResult represents the response from the compare endpoint.
type CompareResult struct {
	// Performed reports whether the comparison actually ran. False means the
	// compare could not be carried out (typically a 404: fork deleted, made
	// private, or DMCA'd) and every other field is meaningless. This is not a
	// GitHub API field; it is set by FetchCompare and persisted to the on-disk
	// cache so a cached entry cannot be mistaken for a real "identical".
	//
	// Cache entries written before this field existed unmarshal to false and
	// are therefore correctly treated as "never compared" rather than as a
	// fork with no divergence.
	Performed       bool         `json:"performed"`
	Status          string       `json:"status"` // "ahead", "behind", "diverged", "identical"
	AheadBy         int          `json:"ahead_by"`
	BehindBy        int          `json:"behind_by"`
	TotalCommits    int          `json:"total_commits"`
	Files           []FileChange `json:"files"`
	Commits         []Commit     `json:"commits"`
	BaseCommit      Commit       `json:"base_commit"`
	MergeBaseCommit Commit       `json:"merge_base_commit"`
	HTMLURL         string       `json:"html_url"`

	// BaseSHA/HeadSHA are resolved by the adapter rather than returned under
	// these names by the API, and are persisted so a cached compare keeps the
	// exact identity of the work. Without them a warm run silently loses the
	// SHA-grade sibling key and degrades to the fuzzy diff-shape fallback,
	// producing different grouping than the cold run that wrote the cache.
	BaseSHA string `json:"base_sha,omitempty"`
	HeadSHA string `json:"head_sha,omitempty"`

	// Divergence signals computed during enrichment (the upstreamed PR probe,
	// the branch scan, and MNA/feature-ratio derivation). Like BaseSHA/HeadSHA
	// these are not GitHub API fields; they are persisted so a warm run scores
	// and exports the same fork the same way as the cold run that wrote the
	// cache. Without them a cached compare rehydrated with zeros: the heat
	// score silently changed (MNA and the upstreamed penalty both feed it) and
	// the export dropped every one of these keys via omitempty.
	Upstreamed         bool    `json:"upstreamed,omitempty"`
	UpstreamedPR       int     `json:"upstreamed_pr,omitempty"`
	MNA                int     `json:"mna,omitempty"`
	FeatureCommitRatio float64 `json:"feature_commit_ratio,omitempty"`
	IsBranchWork       bool    `json:"is_branch_work,omitempty"`
	ActiveBranch       string  `json:"active_branch,omitempty"`

	// FetchedAt is this compare's own freshness timestamp, stamped by
	// SaveCompare. It exists so a compare write never has to touch
	// CacheEntry.FetchedAt (which governs the fork list's own TTL) in order to
	// record its own — see CompareValid and SaveCompare. Empty on entries
	// written before this field existed; CompareValid falls back to the
	// shared entry timestamp for those.
	FetchedAt string `json:"fetched_at,omitempty"`
}

// FileChange represents a changed file in a compare response.
type FileChange struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"` // added, removed, modified, renamed, copied
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Changes          int    `json:"changes"`
	Patch            string `json:"patch"`
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
	Author OwnerInfo         `json:"author"`
	Total  int               `json:"total"`
	Weeks  []ContributorWeek `json:"weeks"`
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
	Authenticated       bool
	Host                string
	TokenSource         string   // "config", "env", "none"
	Scopes              []string // OAuth scopes attached to the token (empty if unauthenticated)
	RateLimit           RateLimit
	DuplicateIdentities int    // configured tokens collapsed because they resolve to the same login
	AuthScopeID         string // non-reversible scope fingerprint (first 16 hex of SHA-256)
	APIVersion          string // pinned REST API version for storage (github/<date>). HTTP wire uses defaultRESTVersion.
	AuthMode            string // "authenticated" or "anonymous"
}

// T1Extra holds additional data from the GraphQL T1 query not in the REST ForkInfo.
type T1Extra struct {
	OpenPRCount  int
	ReleaseCount int
	TopBranches  []BranchInfo

	// DivergentBranches is the count of branches ahead of upstream. A pointer
	// so a cache entry written before the sweep ran is "unknown", not zero.
	DivergentBranches *int `json:"DivergentBranches,omitempty"`

	// BranchFingerprint identifies the fork's divergent work by its branch tip
	// OIDs; two forks sharing one carry identical work.
	BranchFingerprint string `json:"BranchFingerprint,omitempty"`

	// ForkCount is this fork's own fork count (a property of the node itself,
	// not the parent repository). Populated only on the GraphQL path.
	ForkCount int `json:"ForkCount,omitempty"`

	// DirectTotalCount is the parent's total fork count (forks.totalCount in
	// the GraphQL root). It is identical across every fork in a single run,
	// so it doubles as a "true total" sanity check against the streamed list.
	DirectTotalCount int `json:"DirectTotalCount,omitempty"`

	// WholeNetworkForkCount is the root repository's forkCount from
	// GraphQL — the whole-network count, not just direct children.
	WholeNetworkForkCount int `json:"WholeNetworkForkCount,omitempty"`

	// ParentFullPath is the direct parent's "owner/name". Empty when the
	// parent is unknown (REST path, missing parent payload, or root).
	ParentFullPath string `json:"ParentFullPath,omitempty"`

	// ParentDatabaseID is the direct parent's databaseId. Zero when unknown.
	ParentDatabaseID int64 `json:"ParentDatabaseID,omitempty"`

	// DirectParent is 1 when this fork's direct parent is the requested
	// root (depth 1), 0 otherwise. Zero is also the "unknown" value
	// (REST path, missing parent, cycle).
	DirectParent int `json:"DirectParent,omitempty"`

	// DepthFromRoot is the edge count from the requested root: 1=direct
	// child, 2=child of a fork, etc. Zero is the "unknown" value.
	DepthFromRoot int `json:"DepthFromRoot,omitempty"`

	// AuthMode is "authenticated" or "anonymous" for the run that produced
	// this record. Populated only on the GraphQL path.
	AuthMode string `json:"AuthMode,omitempty"`

	// APIVersion is the pinned REST version that backs this client. It is
	// informational only; the GraphQL endpoint does not negotiate versions.
	APIVersion string `json:"APIVersion,omitempty"`

	// DefaultTipSHA is the head commit SHA of the default branch, from the
	// listing query's defaultBranchRef.target.oid. Populated only on the
	// GraphQL path; empty on the REST path.
	DefaultTipSHA string `json:"DefaultTipSHA,omitempty"`
}

// BranchInfo describes a branch with its last commit timestamp.
type BranchInfo struct {
	Name         string
	LastCommitAt string // RFC3339 timestamp
	// TipSHA is the branch's head commit SHA, from the listing query's
	// target.oid. Populated only on the GraphQL path.
	TipSHA string
}

// HasScope reports whether the authenticated token includes the given OAuth scope.
// Note: GitHub treats `repo` as a superset of `public_repo`, etc. — this is a literal match.
func (s AuthStatus) HasScope(want string) bool {
	for _, sc := range s.Scopes {
		if sc == want {
			return true
		}
	}
	return false
}
