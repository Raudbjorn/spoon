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
	HeadSHA       string // upstream default-branch tip SHA; powers the MDG cache
	Stars         int
	Forks         int
	Size          int
	PushedAt      time.Time
	URL           string
	Language      string
	Topics        []string
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

	// DivergentBranches counts this fork's branches holding at least one commit
	// the upstream lacks. Nil means the count was never obtained (provider
	// cannot supply it, the sweep failed, or the fork was unresolvable) and must
	// render as unknown rather than as zero — a real 0 means "checked, nothing
	// diverges anywhere".
	DivergentBranches *int

	// BranchFingerprint identifies this fork's divergent work by the tip OIDs of
	// its ahead-of-upstream branches. Two forks sharing a non-empty fingerprint
	// carry byte-identical work — one fork re-forked, or both branched from the
	// same point. Empty means unknown or nothing divergent, and never groups.
	BranchFingerprint string

	// Fork lineage
	SourceFullPath string // network root used for compare baseline; never the direct parent.
	ParentFullPath string // direct parent
	IsForkOfFork   bool

	// Topics is the repository's topic set as returned by the provider.
	// Empty/nil means "no signal" (e.g. provider lacks topic support, or
	// the topic fetch was skipped by the rate-budget). The P1 penalty in
	// internal/heat reads both Fork.Topics and Parent.Topics.
	Topics []string

	// OwnerProfile is the owner-farmer signal (P3). Populated by the fork
	// pipeline after a GitHub owner-history fetch (capped at 30 distinct
	// owners per run). Nil means "no signal" — see OwnerProfile's doc.
	OwnerProfile *OwnerProfile
}

// OwnerProfile is the owner-farmer signal (P3). Populated by the fork
// pipeline after a GitHub owner-history fetch, with a hard cap of 30
// distinct owners per run. Nil means "no signal": the fetch was
// skipped (rate cap reached), failed (404/403), or the provider does
// not implement the owner-profile path (GitLab, Gitea). Consumers
// must treat nil as "no penalty", not as "owner is a farmer".
type OwnerProfile struct {
	Login            string
	TotalPublicRepos int
	ForkCount        int
	SignalForkCount  int // forks pushed within 1 year AND fork: true
	NonForkRepoCount int // public non-fork repos
	FetchedAt        time.Time
}

// FileDiff is a single file's change statistics from a compare call.
type FileDiff struct {
	Path         string
	PreviousPath string
	Status       string
	Additions    int
	Deletions    int
	Patch        string
	PatchSource  string
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
	// Performed reports whether the comparison actually ran against the
	// upstream. It is false when the compare could not be carried out at all
	// (fork deleted, made private, DMCA'd, or the upstream baseline was never
	// resolved). The zero value is deliberately "not performed": every other
	// field on a !Performed T2Data is meaningless, and treating it as a real
	// "0 ahead, 0 behind, identical" result is what made a whole fork list
	// render as heat 0. Callers must check this before persisting, scoring, or
	// displaying divergence.
	Performed          bool
	AheadCount         int
	BehindCount        int
	MNA                int     // Meaningful Net Additions -- junk/generated stripped.
	TotalAdditions     int     // raw, before filtering
	TotalDeletions     int     // raw, before filtering
	FeatureCommitRatio float64 // fraction of non-merge, non-sync commits
	IsBranchWork       bool    // true when significant work is on a non-default branch
	ActiveBranch       string  // non-empty when IsBranchWork == true
	Upstreamed         bool    // true when the active branch tip heads a merged upstream PR (work already integrated)
	UpstreamedPR       int     // the merged upstream PR number when Upstreamed == true
	BaseSHA            string  // merge-base commit used for the comparison
	HeadSHA            string  // resolved tip of the compared fork branch
	Diffs              []FileDiff
	Commits            []AheadCommit // used by the T3 lone-wolf gate
	PatchSkipReason    string
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

// CommitFileProvider is an optional provider capability for per-commit file
// attribution. Providers that do not implement it remain valid Forge values.
type CommitFileProvider interface {
	CommitFiles(context.Context, T1Data, string) ([]FileDiff, error)
}

// BranchDivergenceProvider is an optional provider capability for counting,
// per fork, the branches carrying commits the upstream lacks. Implementations
// are expected to answer for the whole batch in bounded API cost; a provider
// that would need one request per branch should simply not implement it.
//
// A fork absent from the returned map was not resolved, which is distinct from
// a present zero.
type BranchDivergenceProvider interface {
	// Returns per-fork divergent-branch counts and per-fork work fingerprints.
	// Forks sharing a non-empty fingerprint carry byte-identical work.
	// truncated lists fork IDs whose branch list was too large to enumerate in
	// full, so their counts (and any fingerprint derived from them) are lower
	// bounds rather than exact.
	DivergentBranchCounts(ctx context.Context, forks []T1Data) (counts map[string]int, fingerprints map[string]string, truncated []string, err error)
}

// CompareBaselineSetter is an optional provider capability for restoring the
// upstream baseline that Parent() would normally establish.
//
// Compare needs to know which upstream to compare against, and providers latch
// that from Parent(). A caller that serves the fork list from a local cache
// never calls Parent(), so without this the baseline stays empty and every
// subsequent Compare is issued against a malformed upstream. Providers that do
// not implement it remain valid Forge values.
type CompareBaselineSetter interface {
	SetCompareBaseline(owner, repo, defaultBranch string)
}
type TopicLane string

const (
	TopicLaneDefault TopicLane = "default"
	TopicLaneStars   TopicLane = "stars"
	TopicLaneUpdated TopicLane = "updated"
	TopicLaneForks   TopicLane = "forks"
)

// TopicRepo is a repository carrying a forge topic, as returned by a
// provider's topic search (see topics.TopicSearcher).
type TopicRepo struct {
	FullName    string
	Description string
	Language    string
	Stars       int
	ForkCount   int
	PushedAt    time.Time
	Archived    bool
}
