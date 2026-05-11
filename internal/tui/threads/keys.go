package threads

import tea "github.com/charmbracelet/bubbletea"

// keyAction is the result of dispatching a tea.KeyMsg.
type keyAction int

const (
	actNone keyAction = iota
	actUp
	actDown
	actQuit
	actReply
	actResolve
	actResolveAll
	actUnresolveAll
	actOpen
	actHelp
	actApplySuggestion
)

func dispatchKey(k tea.KeyMsg) keyAction {
	switch k.String() {
	case "up", "k":
		return actUp
	case "down", "j":
		return actDown
	case "q", "ctrl+c":
		return actQuit
	case "r", "enter":
		return actReply
	case "R":
		return actResolve
	case "a":
		// The caller decides between actApplySuggestion (when the focused
		// thread has a suggestion) and actResolveAll otherwise. We return
		// actApplySuggestion here; model.Update falls back to actResolveAll
		// when no suggestion is present.
		return actApplySuggestion
	case "ctrl+a":
		return actResolveAll
	case "A":
		return actUnresolveAll
	case "o":
		return actOpen
	case "?":
		return actHelp
	}
	return actNone
}
