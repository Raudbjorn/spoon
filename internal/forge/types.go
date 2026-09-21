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

// AcquisitionReport summarizes the acquisition metadata for a fork list run.
// It is emitted as a terminal message on the ForkMsg channel and copied to
// the caller-owned Report field in forksops.Options after the channel closes.
type AcquisitionReport struct {
	Method        string    `json:"method"`          // "graphql" | "graphql+rest" | "rest"
	Scope         string    `json:"scope"`           // "direct" in step 2
	APIVersion    string    `json:"apiVersion"`      // pinned REST version e.g. "2022-11-28"
	AuthMode      string    `json:"authMode"`        // "authenticated" | "anonymous"
	FallbackChain []string  `json:"fallbackChain"`   // e.g. ["graphql"] or ["graphql","rest"]
	Pages         int       `json:"pages"`           // upstream page callbacks (raw count)
	RawRows       int       `json:"rawRows"`         // total fork records before dedup
	UniqueRows    int       `json:"uniqueRows"`      // unique database IDs
	DuplicateRows int       `json:"duplicateRows"`   // raw - unique
	CaptureAt     time.Time `json:"captureAt"`       // when the report was generated
	AuthScopeID   string    `json:"authScopeId"`     // non-reversible; never logged with tokens
	Error         string    `json:"error,omitempty"` // set when REST fallback fails

	// VisitedNodes is the number of distinct repos the bounded traversal visited.
	VisitedNodes int    `json:"visitedNodes"`
	MaxNodes     int    `json:"maxNodes"`
	MaxDepth     int    `json:"maxDepth"`
	CapReason    string `json:"capReason,omitempty"`  // "max_nodes"|"max_depth"|"max_pages"|"max_elapsed"|""
	Unresolved   int    `json:"unresolved,omitempty"` // discovered nodes not visited
}

// BoundedOptions controls a bounded whole-network traversal.
type BoundedOptions struct {
	MaxNodes   int
	MaxDepth   int
	MaxPages   int
	MaxElapsed time.Duration
}

// ListForksBoundedProvider is an optional provider capability for bounded whole-network
// fork discovery. Providers that do not implement it remain valid Forge values.
type ListForksBoundedProvider interface {
	ListForksBounded(ctx context.Context, owner, repo string, opts BoundedOptions) (<-chan ForkMsg, error)
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
	Configured  bool   // Local credential material exists but has not been validated.
	Username    string // empty if unauthenticated.
	Concurrency int    // recommended worker pool size.
	RateLimit   int    // max requests per RateUnit.
	RateUnit    string // "hour" (GitHub) or "minute" (GitLab).
	// APIVersion carries the storage-prefixed pinned REST API version (e.g. "github/2022-11-28").
	// Empty for forges that do not expose this. NOT a wire value — HTTP headers must use
	// the unprefixed version (see internal/github.defaultRESTVersion).
	APIVersion string
	// AuthMode is "authenticated" or "anonymous" at the time of this run.
	AuthMode string
	// AuthScopeID is a non-reversible fingerprint of the credential set. Empty
	// when acquisition method does not support it. Safe to store and log.
	AuthScopeID string
}

// Authenticated returns true if the user has any credentials.
func (a AuthInfo) Authenticated() bool {
	return a.Tier > AuthNone
}

