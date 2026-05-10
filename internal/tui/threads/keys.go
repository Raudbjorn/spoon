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
)

func dispatchKey(k tea.KeyMsg) keyAction {
	switch k.String() {
	case "up", "k":
		return actUp
	case "down", "j":
		return actDown
	case "q", "ctrl+c":
		return actQuit
	case "r":
		return actReply
	case "R":
		return actResolve
	case "a":
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
