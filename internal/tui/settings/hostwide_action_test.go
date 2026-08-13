package settings

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
)

func TestSystemLayerRewriteReadmeRequiresHostwideConfirmation(t *testing.T) {
	m := Model{Config: &config.Config{}, Path: config.SystemPath()}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(Model)
	if cmd != nil || !m.confirming || m.pending.Consequence != Hostwide || m.pendingAction != ActionRewriteReadme {
		t.Fatalf("system README write bypassed hostwide confirmation: confirming=%v pending=%#v action=%q", m.confirming, m.pending, m.pendingAction)
	}
}