// BranchRef is a branch name and the timestamp of its most-recent commit.
type BranchRef struct {
	Name          string
	CommittedDate time.Time
	// TipSHA is the branch's head commit SHA, when the listing source
	// provided it (the GitHub GraphQL batch-divergence path). Empty when
	// unknown, e.g. on the REST listing path.
	TipSHA string
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
	// SourceFullPath is the network-root owner/repo Compare must use when the
	// named seed is a mid-chain fork. Empty means "same as FullName / unknown".
	SourceFullPath string
	// SourceDefaultBranch is that root's default branch. Empty means use DefaultBranch.
	SourceDefaultBranch string
	// DirectParentFullPath is the immediate parent's full name. Empty for non-forks.
	DirectParentFullPath string
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
	// DefaultTipSHA is the head commit SHA of DefaultBranch as returned by
	// the listing source. Populated on the GitHub GraphQL batch-divergence
	// path; empty on the REST listing path, where the tip SHA is not known
	// until Compare runs.
	DefaultTipSHA string

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
	SourceFullPath        string // network root used for compare baseline; never the direct parent.
	ParentFullPath        string // direct parent
	IsForkOfFork          bool
	DepthFromRoot         int // edges from root: 1=direct child, 0=unknown
	DirectTotalCount      int // root forks.totalCount (direct children only)
	WholeNetworkForkCount int // root forkCount (whole network)

	// Topics is the repository's topic set as returned by the provider.
	// Empty/nil means "no signal" (e.g. provider lacks topic support, or
	// the topic fetch was skipped by the rate-budget). The P1 penalty in
	// internal/heat reads both Fork.Topics and Parent.Topics.
	Topics []string

	// OwnerProfile is the owner-farmer signal (P3). Populated by the fork
	// pipeline from a fresh cached profile or a GitHub owner-history
	// fetch (live fetches capped at 30 distinct owners per run). Nil
	// means "no signal" — see OwnerProfile's doc.
	OwnerProfile *OwnerProfile
	// LinearHistory is true when the fork's tip is reachable from upstream's
	// tip without crossing a merge commit. False (or nil) when the fork has
	// at least one merge commit on top of its upstream baseline. Nil means
	// unknown -- the provider did not compute it for this fork.
	LinearHistory *bool
	// MergeCommits is the raw count of merge commits on the fork's default
	// branch up to the upstream tip. A negative value means unknown. Drives
	// the LinearHistory boolean and is exposed in the JSON export for
	// downstream tools that want a numeric signal.
	MergeCommits int
	// MergeCommitHistory is the raw parents-totalCount vector for every
	// commit on the fork's default branch up to the upstream tip. Kept so
	// downstream analyses (e.g. "how many forks rebased vs merged") do not
	// have to refetch. Persisted in the merge_commit_history table; nil on
	// T1Data in memory means unknown.
	MergeCommitHistory []int
	// MergeCommitTruncated is true when the MergeCommitHistory vector was
	// capped at the provider's page limit and is therefore a prefix, not
	// the full ahead-of-upstream history. The derived MergeCommits and
	// LinearHistory are lower bounds when this is true.
	MergeCommitTruncated bool
}

// OwnerProfile is the owner-farmer signal (P3). Populated by the fork
// pipeline after a GitHub owner-history fetch, with a hard cap of 30
// distinct owners per run. Nil means "no signal": the fetch was
// skipped (rate cap reached), failed (404/403), or the provider does
// not implement the owner-profile path (GitLab, Gitea). Consumers
// must treat nil as "no penalty", not as "owner is a farmer".
//
// The counts describe the repositories that were sampled, in
// SampleOrder. They describe the whole account only when Complete is
// true; a partial sample can show that an owner has original work but
// can never show that they have none.
type OwnerProfile struct {
	Login string
	// TotalPublicRepos is the number of repositories observed in the
	// sample, not GitHub's public_repos count.
	TotalPublicRepos int
	ForkCount        int
	SignalForkCount  int // forks pushed within 1 year AND fork: true
	NonForkRepoCount int // public non-fork repos
	// Complete is true when the whole account fit inside the sample.
	// False means partial or unknown, which includes profiles persisted
	// before this field existed.
	Complete bool
	// SampleOrder names the order the sample was drawn in, e.g.
	// "pushed_desc" (most recently pushed first).
	SampleOrder string
	FetchedAt   time.Time
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

// CompareFilesCap is the largest file list a single GitHub compare response
// carries (300). A T2 whose Diffs hit it may be missing files; see
// T2Data.FilesTruncated.
const CompareFilesCap = 300

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
	// FilesTruncated is true when the provider capped the file list (GitHub:
	// 300 entries per compare, unpaginated here). Diffs, TotalAdditions,
	// TotalDeletions and MNA are then lower bounds. Rows stored before this
	// field existed decode as false; readers must also treat
	// len(Diffs) >= CompareFilesCap as truncated.
	FilesTruncated bool
	// CompareSource records how this T2Data's divergence was obtained.
	// "" means the provider's ordinary per-fork REST Compare (today's
	// default path). "graphql_batch" means it was synthesised from a
	// BatchCompareProvider's ForkDivergence via SelectDivergentBranch
	// rather than a REST Compare call.
	CompareSource string
	// FilesComplete is true only when a diff-fallback pass confirmed the
	// full file list despite Diffs sitting at CompareFilesCap -- e.g. a
	// compare with exactly CompareFilesCap real files, backfilled and
	// verified complete. False (the zero value, and every row written
	// before this field existed) means no such fallback ran, or it could
	// not confirm completeness; see IsFilesTruncated.
	FilesComplete bool
	// FilesTruncatedReason explains why a capped file list stayed capped
	// after a diff-fallback pass was attempted (e.g. the fallback itself
	// hit a provider limit). Empty when no fallback ran or the fallback
	// resolved the list.
	FilesTruncatedReason string
	Commits              []AheadCommit // used by the T3 lone-wolf gate
	PatchSkipReason      string
}

