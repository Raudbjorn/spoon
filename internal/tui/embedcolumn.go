package tui

import (
	"context"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/store"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// embedModels names the two index partitions the session reports on.
//
// They are full model identities, not provider names, and they are compared
// exactly. A model identity encodes its parameters, so changing the output
// dimension or the length limit re-partitions the index and strands the old
// rows: matching on a "fastembed:"/"voyage:" prefix would count a stranded row
// as coverage while the indexer, which joins on exact equality, kept offering
// the same fork for work -- a tick that never changes however often the user
// asks for a run.
type embedModels struct {
	fastEmbed string
	voyage    string
}

// embedCoverageMsg carries a coverage snapshot back onto the update loop.
type embedCoverageMsg struct {
	coverage map[string]store.ForkCoverage
	err      error
}

// resolveEmbedModels derives the identities from the resolved configuration,
// without constructing either embedder -- fastembed is cgo over a dlopened
// runtime, and Voyage is billed.
func (m *Model) resolveEmbedModels() {
	models := embedModels{}
	if effective := m.settings.Effective; effective != nil {
		if profile, ok := embed.LookupFastEmbedProfile(effective.FastEmbed.Model.Value); ok {
			models.fastEmbed = profile.Identity()
		}
		dimension := embed.VoyageDefaultDimension
		if parsed, err := strconv.Atoi(effective.Voyage.OutputDimension.Value); err == nil && parsed > 0 {
			dimension = parsed
		}
		model := effective.Voyage.EmbedModel.Value
		if model == "" {
			model = embed.VoyageDefaultEmbedModel
		}
		models.voyage = embed.VoyageModelID(model, dimension)
	}
	m.embedModels = models
}

// loadEmbedCoverage reads per-fork coverage off the update loop. View runs on
// every keystroke, so it must never touch the database.
func (m *Model) loadEmbedCoverage() tea.Cmd {
	if m.db == nil || m.parent == nil {
		return nil
	}
	repoKey, ok := m.repoKey()
	if !ok {
		return nil
	}
	db, models := m.db, m.embedModels
	wanted := make([]string, 0, 2)
	if models.fastEmbed != "" {
		wanted = append(wanted, models.fastEmbed)
	}
	if models.voyage != "" {
		wanted = append(wanted, models.voyage)
	}
	return func() tea.Msg {
		coverage, err := db.EmbeddingCoverage(context.Background(), repoKey, wanted)
		return embedCoverageMsg{coverage: coverage, err: err}
	}
}

// repoKey is the store key for the upstream currently on screen. It is derived
// from the same RepoRecord the snapshots are written with, so a lookup cannot
// disagree with what was stored.
func (m Model) repoKey() (string, bool) {
	record, ok := m.storeRepoRecord(false, time.Time{})
	if !ok {
		return "", false
	}
	return store.RepoKey(record.Provider, record.Host, record.Owner, record.Name), true
}

// embedCell renders one fork's coverage as exactly two cells: the local
// embedder, then the paid one.
//
// Three states per provider, not two. A stale embedding -- one computed from a
// document body that has since changed, which is what a T2 enrichment does to
// every fork it touches -- still has a row, but the indexer will redo it, so
// showing it as done would be a lie the user could not act on.
// embedColumnShown reports whether either provider has an index partition worth
// a column. A host with neither configured gets the table it had before.
func (m Model) embedColumnShown() bool {
	return m.embedModels.fastEmbed != "" || m.embedModels.voyage != ""
}

// embedCellFor looks one fork up in the loaded coverage. A fork with no entry
// renders as "no coverage" rather than blank: the store has been asked and the
// answer was nothing, which is exactly what the user wants to see.
func (m Model) embedCellFor(fork ScoredFork) string {
	repoKey, ok := m.repoKey()
	if !ok {
		return m.embedCell(store.ForkCoverage{})
	}
	return m.embedCell(m.embedCoverage[store.ForkKey(repoKey, fork.Fork.ID)])
}

func (m Model) embedCell(coverage store.ForkCoverage) string {
	return m.embedMark(coverage, m.embedModels.fastEmbed) + m.embedMark(coverage, m.embedModels.voyage)
}

func (m Model) embedMark(coverage store.ForkCoverage, model string) string {
	ctx := m.themeContext()
	switch {
	case model == "":
		// The provider is not configured, so it has no opinion about this fork.
		// Deliberately distinct from "configured and missing".
		return " "
	case coverage.Fresh(model):
		return ctx.Glyph(theme.Check)
	case coverage.Present(model):
		return "~"
	default:
		return ctx.Glyph(theme.Separator)
	}
}

// embedLegend explains the column, and only when the column can say anything.
func (m Model) embedLegend() string {
	if m.embedModels.fastEmbed == "" && m.embedModels.voyage == "" {
		return ""
	}
	ctx := m.themeContext()
	check, dot := ctx.Glyph(theme.Check), ctx.Glyph(theme.Separator)
	return "EMB " + check + " embedded  ~ stale (will re-embed)  " + dot + " none  (columns: fastembed, voyage)"
}
