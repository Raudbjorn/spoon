package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/semantic"
	"github.com/svnbjrn/spoon/internal/store"
)

// embedRunStartedMsg announces that a run is under way, so the status bar can
// say so before the first batch lands.
type embedRunStartedMsg struct {
	forks int
	run   int
}

// embedProgressMsg reports one completed batch for one provider.
type embedProgressMsg struct {
	run              int
	provider         string
	indexed, pending int
}

// embedRunDoneMsg closes out a run. warnings carries per-provider failures that
// did not stop the run -- a Voyage outage must not cost the user the local
// index that succeeded beside it.
type embedRunDoneMsg struct {
	run      int
	indexed  int
	warnings []string
	err      error
}

// embedTargets returns the fork indices a run covers.
func (m Model) embedTargets(all bool) []int {
	targets := make([]int, 0, len(m.forks))
	for i := range m.forks {
		if all || m.forks[i].Marked {
			targets = append(targets, i)
		}
	}
	return targets
}

// fastEmbedRunnable reports whether the local embedder can be constructed
// without surprising the user, and why not when it cannot.
//
// The model check is the important one. NewFastEmbedEmbedder provisions
// unconditionally: on a machine with an empty cache it downloads the model
// under a fifteen-minute timeout, so without this a keypress turns into several
// silent minutes of "embedding". The probe already knows; this makes it a
// precondition rather than only a display.
func (m Model) fastEmbedRunnable() (string, bool) {
	switch {
	case m.embedModels.fastEmbed == "":
		return "fastembed is not configured", false
	case !m.embedProbe.resolved:
		return "fastembed availability has not been determined", false
	case !m.embedProbe.fastEmbed.ModelPresent:
		return fmt.Sprintf("fastembed model is not downloaded (expected at %s); run 'spoon setup'",
			m.embedProbe.fastEmbed.ModelPath), false
	case !m.embedProbe.fastEmbed.RuntimeFound:
		return "fastembed needs ONNX Runtime; set " + embed.ONNXPathEnv + " to libonnxruntime.so", false
	}
	return "", true
}

// startEmbedRun embeds the selected forks with every provider that is usable.
// This is what the i/I keys do: an explicit request means both providers.
func (m *Model) startEmbedRun(all bool) tea.Cmd {
	return m.startEmbedRunWith(all, true, true)
}

// maybeAutoIndex runs the configured automatic pass once enrichment settles.
//
// The two providers are gated separately and default differently: fastembed is
// local, so it runs unasked; Voyage bills per token, so it stays off until the
// user turns it on. Reporting nothing when neither is enabled is correct -- an
// automatic pass the user did not ask for should not announce itself.
func (m *Model) maybeAutoIndex() tea.Cmd {
	// Silent preconditions. An automatic pass the user did not ask for must not
	// complain about a store or an empty list the way the i/I keys do -- those
	// messages answer a question that was actually asked.
	if m.autoIndexDone || m.embedRunning || m.db == nil || len(m.forks) == 0 {
		return nil
	}
	useFast, useVoyage := m.autoIndexProviders()
	if !useFast && !useVoyage {
		return nil
	}
	// Once per fork list. A later enrichmentDoneMsg (raising the tier ceiling
	// re-enriches) must not silently start a second billed pass; the keys stay
	// available for a deliberate re-run.
	m.autoIndexDone = true
	return m.startEmbedRunWith(true, useFast, useVoyage)
}

// autoIndexProviders reads the two automatic-indexing settings.
func (m Model) autoIndexProviders() (fast bool, voyage bool) {
	effective := m.settings.Effective
	if effective == nil {
		return false, false
	}
	parse := func(value string, fallback bool) bool {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			// An unparseable setting must not be read as consent to spend.
			return fallback
		}
		return parsed
	}
	fast = parse(effective.FastEmbed.AutoIndex.Value, true)
	voyage = parse(effective.Voyage.AutoIndex.Value, false)
	return fast, voyage
}