// IsFilesTruncated reports whether Diffs is known or suspected to be
// missing files. It is true when the provider explicitly capped the list
// (FilesTruncated), or when Diffs still sits at the cap and no diff-fallback
// pass confirmed the list complete (!FilesComplete). Rows written before
// FilesComplete existed decode with it false, so a capped-looking count
// with no completion signal is correctly treated as truncated rather than
// assumed exact.
func (t T2Data) IsFilesTruncated() bool {
	return t.FilesTruncated || (len(t.Diffs) >= CompareFilesCap && !t.FilesComplete)
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
// Fork is populated for fork records. Err is populated for errors.
// Report is the terminal acquisition metadata; it is the last item sent
// on the channel (when the provider supplies one) and is never nil when set.
type ForkMsg struct {
	Fork   T1Data
	Err    error              // non-nil means this item is an error; Fork is zero.
	Report *AcquisitionReport // terminal acquisition metadata (last item on channel)
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

// LinearHistoryProvider is an optional provider capability that classifies,
// per fork, whether the fork's tip is reachable from the upstream's tip
// without crossing a merge commit. The provider returns the raw signal
// (parents.totalCount per commit on the fork's default branch ahead of the
// merge base); the consumer derives the boolean LinearHistory and the scalar
// MergeCommits in one place, so the derivation logic lives in forge rather
// than duplicated in every backend.
type LinearHistoryProvider interface {
	// MergeCommitHistory returns, per fork ID, the raw parents-totalCount
	// vector for every commit on the fork's default branch that lies ahead
	// of upstream. callers derive the boolean LinearHistory and the scalar
	// MergeCommits from the vector themselves. Forks absent from the map
	// were not resolved, which is distinct from a present empty vector
	// (linear). truncated lists IDs whose history was capped at the request
	// limit; the slice for those forks is a prefix, not the full history.
	MergeCommitHistory(ctx context.Context, forks []T1Data) (histories map[string][]int, truncated []string, err error)
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

// PathLastTouch is the upstream last-touch resolution for one path: the most
// recent commit on the upstream default branch that changed it, and how many
// commits landed on that branch afterward (CommitsSince). A fork whose own
// last-touch commit for the same path equals SHA has never itself changed
// the path -- everything the fork carries there came from upstream.
type PathLastTouch struct {
	SHA          string
	CommittedAt  time.Time
	CommitsSince int
}

// LastTouchOutcome classifies a ForkLastTouch call. GitHub's tree-commit-info
// endpoint is undocumented and best-effort (see internal/github/treecommitinfo),
// so every value other than LastTouchOK means "no information" -- callers
// must treat it as a skip, never as evidence a path is untouched.
type LastTouchOutcome int

const (
	LastTouchOK LastTouchOutcome = iota
	LastTouchNotFound
	LastTouchDisabled
	LastTouchError
)

// String renders o for logging.
func (o LastTouchOutcome) String() string {
	switch o {
	case LastTouchOK:
		return "ok"
	case LastTouchNotFound:
		return "not_found"
	case LastTouchDisabled:
		return "disabled"
	case LastTouchError:
		return "error"
	default:
		return "unknown"
	}
}

// LastTouchProvider is an optional provider capability that lets a caller
// compare a fork's own last-touch commit for a path against upstream's,
// skipping a REST compare when they agree (proof the fork never changed the
// path itself). PathLastTouch resolves the upstream side in one bounded
// batch; ForkLastTouch is the fork-side counterpart, called per fork/ref/dir.
//
// Providers that do not implement it remain valid Forge values -- callers
// fall back to their normal compare path when this capability is absent.
type LastTouchProvider interface {
	// PathLastTouch resolves, for each of paths, the most recent commit on
	// the upstream default branch that touched it. A path absent from the
	// returned map has no history on that branch at all -- distinct from a
	// present entry with CommitsSince == 0.
	PathLastTouch(ctx context.Context, paths []string) (map[string]PathLastTouch, error)

	// ForkLastTouch queries the fork's own last-touch commit for every entry
	// name in dir at ref (dir == "" addresses the repository root), the same
	// listing GitHub's file browser shows. The returned map is keyed by
	// entry name (not full path); it is nil whenever the outcome is not
	// LastTouchOK.
	ForkLastTouch(ctx context.Context, fork T1Data, ref, dir string) (map[string]string, LastTouchOutcome)
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
