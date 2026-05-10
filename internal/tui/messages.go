package tui

import (
	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
)

// Message types for Bubble Tea update loop.

type authReadyMsg struct {
	provider forge.Forge
	auth     forge.AuthInfo
	err      error
}

type parentFetchedMsg struct {
	parent forge.ParentData
	err    error
}

type forksFetchedMsg struct {
	forks []forge.T1Data
	err   error
}

type tier2ResultMsg struct {
	forkID string
	t2     forge.T2Data
	err    error
}

type startFetchMsg struct{}

type enrichBatchTickMsg struct{}

type enrichmentDoneMsg struct{}

type errMsg struct{ err error }

// cachedLoadMsg is used when loading from the GitHub-specific cache.
// The cache bridge converts gh types to forge types.
type cachedLoadMsg struct {
	parent forge.ParentData
	forks  []forge.T1Data
	cache  *gh.CacheEntry // kept for compare cache lookups
}
