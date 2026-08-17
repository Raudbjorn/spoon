package tui

import (
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
)

// embedStatus is what the table knows about the two embedding providers.
//
// Neither provider announced itself anywhere in the interactive session before
// this: the TUI clusters lexically and never constructs fastembed, and Voyage is
// reached only by the rerank key, so "is any of this actually being used?" had
// no answer short of reading the database. Worse, the honest answer for
// fastembed is not a yes/no -- the model is a download and the runtime is a
// system library, and either can be missing on its own.
type embedStatus struct {
	fastEmbed embed.FastEmbedStatus
	voyage    embed.VoyageKeyDiagnosis
	resolved  bool
}

// refreshEmbedStatus re-probes both providers. It touches only the filesystem
// and the already-resolved configuration -- no network, no store, no dlopen --
// so it is cheap enough to call on load and after a settings change.
func (m *Model) refreshEmbedStatus() {
	effective := m.settings.Effective
	if effective == nil {
		m.embedProbe = embedStatus{}
		return
	}
	env := m.settings.Environment
	if env == nil {
		env = map[string]string{}
	}
	m.embedProbe = embedStatus{
		fastEmbed: embed.ProbeFastEmbed(fastEmbedCacheDir(*effective), env),
		voyage:    embed.DiagnoseVoyageKey(*effective, false, env),
		resolved:  true,
	}
}

// fastEmbedCacheDir reads the configured cache directory, leaving the default
// to the prober when unset.
func fastEmbedCacheDir(effective config.EffectiveConfig) string {
	return strings.TrimSpace(effective.FastEmbed.CacheDir.Value)
}

// embedStatusLine is the one-line form for the status bar. It is deliberately
// terse and deliberately never says "on" for fastembed on the strength of a
// probe that has not loaded the runtime -- "ready" is a prediction, and the
// detail behind it lives in the settings panel.
func (m Model) embedStatusLine() string {
	if !m.embedProbe.resolved {
		return ""
	}

	fast := "fastembed "
	switch {
	case m.embedProbe.fastEmbed.Ready():
		fast += "ready"
	case !m.embedProbe.fastEmbed.ModelPresent:
		fast += "no model"
	default:
		fast += "no runtime"
	}

	voyage := "voyage "
	switch {
	case m.embedProbe.voyage.HasKey:
		voyage += "key set"
	case m.embedProbe.voyage.Label != "":
		voyage += m.embedProbe.voyage.Label
	default:
		voyage += "no key"
	}

	return "emb: " + fast + ", " + voyage
}
