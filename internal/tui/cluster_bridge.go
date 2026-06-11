package tui

// cluster_bridge.go contains the TUI-side orchestration of the cluster
// pipeline. It mirrors what dump.Run and forksops.Stream do for the
// non-interactive paths, but adapted to Bubble Tea's event-driven model:
// the goroutine that runs cluster.RunPipeline pushes status/result messages
// onto a shared channel which the Model drains via a long-lived tea.Cmd.
//
// The Prompter for the missing-model bootstrap path routes its yes/no
// prompt through the same channel as a clusterPromptMsg. The user's
// keypress in the embedderBootstrap view returns the answer to the blocked
// SelectEmbedder goroutine via the prompt's reply channel.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
)

// ClusterOptions mirrors dump.ClusterOptions so callers (cmd/spoon/main.go)
// can pass cluster configuration into the TUI without dragging in the
// dump package. Keep this in sync with dump.ClusterOptions.
type ClusterOptions struct {
	Enabled         bool
	TopN            int
	Endpoint        string
	ModelOverride   string
	LabelerEndpoint string
	LabelerModel    string
	Epsilon         float64
	MinClusterSize  int
	AutoPull        bool
	NoPrompt        bool
	Refresh         bool

	// Backend selects the Embedder implementation. Empty defaults to "ollama".
	// "sidecar" routes through SidecarEmbedder against SidecarEndpoint.
	Backend string

	// SidecarEndpoint is the http://host:port of the Python sidecar process,
	// used when Backend=="sidecar".
	SidecarEndpoint string

	// NonInteractive mirrors the field of the same name on
	// dump.ClusterOptions / forksops.ClusterOptions. The TUI is always
	// interactive (and consequently passes false to the pipeline regardless
	// of this value); the field exists for shape-parity so callers that
	// translate between Options structs don't accidentally lose state, and
	// future refactors can collapse the four ClusterOptions copies into one.
	NonInteractive bool

	// Labeler, when non-nil, overrides construction from LabelerEndpoint.
	Labeler cluster.Labeler

	// EmbedderForTest is reserved for tests.
	EmbedderForTest embed.Embedder

	// CentralityBackend is forwarded to cluster.PipelineOptions. "" or
	// "directory" → directory-centrality proxy. "mdg" → Module Dependency
	// Graph. See `--full-mdg` in `spoon --help`.
	CentralityBackend string
}

// tuiDefaultLabelerModel aliases cluster.DefaultLabelerModel for readability
// at the call site below. Keeping it as a const aliasing the package-level
// constant means a single source of truth without churning the existing
// readability of this file.
const tuiDefaultLabelerModel = cluster.DefaultLabelerModel

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

	labeler := opts.Labeler
	if labeler == nil && opts.LabelerEndpoint != "" {
		model := opts.LabelerModel
		if model == "" {
			model = tuiDefaultLabelerModel
		}
		labeler = &cluster.OllamaChatLabeler{
			Endpoint: opts.LabelerEndpoint,
			Model:    model,
		}
	}

	pipelineOpts := cluster.PipelineOptions{
		Enabled:           opts.Enabled,
		TopN:              opts.TopN,
		Endpoint:          opts.Endpoint,
		ModelOverride:     opts.ModelOverride,
		Backend:           opts.Backend,
		SidecarEndpoint:   opts.SidecarEndpoint,
		LabelerEndpoint:   opts.LabelerEndpoint,
		Epsilon:           opts.Epsilon,
		MinClusterSize:    opts.MinClusterSize,
		AutoPull:          opts.AutoPull,
		NoPrompt:          opts.NoPrompt,
		NonInteractive:    false, // TUI is interactive — let the Prompter run
		Refresh:           opts.Refresh,
		Labeler:           labeler,
		EmbedderForTest:   opts.EmbedderForTest,
		CentralityBackend: opts.CentralityBackend,
	}

	// The Prompter is consulted by cluster.RunPipeline via SelectEmbedder.
	// We can't pass it through PipelineOptions directly — the pipeline
	// constructs an embed.StdinPrompter when NonInteractive == false. So
	// we instead wrap the pipeline's bootstrap step ourselves below.

	skip, err := runTUIPipelineWithPrompter(context.Background(), pipelineOpts, inputs, out)
	send(out, clusterResultMsg{
		Skip:        skip,
		Err:         err,
		Assignments: nil, // pipeline writes back via the Heat pointers; we don't ferry them
		Clusters:    nil,
	})
}