func (m *Model) startEmbedRunWith(all, useFast, useVoyage bool) tea.Cmd {
	// Selection before capability. If the user pressed the key with nothing
	// marked, "no forks marked" is the useful answer whether or not a store
	// happens to be open; leading with an infrastructure complaint would be a
	// non sequitur about a run they did not actually request.
	targets := m.embedTargets(all)
	if len(targets) == 0 {
		// Never widen an empty selection into "everything": under Voyage that
		// is the difference between spending nothing and paying for a full pass.
		m.setEmbedError("No forks marked; use Space to mark forks, then i (or I for all)")
		return nil
	}
	if m.db == nil {
		m.setEmbedError("No store is open, so embeddings could not be kept")
		return nil
	}
	if m.embedRunning {
		m.setEmbedError("An embedding run is already in progress")
		return nil
	}

	repo, ok := m.storeRepoRecord(false, time.Time{})
	if !ok {
		m.setEmbedError("The upstream repository is not known yet")
		return nil
	}

	// Snapshot everything the goroutine needs while still on the update loop.
	// The model is copied by value on every Update, so a closure reading it
	// later would be reading a stale copy.
	type target struct {
		snapshot store.Snapshot
		forkKey  string
	}
	work := make([]target, 0, len(targets))
	for _, i := range targets {
		fork := m.forks[i]
		snapshot := store.SnapshotFromForge(repo, fork.Fork, fork.T2, fork.Heat.Score, fork.Heat.Tier, time.Now().UTC())
		attachDocument(&snapshot, &fork)
		if snapshot.Document.DocumentID == "" {
			continue
		}
		work = append(work, target{snapshot: snapshot, forkKey: snapshot.ForkKey()})
	}
	if len(work) == 0 {
		m.setEmbedError("None of the selected forks have anything to embed")
		return nil
	}

	fastReason, fastOK := m.fastEmbedRunnable()
	fastOK = fastOK && useFast
	// Distinguishes "embed these forks, Voyage too if it happens to be set up"
	// from "the user switched automatic Voyage indexing on". Only the latter
	// makes a silent no-op a defect.
	_, voyageRequired := m.autoIndexProviders()
	db := m.db
	effective := m.settings.Effective
	environment := m.settings.Environment
	cacheDir := ""
	if effective != nil {
		cacheDir = effective.FastEmbed.CacheDir.Value
	}
	parentCtx := m.lifecycleCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	progress := make(chan tea.Msg, 16)
	m.embedRunning = true
	m.embedRunID++
	run := m.embedRunID
	m.embedRunTotal = len(work)
	m.embedMsgs = progress
	m.embedProgressCtx, m.embedProgressCancel = context.WithCancel(parentCtx)
	runCtx := m.embedProgressCtx

	return tea.Batch(
		func() tea.Msg { return embedRunStartedMsg{forks: len(work), run: run} },
		func() tea.Msg {
			// runCtx, not context.Background(): the cancel button and the
			// TUI shutdown must reach this goroutine, otherwise a cancelled
			// Voyage run keeps billing after the user walks away.
			ctx := runCtx

			// Documents first. A fork the TUI has never persisted a document
			// for has nothing to embed, and until recently that was every fork
			// it had ever seen.
			snapshots := make([]store.Snapshot, 0, len(work))
			forkKeys := make([]string, 0, len(work))
			for _, item := range work {
				snapshots = append(snapshots, item.snapshot)
				forkKeys = append(forkKeys, item.forkKey)
			}
			if err := db.UpsertSnapshots(ctx, snapshots); err != nil {
				return embedRunDoneMsg{run: run, err: fmt.Errorf("persist documents: %w", err)}
			}

			var warnings []string
			total := 0

			if fastOK {
				indexed, err := runFastEmbed(ctx, db, cacheDir, forkKeys, run, progress)
				total += indexed
				if err != nil {
					warnings = append(warnings, "fastembed: "+err.Error())
				}
			} else if useFast && fastReason != "" {
				warnings = append(warnings, fastReason)
			}

			// Voyage runs beside fastembed, never instead of it, and its
			// failures never cost the local index -- the same contract the CLI
			// keeps.
			if useVoyage && effective != nil {
				indexed, err := runVoyage(ctx, db, *effective, environment, forkKeys, voyageRequired, run, progress)
				total += indexed
				if err != nil {
					warnings = append(warnings, "voyage: "+err.Error())
				}
			}

			return embedRunDoneMsg{run: run, indexed: total, warnings: warnings}
		},
	)
}

