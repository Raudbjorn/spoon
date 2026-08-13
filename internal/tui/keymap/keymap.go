// Package keymap is the single declarative source for TUI keyboard behavior.
package keymap

import "strings"

type Scope string

const (
	Global        Scope = "global"
	MainInput     Scope = "main-input"
	MainTable     Scope = "main-table"
	MainDetail    Scope = "main-detail"
	MainHelp      Scope = "main-help"
	MainExport    Scope = "main-export"
	MainTopics    Scope = "main-topics"
	MainFilter    Scope = "main-filter"
	MainRank      Scope = "main-rank"
	ThreadCompose Scope = "thread-compose"
	ThreadList    Scope = "thread-list"
	ThreadHelp    Scope = "thread-help"
	ThreadPick    Scope = "thread-picker"
)

type Action string

const (
	None             Action = ""
	Quit             Action = "quit"
	Back             Action = "back"
	Up               Action = "up"
	Down             Action = "down"
	PageUp           Action = "page-up"
	PageDown         Action = "page-down"
	Home             Action = "home"
	End              Action = "end"
	Submit           Action = "submit"
	Edit             Action = "edit"
	CursorLeft       Action = "cursor-left"
	CursorRight      Action = "cursor-right"
	CursorStart      Action = "cursor-start"
	CursorEnd        Action = "cursor-end"
	DeleteBackward   Action = "delete-backward"
	DeleteForward    Action = "delete-forward"
	ClearInput       Action = "clear-input"
	ToggleHelp       Action = "toggle-help"
	ToggleTheme      Action = "toggle-theme"
	ToggleFullscreen Action = "toggle-fullscreen"
	OpenDetail       Action = "open-detail"
	NewRepository    Action = "new-repository"
	Filter           Action = "filter"
	Rank             Action = "rank"
	ClearFilter      Action = "clear-filter"
	GroupClusters    Action = "group-clusters"
	CycleSort        Action = "cycle-sort"
	ReverseSort      Action = "reverse-sort"
	OpenBrowser      Action = "open-browser"
	OpenCompare      Action = "open-compare"
	CycleTier        Action = "cycle-tier"
	Yank             Action = "yank"
	Refresh          Action = "refresh"
	ToggleMark       Action = "toggle-mark"
	ExportMarked     Action = "export-marked"
	ExportAll        Action = "export-all"
	Reply            Action = "reply"
	Resolve          Action = "resolve"
	ResolveAll       Action = "resolve-all"
	UnresolveAll     Action = "unresolve-all"
	ApplySuggestion  Action = "apply-suggestion"
	CounterPropose   Action = "counter-propose"
)

type Binding struct {
	Scope     Scope
	Keys      []string
	Action    Action
	Label     string
	Deviation string
}

