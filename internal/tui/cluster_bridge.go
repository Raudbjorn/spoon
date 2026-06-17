package tui

// cluster_bridge.go contains the TUI-side orchestration of the cluster
// pipeline. It mirrors what forksops.Stream does for the non-interactive
// paths, but adapted to Bubble Tea's event-driven model: the goroutine that
// runs cluster.RunPipeline pushes status/result messages onto a shared
// channel which the Model drains via a long-lived tea.Cmd.

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
)

// ClusterOptions mirrors forksops.ClusterOptions so callers
// (cmd/spoon/main.go) can pass cluster configuration into the TUI without
// dragging in the forksops package.
type ClusterOptions struct {
	Enabled        bool
	TopN           int
	Epsilon        float64
	MinClusterSize int
	Refresh        bool

	// Embedder, when non-nil, replaces the built-in lexical embedder
	// (openvino backend or tests). EmbedderID keys the cluster cache.
	Embedder   embed.Embedder
	EmbedderID string

	// Categorize enables zero-shot category assignment.
	Categorize bool

	// LabelPolisher, when non-nil, rewrites cluster labels (in-process LLM).
	LabelPolisher cluster.LabelPolisher

	// CentralityBackend is forwarded to cluster.PipelineOptions. "" or
	// "directory" → directory-centrality proxy. "mdg" → Module Dependency
	// Graph. See `--full-mdg` in `spoon --help`.
	CentralityBackend string

	// CentralityHeadSHA is forwarded to cluster.PipelineOptions. It pins
	// the MDG cache to the upstream's current default-branch tip SHA so
	// the 24h fast path is actually used. When empty (e.g., a Parent
	// call that did not resolve a SHA), the cache is skipped and the MDG
	// builds from scratch on every run.
	CentralityHeadSHA string

	// StrictMDG, when true, surfaces a non-fatal MDG build/cache failure
	// as a ClusterSkip with code "mdg_unavailable" instead of silently
	// falling back to the directory proxy. Off by default — the silent
	// fallback is the right behavior for ordinary `--full-mdg` runs.
	// See `--strict-mdg` in `spoon --help`.
	StrictMDG bool
}

// waitForClusterMsg returns a tea.Cmd that blocks on the model's cluster
// message channel and re-arms itself on each receive. This is how the
// cluster goroutines feed messages into Bubble Tea's Update loop without
// holding a *tea.Program reference.
//
// The ctx parameter is the model's lifecycle context: when the TUI quits
// it is cancelled, which causes this Cmd's blocking receive to return nil
// (a valid no-op for tea.Cmd) so the spawned goroutine exits cleanly
// rather than leaking past program shutdown. Returning nil from a tea.Cmd
// produces no message, so the pump simply stops re-arming.
func waitForClusterMsg(ch <-chan tea.Msg, ctx context.Context) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		if ctx == nil {
			msg, ok := <-ch
			if !ok {
				return nil
			}
			return msg
		}
		select {
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			return msg
		case <-ctx.Done():
			return nil
		}
	}
}

// maybeStartClusterPipeline returns a tea.Cmd that launches the cluster
// pipeline in a background goroutine, or nil when clustering is disabled,
// already run, or there is nothing to cluster. The returned Cmd performs
// the pipeline-launch synchronously inside the Bubble Tea worker pool but
// the heavy lifting happens in a spawned goroutine that pushes results
// onto m.clusterMsgs.
func (m *Model) maybeStartClusterPipeline() tea.Cmd {
	if m == nil {
		return nil
	}
	if !m.clusterOpts.Enabled {
		return nil
	}
	if m.clusterRan {
		return nil
	}
	if m.parent == nil || len(m.forks) == 0 {
		return nil
	}
	m.clusterRan = true
	m.clusterStatus = "running"

	// Snapshot inputs the goroutine will need. The forks slice is captured
	// by reference so cluster-result write-back lands on the live data;
	// callers must not reslice m.forks during this window.
	provider := m.provider
	parent := *m.parent
	owner, repoName := splitParentName(m.parent.FullName)
	if owner == "" || repoName == "" {
		m.clusterStatus = "skipped"
		m.clusterSkipReason = "malformed parent FullName: " + m.parent.FullName
		return nil
	}
	opts := m.clusterOpts
	out := m.clusterMsgs
	forksRef := m.forks

	return func() tea.Msg {
		go runTUIClusterPipeline(provider, parent, owner, repoName, opts, forksRef, out)
		return nil
	}
}

