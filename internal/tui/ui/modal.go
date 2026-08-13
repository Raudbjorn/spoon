package ui

import "github.com/svnbjrn/spoon/internal/tui/theme"

// OverlayKind identifies the active composition without involving view routing.
type OverlayKind int

const (
	ModalOverlay OverlayKind = iota + 1
	SheetOverlay
)

// Overlay records the focus token that must be restored when an overlay closes.
// It is event-first: callers ask it before dispatching any background key.
type Overlay struct {
	kind  OverlayKind
	focus string
}

func (o *Overlay) Open(kind OverlayKind, focus string) {
	o.kind = kind
	o.focus = focus
}

func (o Overlay) IsOpen() bool      { return o.kind != 0 }
func (o Overlay) Kind() OverlayKind { return o.kind }

// HandleKey consumes every key while open. Esc closes and returns the saved
// focus token exactly once; a later event sees a closed overlay.
func (o *Overlay) HandleKey(key string) (handled bool, restoredFocus string) {
	if !o.IsOpen() {
		return false, ""
	}
	if key != "esc" {
		return true, ""
	}
	restoredFocus = o.focus
	o.kind = 0
	o.focus = ""
	return true, restoredFocus
}

// Modal is a Spoon-owned consequence-confirmation composition absent from
// molecules.crepus. Its contract comes from phase-4b-molecules.md:32,65-71.
func Modal(ctx theme.Context, title, message string, width int) string {
	return Box(ctx, width, []BoxPart{
		{Text: Heading(ctx, 2, title, width-4)},
		{Divider: true},
		{Text: Text(ctx, TextDefault, message, width-4)},
		{Text: Text(ctx, TextMuted, "Esc closes", width-4)},
	})
}
