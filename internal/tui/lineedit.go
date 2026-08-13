package tui

import "github.com/svnbjrn/spoon/internal/tui/edit"

// Existing prompts retain these private names while all mutation and paste
// handling lives in edit. Settings consumes the exact same implementation.
var (
	typedText = edit.TypedText
	lineEdit  = edit.Apply
)