// Registry drives dispatch and contextual help; alternates are intentionally
// co-located so neither path can silently drop a key from a multi-key action.
var Registry = []Binding{
	{Global, []string{"ctrl+c"}, Quit, "Quit", ""},
	{MainInput, []string{"enter"}, Submit, "Search repository", ""},
	{MainInput, []string{"esc"}, Back, "Return to fork table", ""},
	{MainInput, []string{"left", "ctrl+b"}, CursorLeft, "Move input cursor left", ""},
	{MainInput, []string{"right", "ctrl+f"}, CursorRight, "Move input cursor right", ""},
	{MainInput, []string{"home", "ctrl+a"}, CursorStart, "Move input cursor to start", ""},
	{MainInput, []string{"end", "ctrl+e"}, CursorEnd, "Move input cursor to end", ""},
	{MainInput, []string{"backspace"}, DeleteBackward, "Delete previous character", ""},
	{MainInput, []string{"delete"}, DeleteForward, "Delete next character", ""},
	{MainInput, []string{"ctrl+u"}, ClearInput, "Clear input", ""},
	{MainTable, []string{"q"}, Quit, "Quit", ""},
	{MainTable, []string{"up", "k"}, Up, "Move selection up", "Spoon adds vi navigation"},
	{MainTable, []string{"down", "j"}, Down, "Move selection down", "Spoon adds vi navigation"},
	{MainTable, []string{"pgup"}, PageUp, "Page up", ""},
	{MainTable, []string{"pgdown"}, PageDown, "Page down", ""},
	{MainTable, []string{"home"}, Home, "Go to top", ""},
	{MainTable, []string{"G", "end"}, End, "Go to bottom", "Spoon adds vi navigation"},
	{MainTable, []string{"g"}, GroupClusters, "Toggle cluster grouping", "No upstream counterpart"},
	{MainTable, []string{"enter"}, OpenDetail, "View fork details", ""},
	{MainTable, []string{"n"}, NewRepository, "Search new repository", "No upstream counterpart"},
	{MainTable, []string{"/"}, Filter, "Filter forks", "No upstream counterpart"},
	{MainTable, []string{"R"}, Rank, "Rank by intent", "No upstream counterpart"},
	{MainTable, []string{"esc"}, ClearFilter, "Clear active filter", ""},
	{MainTable, []string{"?"}, ToggleHelp, "Open help", ""},
	{MainTable, []string{"s"}, CycleSort, "Cycle sort column", "No upstream counterpart"},
	{MainTable, []string{"S"}, ReverseSort, "Reverse sort order", "No upstream counterpart"},
	{MainTable, []string{"o"}, OpenBrowser, "Open selected fork", "No upstream counterpart"},
	{MainTable, []string{"d"}, OpenCompare, "Open compare", "No upstream counterpart"},
	{MainTable, []string{"c"}, CycleTier, "Cycle enrichment ceiling", "No upstream counterpart"},
	{MainTable, []string{"t"}, ToggleTheme, "Toggle dark/light theme", ""},
	{MainTable, []string{"f"}, ToggleFullscreen, "Hide or restore chrome", ""},
	{MainTable, []string{"y"}, Yank, "Yank clone command", "No upstream counterpart"},
	{MainTable, []string{"r"}, Refresh, "Refresh", "No upstream counterpart"},
	{MainTable, []string{" "}, ToggleMark, "Mark or unmark fork", ""},
	{MainTable, []string{"e"}, ExportMarked, "Export marked forks", "No upstream counterpart"},
	{MainTable, []string{"E"}, ExportAll, "Export all forks", "No upstream counterpart"},
	{MainDetail, []string{"esc", "b", "q"}, Back, "Return to fork table", "Spoon maps q to back in detail; b is an added back shortcut"},
	{MainDetail, []string{"up", "k"}, Up, "Scroll up", "Spoon adds vi navigation"},
	{MainDetail, []string{"down", "j"}, Down, "Scroll down", "Spoon adds vi navigation"},
	{MainDetail, []string{"pgup"}, PageUp, "Page up", ""},
	{MainDetail, []string{"pgdown"}, PageDown, "Page down", ""},
	{MainDetail, []string{"home"}, Home, "Go to top", ""},
	{MainDetail, []string{"G", "end"}, End, "Go to bottom", "Spoon adds vi navigation"},
	{MainDetail, []string{"o"}, OpenBrowser, "Open selected fork", "No upstream counterpart"},
	{ThreadCompose, []string{"ctrl+s"}, Submit, "Submit reply", ""},
	{ThreadCompose, []string{"esc"}, Back, "Cancel reply", ""},
	{MainDetail, []string{"d"}, OpenCompare, "Open compare", "No upstream counterpart"},
	{MainDetail, []string{"c"}, CycleTier, "Cycle enrichment ceiling", "No upstream counterpart"},
	{MainDetail, []string{"t"}, ToggleTheme, "Toggle dark/light theme", ""},
	{MainDetail, []string{"f"}, ToggleFullscreen, "Hide or restore chrome", ""},
	{MainDetail, []string{"y"}, Yank, "Yank clone command", "No upstream counterpart"},
	{MainHelp, []string{"?", "esc", "q"}, Back, "Close help", ""},
	{MainHelp, []string{"up", "k"}, Up, "Scroll up", ""},
	{MainHelp, []string{"down", "j"}, Down, "Scroll down", ""},
	{MainHelp, []string{"pgup"}, PageUp, "Page up", ""},
	{MainHelp, []string{"pgdown"}, PageDown, "Page down", ""},
	{MainHelp, []string{"home"}, Home, "Go to top", ""},
	{MainHelp, []string{"G", "end"}, End, "Go to bottom", ""},
	{MainExport, []string{"enter"}, Submit, "Export", ""},
	{MainExport, []string{"esc"}, Back, "Cancel export", ""},
	{MainExport, []string{"left", "ctrl+b"}, CursorLeft, "Move input cursor left", ""},
	{MainExport, []string{"right", "ctrl+f"}, CursorRight, "Move input cursor right", ""},
	{MainExport, []string{"home", "ctrl+a"}, CursorStart, "Move input cursor to start", ""},
	{MainExport, []string{"end", "ctrl+e"}, CursorEnd, "Move input cursor to end", ""},
	{MainExport, []string{"backspace"}, DeleteBackward, "Delete previous character", ""},
	{MainExport, []string{"delete"}, DeleteForward, "Delete next character", ""},
	{MainExport, []string{"ctrl+u"}, ClearInput, "Clear input", ""},
	{MainTopics, []string{"up", "k"}, Up, "Move selection up", ""},
	{MainTopics, []string{"down", "j"}, Down, "Move selection down", ""},
	{MainTopics, []string{"enter"}, Submit, "Choose topic repository", ""},
	{MainTopics, []string{"esc", "q"}, Back, "Cancel topic picker", ""},
	{MainFilter, []string{"enter"}, Submit, "Apply filter", ""},
	{MainFilter, []string{"esc"}, Back, "Cancel filter", ""},
	{MainFilter, []string{"left", "ctrl+b"}, CursorLeft, "Move input cursor left", ""},
	{MainFilter, []string{"right", "ctrl+f"}, CursorRight, "Move input cursor right", ""},
	{MainFilter, []string{"home", "ctrl+a"}, CursorStart, "Move input cursor to start", ""},
	{MainFilter, []string{"end", "ctrl+e"}, CursorEnd, "Move input cursor to end", ""},
	{MainFilter, []string{"backspace"}, DeleteBackward, "Delete previous character", ""},
	{MainFilter, []string{"delete"}, DeleteForward, "Delete next character", ""},
	{MainFilter, []string{"ctrl+u"}, ClearInput, "Clear input", ""},
	{MainRank, []string{"enter"}, Submit, "Apply ranking (empty clears)", ""},
	{MainRank, []string{"esc"}, Back, "Cancel ranking", ""},
	{MainRank, []string{"left", "ctrl+b"}, CursorLeft, "Move input cursor left", ""},
	{MainRank, []string{"right", "ctrl+f"}, CursorRight, "Move input cursor right", ""},
	{MainRank, []string{"home", "ctrl+a"}, CursorStart, "Move input cursor to start", ""},
	{MainRank, []string{"end", "ctrl+e"}, CursorEnd, "Move input cursor to end", ""},
	{MainRank, []string{"backspace"}, DeleteBackward, "Delete previous character", ""},
	{MainRank, []string{"delete"}, DeleteForward, "Delete next character", ""},
	{MainRank, []string{"ctrl+u"}, ClearInput, "Clear input", ""},
	{ThreadList, []string{"up", "k"}, Up, "Move thread selection up", "Spoon adds vi navigation"},
	{ThreadList, []string{"down", "j"}, Down, "Move thread selection down", "Spoon adds vi navigation"},
	{ThreadList, []string{"q"}, Quit, "Quit", ""},
	{ThreadList, []string{"r", "enter"}, Reply, "Reply", "No upstream counterpart"},
	{ThreadList, []string{"R"}, Resolve, "Resolve current thread", "No upstream counterpart"},
	{ThreadList, []string{"a"}, ApplySuggestion, "Apply suggestion", "No upstream counterpart"},
	{ThreadList, []string{"ctrl+a"}, ResolveAll, "Resolve all", "No upstream counterpart"},
	{ThreadList, []string{"A"}, UnresolveAll, "Unresolve all", "No upstream counterpart"},
	{ThreadList, []string{"o"}, OpenBrowser, "Open pull request", "No upstream counterpart"},
	{ThreadHelp, []string{"?"}, ToggleHelp, "Close help", ""},
	{ThreadList, []string{"c"}, CounterPropose, "Counter-propose", "No upstream counterpart"},
	{ThreadList, []string{"?"}, ToggleHelp, "Toggle help", ""},
	{ThreadList, []string{"f"}, ToggleFullscreen, "Hide or restore chrome", ""},
	{ThreadPick, []string{"up", "k"}, Up, "Move selection up", ""},
	{ThreadPick, []string{"down", "j"}, Down, "Move selection down", ""},
	{ThreadPick, []string{"enter"}, Submit, "Choose pull request", ""},
	{ThreadPick, []string{"ctrl+c", "esc", "q"}, Quit, "Cancel picker", ""},
}

