package theme

import (
	"fmt"
	"strings"
)

// GlyphProfile chooses Unicode symbols or same-width ASCII alternatives.
type GlyphProfile int

const (
	Unicode GlyphProfile = iota
	Ascii
)

// ParseGlyphProfile parses SPOON_TUI_GLYPHS. Empty is the Unicode default.
func ParseGlyphProfile(value string) (GlyphProfile, error) {
	switch value {
	case "", "unicode":
		return Unicode, nil
	case "ascii":
		return Ascii, nil
	default:
		return Unicode, fmt.Errorf("invalid SPOON_TUI_GLYPHS value %q (want unicode or ascii)", value)
	}
}

// Glyph names every non-ASCII rendering symbol. Wider pictographs deliberately
// have text labels instead: Bubble Tea composes strings and cannot release a
// trailing terminal cell after replacing a width-two glyph.
type Glyph string

const (
	HeatEmpty      Glyph = "heat-empty"
	HeatDim        Glyph = "heat-dim"
	HeatMedium     Glyph = "heat-medium"
	HeatFull       Glyph = "heat-full"
	Cursor         Glyph = "cursor"
	BoxTopLeft     Glyph = "box-top-left"
	BoxTopRight    Glyph = "box-top-right"
	BoxBottomLeft  Glyph = "box-bottom-left"
	BoxBottomRight Glyph = "box-bottom-right"
	BoxHorizontal  Glyph = "box-horizontal"
	BoxVertical    Glyph = "box-vertical"
	BoxTeeRight    Glyph = "box-tee-right"
	BoxTeeLeft     Glyph = "box-tee-left"
	ArrowRight     Glyph = "arrow-right"
	ArrowLeft      Glyph = "arrow-left"
	ArrowUp        Glyph = "arrow-up"
	ArrowDown      Glyph = "arrow-down"
	Warning        Glyph = "warning"
	Cross          Glyph = "cross"
	Check          Glyph = "check"
	SortUp         Glyph = "sort-up"
	SortDown       Glyph = "sort-down"
	Selected       Glyph = "selected"
	Star           Glyph = "star"
	Fork           Glyph = "fork"
	Gutter         Glyph = "gutter"
	Separator      Glyph = "separator"
	EmDash         Glyph = "em-dash"
)

type glyphPair struct {
	Unicode string
	ASCII   string
}

var glyphTable = map[Glyph]glyphPair{
	HeatEmpty: {Unicode: "░", ASCII: "."}, HeatDim: {Unicode: "▒", ASCII: ":"}, HeatMedium: {Unicode: "▓", ASCII: "*"}, HeatFull: {Unicode: "█", ASCII: "#"},
	Cursor:     {Unicode: "█", ASCII: "_"},
	BoxTopLeft: {Unicode: "╭", ASCII: "+"}, BoxTopRight: {Unicode: "╮", ASCII: "+"}, BoxBottomLeft: {Unicode: "╰", ASCII: "+"}, BoxBottomRight: {Unicode: "╯", ASCII: "+"},
	BoxHorizontal: {Unicode: "─", ASCII: "-"}, BoxVertical: {Unicode: "│", ASCII: "|"}, BoxTeeRight: {Unicode: "├", ASCII: "+"}, BoxTeeLeft: {Unicode: "┤", ASCII: "+"},
	ArrowRight: {Unicode: "→", ASCII: "-"}, ArrowLeft: {Unicode: "←", ASCII: "<"}, ArrowUp: {Unicode: "↑", ASCII: "^"}, ArrowDown: {Unicode: "↓", ASCII: "v"},
	Warning: {Unicode: "⚠", ASCII: "!"}, Cross: {Unicode: "✘", ASCII: "x"}, Check: {Unicode: "✓", ASCII: "+"},
	SortUp: {Unicode: "▲", ASCII: "^"}, SortDown: {Unicode: "▼", ASCII: "v"}, Selected: {Unicode: "▸", ASCII: ">"},
	Star: {Unicode: "★", ASCII: "*"}, Fork: {Unicode: "⑂", ASCII: "f"}, Gutter: {Unicode: "┃", ASCII: "|"}, Separator: {Unicode: "·", ASCII: "."}, EmDash: {Unicode: "—", ASCII: "-"},
}

// Glyph returns a centralized rendering symbol under this context's profile.
func (c Context) Glyph(name Glyph) string {
	pair, ok := glyphTable[name]
	if !ok {
		return "?"
	}
	if c.GlyphProfile == Ascii {
		return pair.ASCII
	}
	return pair.Unicode
}

// ASCIIFallback ports the general upstream profile.rs degradation rule. The
// table is the only source for Spoon's known rendering glyphs; this handles
// unanticipated input safely.
func ASCIIFallback(symbol string) string {
	for _, character := range symbol {
		if character >= '!' && character <= '~' {
			return string(character)
		}
	}
	switch symbol {
	case "─", "━", "╌", "╍", "┄", "┅", "┈", "┉", "═":
		return "-"
	case "│", "┃", "╎", "╏", "┆", "┇", "┊", "┋", "║":
		return "|"
	case "✅", "✓", "✔":
		return "+"
	case "⚠", "⚠️":
		return "!"
	case "❌", "✘", "✖":
		return "x"
	case "→", "↗", "⇒":
		return ">"
	case "←", "↙", "⇐":
		return "<"
	case "↑", "⇑":
		return "^"
	case "↓", "⇓":
		return "v"
	}
	first := '?'
	for _, character := range symbol {
		first = character
		break
	}
	switch {
	case first >= '\u2500' && first <= '\u257f':
		return "+"
	case first >= '\u2580' && first <= '\u259f':
		return "#"
	case first >= '\u2800' && first <= '\u28ff':
		return "*"
	case (first >= '\u4e00' && first <= '\u9fff') || (first >= '\ue000' && first <= '\uf8ff'):
		return "?"
	case strings.ContainsRune(symbol, '\u200d') || isEmoji(first):
		return "*"
	default:
		return "?"
	}
}

func isEmoji(character rune) bool {
	return (character >= '\U0001f000' && character <= '\U0001faff') || (character >= '\u2600' && character <= '\u27bf')
}
