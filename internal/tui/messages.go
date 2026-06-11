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
	// warn is a non-fatal warning to surface alongside a populated fork list —
	// e.g. the stream was cut short by an error after some forks arrived, so the
	// displayed list is partial. Distinct from err, which suppresses the list.
	warn error
}

type tier2ResultMsg struct {
	forkID string
	t2     forge.T2Data
	err    error
	// budgetSkipped is true when the compare was not attempted because the
	// rate-limit reserve floor was reached. Distinct from err: the fork is
	// kept, just marked un-enriched rather than failed.
	budgetSkipped bool
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
