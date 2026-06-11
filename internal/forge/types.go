// Package forge defines the provider-agnostic interface that both the GitHub
// and GitLab packages implement. The heat, tui, and cmd packages depend only
// on this package -- never on github or gitlab directly.
package forge

import (
	"context"
	"time"
)

// Provider identifies the hosting forge.
type Provider int

const (
	ProviderGitHub Provider = iota
	ProviderGitLab
	// ProviderGitea covers Gitea and its fork Forgejo (e.g. codeberg.org). They
	// share the /api/v1 REST surface, so one provider serves both.
	ProviderGitea
)

func (p Provider) String() string {
	switch p {
	case ProviderGitHub:
		return "github"
	case ProviderGitLab:
		return "gitlab"
	case ProviderGitea:
		return "gitea"
	default:
		return "unknown"
	}
}

// AuthTier describes the capability level of the detected credentials.
type AuthTier int

const (
	AuthNone  AuthTier = iota // Public API only.
	AuthToken                 // PAT / env-var token.
	AuthCLI                   // gh / glab CLI is authenticated.
)

// AuthInfo is the result of startup credential detection.
type AuthInfo struct {
	Provider    Provider
	Tier        AuthTier
	Host        string // "github.com", "gitlab.com", or custom hostname.
	Username    string // empty if unauthenticated.
	Concurrency int    // recommended worker pool size.
	RateLimit   int    // max requests per RateUnit.
	RateUnit    string // "hour" (GitHub) or "minute" (GitLab).
}

// Authenticated returns true if the user has any credentials.
func (a AuthInfo) Authenticated() bool {
	return a.Tier > AuthNone
}

// BranchRef is a branch name and the timestamp of its most-recent commit.
type BranchRef struct {
	Name          string
	CommittedDate time.Time
}

// ParentData holds metadata about the parent (upstream) repository.
type ParentData struct {
	FullName      string
	Description   string
	DefaultBranch string
	Stars         int
	Forks         int
	Size          int
	PushedAt      time.Time
	URL           string
	Language      string
}

// T1Data is the surface data available immediately after fork discovery,
// without comparing the fork to its upstream.
type T1Data struct {
	// Identity
	ID            string // opaque provider key: nameWithOwner (GH) / fullPath (GL)
	Owner         string
	Name          string
	URL           string
	DefaultBranch string

	// Surface metrics
	Stars        int
	PushedAt     time.Time
	IsArchived   bool
	SubForkCount int
	OpenPRCount  int // MRs on GitLab; PRs on GitHub.
	ReleaseCount int

	// Metadata (used by TUI detail/export/scoring)
	Description string
	Size        int
	Language    string
	OpenIssues  int
	CreatedAt   time.Time

	// Top branches by commit date (populated at T1 for GitHub via GraphQL;
	// populated lazily via Branches() for GitLab).
	Branches []BranchRef

	// Fork lineage
	SourceFullPath string // network root used for compare baseline; never the direct parent.
	ParentFullPath string // direct parent
	IsForkOfFork   bool
}

// FileDiff is a single file's change statistics from a compare call.
type FileDiff struct {
	Path      string
	Additions int
	Deletions int
}

// AheadCommit is a commit present in the fork but not in the upstream.
type AheadCommit struct {
	SHA         string
	Message     string
	AuthorEmail string
	AuthorLogin string // may be empty (GitLab doesn't always return login)
	Timestamp   time.Time
	Files       []FileDiff // per-commit file changes; may be nil if not fetched at this tier
}

// T2Data is code-divergence data from comparing the fork to its upstream source.
type T2Data struct {
	AheadCount         int
	BehindCount        int
	MNA                int     // Meaningful Net Additions -- junk/generated stripped.
	TotalAdditions     int     // raw, before filtering
	TotalDeletions     int     // raw, before filtering
	FeatureCommitRatio float64 // fraction of non-merge, non-sync commits
	IsBranchWork       bool    // true when significant work is on a non-default branch
	ActiveBranch       string  // non-empty when IsBranchWork == true
	Diffs              []FileDiff
	Commits            []AheadCommit // used by the T3 lone-wolf gate
}

// Contributor is a single contributor to a fork.
type Contributor struct {
	Login       string // may be email-derived on GitLab
	Email       string
	CommitCount int
	Additions   int
	Deletions   int
}

// T3Data is contributor statistics for a fork.
type T3Data struct {
	Contributors   []Contributor
	CommitSpanDays int // computed by the worker from T2.Commits timestamps
}

// ForkMsg is a result item on the channel returned by ListForks.
type ForkMsg struct {
	Fork T1Data
	Err  error // non-nil means this item is an error; Fork is zero.
}

// Forge is the interface both GitHub and GitLab packages implement.
// All implementations must be safe for concurrent use.
type Forge interface {
	// Auth detects and returns credential information. Called once at startup.
	Auth(ctx context.Context) (AuthInfo, error)

	// Parent returns metadata about the upstream repository.
	Parent(ctx context.Context, owner, repo string) (ParentData, error)

	// ListForks streams T1Data for all forks of owner/repo.
	// The channel is closed when all pages are exhausted or ctx is cancelled.
	ListForks(ctx context.Context, owner, repo string) (<-chan ForkMsg, error)

	// Branches returns the top n branches for fork sorted by commit date, descending.
	Branches(ctx context.Context, fork T1Data, n int) ([]BranchRef, error)

	// Compare returns code-divergence data between fork@branch and the network source.
	Compare(ctx context.Context, fork T1Data, branch string) (T2Data, error)

	// Contributors returns contributor statistics for a fork.
	Contributors(ctx context.Context, fork T1Data) (T3Data, error)

	// Headroom returns the current rate-limit headroom in [0.0, 1.0].
	Headroom() float64
}
