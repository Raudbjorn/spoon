package ui

import "github.com/svnbjrn/spoon/internal/tui/theme"

// ButtonState is the presentation state of an existing action.
type ButtonState struct {
	Label   string
	Focused bool
	Enabled bool
	Loading bool
}

// Button derives from molecules.crepus:29-38. It composes the atom field
// treatment so focus stays visible when the selected profile has no color.
func Button(ctx theme.Context, state ButtonState, width int) string {
	label := state.Label
	if state.Loading {
		label = "loading " + label
	}
	return field(ctx, fieldPrefix(state.Focused, state.Enabled)+Text(ctx, TextStrong, label, width-2), state.Focused, state.Enabled, width)
}
