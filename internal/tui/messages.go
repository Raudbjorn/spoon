package tui

import (
	"github.com/svnbjrn/spoon/internal/cluster"
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

// clusterResultMsg is published when the cluster pipeline finishes (or
// fails). The model handles it by setting cluster fields on each fork's
// HeatResult and triggering a re-render.
type clusterResultMsg struct {
	Assignments []cluster.Assignment
	Clusters    []cluster.Cluster
	Skip        *cluster.SkipReason // non-nil when clustering was skipped
	Err         error               // non-nil on hard failure
}

// clusterPromptMsg is published by the embed.Prompter implementation when
// it needs to ask the user whether to pull a missing embedding model. The
// model transitions to the embedderBootstrap viewState and renders the
// prompt; the user's response is sent back via clusterPromptResponseMsg.
type clusterPromptMsg struct {
	Model  string
	SizeMB int
	Reply  chan<- bool // SelectEmbedder is blocked waiting on this
}

// clusterPromptResponseMsg carries the user's answer back to the goroutine
// running SelectEmbedder.
type clusterPromptResponseMsg struct {
	Reply chan<- bool
	Yes   bool
}
