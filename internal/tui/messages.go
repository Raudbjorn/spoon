package tui

import (
	gh "github.com/svnbjrn/spoon/internal/github"
)

// Message types for Bubble Tea update loop.

type authCheckedMsg struct {
	client *gh.Client
	status gh.AuthStatus
	err    error
}

type parentFetchedMsg struct {
	parent gh.RepoInfo
	err    error
}

type forksFetchedMsg struct {
	forks  []gh.ForkInfo
	extras map[int64]gh.T1Extra
	err    error
}

type tier2ResultMsg struct {
	forkID       int64
	compare      gh.CompareResult
	activeBranch string // non-empty if work found on a side branch
	err          error
}

type enrichBatchTickMsg struct{}

type enrichmentDoneMsg struct{}

type errMsg struct{ err error }

type cachedLoadMsg struct {
	parent gh.RepoInfo
	forks  []gh.ForkInfo
	extras map[int64]gh.T1Extra
	cache  *gh.CacheEntry
}