func Lookup(scope Scope, key string) (Action, bool) {
	for _, binding := range Registry {
		if binding.Scope != scope {
			continue
		}
		for _, candidate := range binding.Keys {
			if candidate == key {
				return binding.Action, true
			}
		}
	}
	return None, false
}

func Dispatch(scope Scope, key string) Action {
	if action, ok := Lookup(scope, key); ok {
		return action
	}
	if scope != Global {
		if action, ok := Lookup(Global, key); ok {
			return action
		}
	}
	return None
}

func ForScopes(scopes ...Scope) []Binding {
	want := make(map[Scope]bool, len(scopes))
	for _, scope := range scopes {
		want[scope] = true
	}
	bindings := make([]Binding, 0)
	for _, binding := range Registry {
		if want[binding.Scope] {
			bindings = append(bindings, binding)
		}
	}
	return bindings
}

func KeyLabel(keys []string) string {
	parts := append([]string(nil), keys...)
	for i := range parts {
		switch parts[i] {
		case "up":
			parts[i] = "Up"
		case "down":
			parts[i] = "Down"
		case "pgup":
			parts[i] = "PgUp"
		case "pgdown":
			parts[i] = "PgDn"
		case "esc":
			parts[i] = "Esc"
		case "enter":
			parts[i] = "Enter"
		case " ":
			parts[i] = "Space"
		}
	}
	return strings.Join(parts, "/")
}
