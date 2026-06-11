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
	actCounterPropose
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
		// "a" always dispatches actApplySuggestion. When the focused thread has
		// no suggestion, model.Update shows a "no suggestion on this thread"
		// status and no-ops — it does NOT fall back to resolve-all (that's the
		// dedicated Ctrl+A binding).
		return actApplySuggestion
	case "ctrl+a":
		return actResolveAll
	case "A":
		return actUnresolveAll
	case "o":
		return actOpen
	case "?":
		return actHelp
	case "c":
		return actCounterPropose
	}
	return actNone
}