func runFastEmbed(ctx context.Context, db *store.Store, cacheDir string, forkKeys []string, run int, progress chan<- tea.Msg) (int, error) {
	model, err := embed.NewFastEmbedEmbedder(embed.FastEmbedConfig{CacheDir: cacheDir})
	if err != nil {
		return 0, err
	}
	defer model.Close()
	return semantic.IndexPendingFor(ctx, db, model, forkKeys, func(p semantic.IndexProgress) {
		sendEmbedProgress(progress, embedProgressMsg{run: run, provider: "fastembed", indexed: p.Indexed, pending: p.Pending})
	})
}

func runVoyage(ctx context.Context, db *store.Store, effective config.EffectiveConfig, environment map[string]string, forkKeys []string, required bool, run int, progress chan<- tea.Msg) (int, error) {
	cfg, active, err := embed.ResolveVoyageEffective(ctx, effective, false, db, environment)
	if err != nil {
		return 0, err
	}
	if !active {
		// Silence is right for exactly one case: the user pressed i/I on a host
		// that has never configured Voyage. They asked to embed, fastembed did
		// it, and nagging about a provider they do not use is noise.
		//
		// Every other case is the bug this work exists to fix. A key that
		// resolves but leaves Voyage inactive, or an explicit
		// embedder.voyage.autoIndex, means the user opted in and got nothing --
		// reporting that as success is how a good 0600 key file goes unnoticed
		// for weeks. DiagnoseVoyageKey has the sentence that says which it is.
		diagnosis := embed.DiagnoseVoyageKey(effective, false, environment)
		if diagnosis.HasKey || required {
			return 0, errors.New(diagnosis.Summary)
		}
		return 0, nil
	}
	model, err := embed.NewVoyageEmbedder(cfg)
	if err != nil {
		return 0, err
	}
	return semantic.IndexPendingFor(ctx, db, model, forkKeys, func(p semantic.IndexProgress) {
		sendEmbedProgress(progress, embedProgressMsg{run: run, provider: "voyage", indexed: p.Indexed, pending: p.Pending})
	})
}

// send delivers a progress message without ever blocking the indexing
// goroutine: a full channel drops the update rather than stalling paid work
// behind a UI that is not draining fast enough. Progress is advisory; the
// completion message is not, and goes back through tea.Cmd rather than here.
func sendEmbedProgress(ch chan<- tea.Msg, msg tea.Msg) {
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func (m *Model) setEmbedError(message string) {
	m.errMsg = message
	m.errMsgTime = time.Now()
}

// embedRunFooter is the status-bar segment while a run is in flight or just
// after it finished.
func (m Model) embedRunFooter() string {
	if !m.embedRunning {
		return ""
	}
	if m.embedRunNote == "" {
		return fmt.Sprintf("embedding %d forks...", m.embedRunTotal)
	}
	return m.embedRunNote
}

// describeEmbedResult composes the closing message, keeping partial success
// visible: "indexed 40, voyage failed" is materially different from "failed".
func describeEmbedResult(msg embedRunDoneMsg) string {
	if msg.err != nil {
		return "Embedding failed: " + msg.err.Error()
	}
	text := fmt.Sprintf("Embedded %d documents", msg.indexed)
	if len(msg.warnings) > 0 {
		text += " (" + strings.Join(msg.warnings, "; ") + ")"
	}
	return text
}