// runTUIClusterPipeline builds the cluster.PipelineInputs from the TUI's
// model state and invokes cluster.RunPipeline. Result/skip/error are
// pushed onto `out` as a clusterResultMsg so the Update loop can apply
// them.
func runTUIClusterPipeline(
	provider forge.Forge,
	parent forge.ParentData,
	owner, repoName string,
	opts ClusterOptions,
	forks []ScoredFork,
	out chan<- tea.Msg,
) {
	// Build EnrichedFork pointers over the model's live slice so the
	// pipeline's in-place mutations land back on m.forks[i].Heat.
	enriched := make([]cluster.EnrichedFork, 0, len(forks))
	for i := range forks {
		enriched = append(enriched, cluster.EnrichedFork{
			T1:   forks[i].Fork,
			T2:   forks[i].T2,
			Heat: &forks[i].Heat,
		})
	}

	inputs := cluster.PipelineInputs{
		Provider:      "github",
		UpstreamOwner: owner,
		UpstreamRepo:  repoName,
		Upstream:      parent,
		Forks:         enriched,
	}
	if ghp, ok := provider.(*gh.GHProvider); ok {
		if client := ghp.Client(); client != nil {
			inputs.TreeSource = &gh.TreeSourceForRepo{Client: client, Ref: parent.DefaultBranch}
			inputs.CommitSource = &gh.CommitSourceForRepo{Client: client}
			inputs.ReadmeFetcher = client
		}
	} else {
		inputs.Provider = "other"
	}

	// Resolve the MDG-cache pin: prefer the caller's explicit
	// CentralityHeadSHA override (tests, future pinning beyond the
	// upstream's HEAD), and fall back to parent.HeadSHA when the
	// caller didn't supply one. The fallback is the common case for
	// both the spn forks list and the spoon TUI — both end up here
	// via the cluster_pipeline construction in cmd/spn and cmd/spoon
	// respectively, which always set opts.CentralityHeadSHA = ""
	// and rely on parent.HeadSHA being populated.
	pin := parent.HeadSHA
	if opts.CentralityHeadSHA != "" {
		pin = opts.CentralityHeadSHA
	}
	pipelineOpts := cluster.PipelineOptions{
		Enabled:           opts.Enabled,
		TopN:              opts.TopN,
		Epsilon:           opts.Epsilon,
		MinClusterSize:    opts.MinClusterSize,
		Refresh:           opts.Refresh,
		Embedder:          opts.Embedder,
		EmbedderID:        opts.EmbedderID,
		Categorize:        opts.Categorize,
		LabelPolisher:     opts.LabelPolisher,
		CentralityBackend: opts.CentralityBackend,
		CentralityHeadSHA: pin,
		StrictMDG:         opts.StrictMDG,
	}
	skip, err := cluster.RunPipeline(context.Background(), pipelineOpts, inputs, &silentWriter{})
	send(out, clusterResultMsg{
		Skip:        skip,
		Err:         err,
		Assignments: nil, // pipeline writes back via the Heat pointers; we don't ferry them
		Clusters:    nil,
	})
}

// send pushes msg onto out without blocking the producer. The channel is
// buffered (size 16); on overflow the message is dropped (only happens
// when Bubble Tea isn't draining for some reason, e.g. mid-quit).
func send(out chan<- tea.Msg, msg tea.Msg) {
	if out == nil {
		return
	}
	select {
	case out <- msg:
	default:
	}
}

// silentWriter discards all writes. Used as the cluster pipeline logger
// in the TUI path — pipeline progress is reflected in the model state
// rather than text-logged.
type silentWriter struct{}

func (silentWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- Model handlers for cluster messages ---

// handleClusterResult applies the cluster pipeline's outcome to the model.
// Heat results have already been mutated in-place by the pipeline; this
// handler only needs to update status text and re-sort for display.
func (m *Model) handleClusterResult(msg clusterResultMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Err != nil:
		m.clusterStatus = "error"
		m.clusterSkipReason = msg.Err.Error()
	case msg.Skip != nil:
		switch msg.Skip.Code {
		case "disabled", "no_eligible_forks":
			m.clusterStatus = "skipped"
			m.clusterSkipReason = ""
		default:
			m.clusterStatus = "skipped"
			m.clusterSkipReason = msg.Skip.Message
		}
	default:
		m.clusterStatus = "done"
	}
	// Re-sort so any score-influencing fields (none currently, but
	// reserved) settle into a stable order.
	m.sortForks()
	// Re-arm the message pump so we continue to receive any later
	// cluster-related messages.
	return m, waitForClusterMsg(m.clusterMsgs, m.lifecycleCtx)
}

// clusterFooter returns a short status line for the table footer
// reflecting the cluster pipeline state. Empty when there is nothing
// useful to surface.
func (m Model) clusterFooter() string {
	if !m.clusterOpts.Enabled {
		return ""
	}
	switch m.clusterStatus {
	case "running":
		return "clusters: computing..."
	case "skipped":
		if m.clusterSkipReason != "" {
			return "clusters skipped: " + m.clusterSkipReason
		}
		return ""
	case "error":
		if m.clusterSkipReason != "" {
			return "clusters error: " + m.clusterSkipReason
		}
		return "clusters error"
	case "done":
		// Brief silent success — surfaced via per-fork detail view.
		return ""
	}
	return ""
}

// splitParentName splits "owner/repo" into its two halves; returns
// empty strings if the input is malformed.
func splitParentName(full string) (string, string) {
	idx := strings.Index(full, "/")
	if idx <= 0 || idx == len(full)-1 {
		return "", ""
	}
	return full[:idx], full[idx+1:]
}