// runTUIPipelineWithPrompter invokes cluster.RunPipeline with a TUI-aware
// Prompter. Because cluster.RunPipeline's NonInteractive==false branch
// constructs its own StdinPrompter, we have to do the SelectEmbedder dance
// ourselves and then call RunPipeline with EmbedderForTest set to the
// resulting Embedder so the pipeline reuses it.
//
// This keeps the surface area of cluster.RunPipeline untouched while still
// letting the TUI control prompting.
func runTUIPipelineWithPrompter(
	ctx context.Context,
	opts cluster.PipelineOptions,
	inputs cluster.PipelineInputs,
	out chan<- tea.Msg,
) (*cluster.SkipReason, error) {
	// If a test embedder is already supplied, skip bootstrap entirely.
	if opts.EmbedderForTest != nil {
		return cluster.RunPipeline(ctx, opts, inputs, &silentWriter{})
	}

	prompter := &tuiPrompter{out: out, answer: make(chan bool, 1)}
	embedder, modelName, skip := embed.SelectEmbedder(ctx, embed.SelectOptions{
		Endpoint:        opts.Endpoint,
		ExplicitModel:   opts.ModelOverride,
		AutoPull:        opts.AutoPull,
		NoPrompt:        opts.NoPrompt,
		NonInteractive:  false,
		Backend:         opts.Backend,
		SidecarEndpoint: opts.SidecarEndpoint,
	}, prompter)
	if skip != nil {
		return &cluster.SkipReason{
			Code:     skip.Code,
			Message:  skip.Message,
			Endpoint: skip.Endpoint,
			Model:    skip.Model,
		}, nil
	}

	opts.EmbedderForTest = embedder
	if opts.ModelOverride == "" {
		opts.ModelOverride = modelName
	}
	// NonInteractive must stay false to preserve the existing log noise
	// suppression behaviour inside the pipeline; the pipeline's stdin
	// prompter is never consulted because we've already provided the
	// embedder via EmbedderForTest.
	return cluster.RunPipeline(ctx, opts, inputs, &silentWriter{})
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

// --- Prompter ---

// tuiPrompter satisfies embed.Prompter for the TUI. AskPull sends a
// clusterPromptMsg into the model's message channel and blocks on the
// returned answer channel until the user replies.
type tuiPrompter struct {
	out    chan<- tea.Msg
	answer chan bool
}

// AskPull sends a clusterPromptMsg into the TUI message channel and waits
// for the user's reply. Returns the boolean answer.
func (p *tuiPrompter) AskPull(model string, sizeMB int) (bool, error) {
	if p.out == nil {
		return false, errors.New("tui prompter: no message channel")
	}
	// Buffered reply channel so the Update handler can write without
	// blocking even if AskPull's reader gets cancelled.
	reply := make(chan bool, 1)
	// Non-blocking send: if the TUI isn't draining (e.g. mid-quit), drop
	// the prompt and skip clustering rather than deadlocking the pipeline.
	select {
	case p.out <- clusterPromptMsg{Model: model, SizeMB: sizeMB, Reply: reply}:
	default:
		return false, errors.New("tui prompter: message channel full; skipping pull prompt")
	}
	yes, ok := <-reply
	if !ok {
		return false, nil
	}
	return yes, nil
}

// ProgressFunc returns a no-op callback. v1 of the bootstrap screen does
// not render a progress bar (the goroutine that calls embed.Pull is
// effectively invisible until it finishes). A future revision can route
// progress updates through the same channel.
func (p *tuiPrompter) ProgressFunc() func(phase string, pct float64) {
	return func(phase string, pct float64) {}
}

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

// handleClusterPrompt transitions the model into viewEmbedderBootstrap so
// the next render shows the pull prompt. The reply channel is stashed on
// the model and consumed by handleEmbedderBootstrapKey.
func (m *Model) handleClusterPrompt(msg clusterPromptMsg) (tea.Model, tea.Cmd) {
	cp := msg
	m.clusterPendingPrompt = &cp
	m.view = viewEmbedderBootstrap
	return m, waitForClusterMsg(m.clusterMsgs, m.lifecycleCtx)
}

// handleEmbedderBootstrapKey consumes Y/N from the user, replies on the
// prompt's channel, and returns to the previous view.
func (m *Model) handleEmbedderBootstrapKey(key string) (tea.Model, tea.Cmd) {
	if m.clusterPendingPrompt == nil {
		// Defensive: no pending prompt; just bail back to the table.
		m.view = viewTable
		return m, nil
	}
	reply := m.clusterPendingPrompt.Reply
	switch key {
	case "y", "Y", "enter":
		return m, func() tea.Msg {
			return clusterPromptResponseMsg{Reply: reply, Yes: true}
		}
	case "n", "N", "esc":
		return m, func() tea.Msg {
			return clusterPromptResponseMsg{Reply: reply, Yes: false}
		}
	}
	return m, nil
}

// viewEmbedderBootstrap renders the model-pull confirmation card.
func (m Model) viewEmbedderBootstrap() string {
	if m.clusterPendingPrompt == nil {
		return "\n  (no pending embedder prompt)\n"
	}
	cp := m.clusterPendingPrompt
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 3).
		BorderForeground(lipgloss.Color("214"))

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(titleStyle.Render("  Embedding model required"))
	b.WriteString("\n\n")
	body := fmt.Sprintf(
		"No embedding model installed.\n\n"+
			"Pull %s (%d MB) from Ollama?\n\n"+
			"%s   %s",
		cp.Model, cp.SizeMB,
		lipgloss.NewStyle().Bold(true).Render("[Y]es"),
		lipgloss.NewStyle().Bold(true).Render("[N]o"),
	)
	b.WriteString("  " + box.Render(body))
	b.WriteString("\n\n  ")
	b.WriteString(helpStyle.Render("Enter/Y to pull, N/Esc to skip clustering"))
	b.WriteString("\n")
	return b.String()
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
