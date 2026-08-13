package threads

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/tui/keymap"
)

// keyAction remains a local alias so the model's switch documents its thread
// operations, while the registry owns every key-to-action decision.
type keyAction = keymap.Action

const (
	actNone            = keymap.None
	actUp              = keymap.Up
	actDown            = keymap.Down
	actQuit            = keymap.Quit
	actReply           = keymap.Reply
	actResolve         = keymap.Resolve
	actResolveAll      = keymap.ResolveAll
	actUnresolveAll    = keymap.UnresolveAll
	actOpen            = keymap.OpenBrowser
	actHelp            = keymap.ToggleHelp
	actApplySuggestion = keymap.ApplySuggestion
	actCounterPropose  = keymap.CounterPropose
	actFullscreen      = keymap.ToggleFullscreen
)

func dispatchKey(k tea.KeyMsg) keyAction {
	return keymap.Dispatch(keymap.ThreadList, k.String())
}
