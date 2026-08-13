package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// TextVariant is the semantic text role used by Text.
type TextVariant string

const (
	TextDefault TextVariant = "default"
	TextStrong  TextVariant = "strong"
	TextMuted   TextVariant = "muted"
	TextFaint   TextVariant = "faint"
)

// AlertTone selects Alert's semantic severity color.
type AlertTone string

const (
	AlertInfo    AlertTone = "info"
	AlertSuccess AlertTone = "success"
	AlertWarning AlertTone = "warning"
	AlertError   AlertTone = "error"
)

// InputState is a read-only projection of an existing line edit state.
type InputState struct {
	Value            string
	Cursor           int
	Focused, Enabled bool
}

// SelectState is the display state of a single-row select control.
type SelectState struct {
	Label, Value     string
	Focused, Enabled bool
	Open             bool
}

// ToggleState is the display state shared by switch, checkbox, and radio.
type ToggleState struct {
	Label            string
	On               bool
	Focused, Enabled bool
}

// BoxPart is a content line or a divider in a Box.
type BoxPart struct {
	Text    string
	Divider bool
}

// Text derives from atoms.crepus:1-12 and phase-4-component-grammar.md:45-60.
func Text(ctx theme.Context, variant TextVariant, label string, width int) string {
	style := lipgloss.NewStyle()
	switch variant {
	case TextStrong:
		style = role(ctx, ctx.Palette.TextStrong).Bold(true)
	case TextMuted:
		style = role(ctx, ctx.Palette.TextMuted).Faint(true)
	case TextFaint:
		style = role(ctx, ctx.Palette.TextFaint).Faint(true).Italic(true)
	default:
		style = role(ctx, ctx.Palette.Text)
	}
	return style.Render(truncate(label, width))
}

// Heading derives from atoms.crepus:14-29 and phase-4-component-grammar.md:45-60.
func Heading(ctx theme.Context, level int, title string, width int) string {
	style := role(ctx, ctx.Palette.TextStrong)
	switch level {
	case 1:
		style = role(ctx, ctx.Palette.Accent).Bold(true).Underline(true)
	case 2:
		style = style.Bold(true).Underline(true)
	case 3:
		style = style.Bold(true)
	case 4:
		style = role(ctx, ctx.Palette.Text)
	default:
		style = style.Bold(true)
	}
	return style.Render(truncate(title, width))
}

// Badge derives from atoms.crepus:40-42 and phase-4-component-grammar.md:62-67.
func Badge(ctx theme.Context, label string, width int) string {
	return role(ctx, ctx.Palette.TextMuted).Background(background(ctx, ctx.Palette.Surface1)).Render(bracketed(label, width))
}

// Kbd derives from atoms.crepus:36-38 and phase-4-component-grammar.md:62-67.
func Kbd(ctx theme.Context, key string, width int) string {
	return role(ctx, ctx.Palette.Text).Background(background(ctx, ctx.Palette.Bg)).Render(bracketed(key, width))
}

// Alert derives from phase-4-component-grammar.md:69-72.
func Alert(ctx theme.Context, tone AlertTone, message string, width int) string {
	color := ctx.Palette.Info
	label := "info"
	switch tone {
	case AlertSuccess:
		color, label = ctx.Palette.Success, "success"
	case AlertWarning:
		color, label = ctx.Palette.Warning, "warning"
	case AlertError:
		color, label = ctx.Palette.Error, "error"
	}
	return role(ctx, color).Bold(true).Render(truncate(label+": "+message, width))
}

// Input derives from phase-4-component-grammar.md:74-85. It delegates cursor
// rendering to LineEdit so app prompts and field rows share one implementation.
func Input(ctx theme.Context, state InputState, width int) string {
	prefix := fieldPrefix(state.Focused, state.Enabled)
	return field(ctx, prefix+inputValue(ctx, state, width-lipgloss.Width(prefix)), state.Focused, state.Enabled, width)
}

// Select derives from phase-4-component-grammar.md:74-80.
func Select(ctx theme.Context, state SelectState, width int) string {
	arrow := ctx.Glyph(theme.ArrowDown)
	if state.Open {
		arrow = ctx.Glyph(theme.ArrowUp)
	}
	prefix, suffix := fieldPrefix(state.Focused, state.Enabled), " ["+arrow+"]"
	content := truncateBounded(state.Label+": "+state.Value, width-lipgloss.Width(prefix)-lipgloss.Width(suffix))
	return field(ctx, prefix+content+suffix, state.Focused, state.Enabled, width)
}

// Switch derives from phase-4-component-grammar.md:74-80.
func Switch(ctx theme.Context, state ToggleState, width int) string {
	value := "off"
	if state.On {
		value = "on"
	}
	prefix, suffix := fieldPrefix(state.Focused, state.Enabled), " ["+value+"]"
	return field(ctx, prefix+truncateBounded(state.Label, width-lipgloss.Width(prefix)-lipgloss.Width(suffix))+suffix, state.Focused, state.Enabled, width)
}

// Checkbox derives from phase-4-component-grammar.md:74-80.
func Checkbox(ctx theme.Context, state ToggleState, width int) string {
	mark := " "
	if state.On {
		mark = ctx.Glyph(theme.Check)
	}
	return field(ctx, fieldPrefix(state.Focused, state.Enabled)+"["+mark+"] "+state.Label, state.Focused, state.Enabled, width)
}

// Radio derives from phase-4-component-grammar.md:74-80.
func Radio(ctx theme.Context, state ToggleState, width int) string {
	mark := " "
	if state.On {
		mark = "*"
	}
	return field(ctx, fieldPrefix(state.Focused, state.Enabled)+"("+mark+") "+state.Label, state.Focused, state.Enabled, width)
}

// Box derives from phase-4-component-grammar.md:87-91. It owns all border
// glyph selection and guarantees each returned line fits width terminal cells.
func Box(ctx theme.Context, width int, parts []BoxPart) string {
	if width < 2 {
		return ""
	}
	horizontal := strings.Repeat(ctx.Glyph(theme.BoxHorizontal), width-2)
	var out strings.Builder
	out.WriteString(ctx.Glyph(theme.BoxTopLeft) + horizontal + ctx.Glyph(theme.BoxTopRight))
	for _, part := range parts {
		out.WriteByte('\n')
		if part.Divider {
			out.WriteString(ctx.Glyph(theme.BoxTeeRight) + horizontal + ctx.Glyph(theme.BoxTeeLeft))
			continue
		}
		out.WriteString(boxLine(ctx, " "+part.Text, width))
	}
	out.WriteByte('\n')
	out.WriteString(ctx.Glyph(theme.BoxBottomLeft) + horizontal + ctx.Glyph(theme.BoxBottomRight))
	return out.String()
}

func fieldPrefix(focused, enabled bool) string {
	switch {
	case focused && enabled:
		return "> "
	case focused:
		return "! "
	case enabled:
		return "  "
	default:
		return "x "
	}
}

func field(ctx theme.Context, value string, focused, enabled bool, width int) string {
	color := ctx.Palette.Text
	style := role(ctx, color)
	if !enabled {
		style = role(ctx, ctx.Palette.TextFaint).Faint(true)
	}
	if focused {
		style = style.Bold(true)
	}
	return style.Render(truncate(value, width))
}

func boxLine(ctx theme.Context, line string, width int) string {
	contentWidth := width - 2
	line = ansi.Truncate(line, contentWidth, "")
	return ctx.Glyph(theme.BoxVertical) + line + strings.Repeat(" ", contentWidth-lipgloss.Width(line)) + ctx.Glyph(theme.BoxVertical)
}

// inputValue projects a cursor-centered suffix into available terminal cells.
// Mutation remains in tui.lineEdit; this only prevents a long prefix from
// hiding the editing cursor and the text immediately after it.
func inputValue(ctx theme.Context, state InputState, available int) string {
	if available <= 0 {
		return ""
	}
	if !state.Focused || !state.Enabled {
		return truncateBounded(state.Value, available)
	}
	clusters := graphemes(state.Value)
	cursor := state.Cursor
	if cursor < 0 {
		cursor = 0
	}
	if len(clusters) == 0 || cursor >= clusters[len(clusters)-1].end {
		marker := ctx.Glyph(theme.Cursor)
		return suffixClusters(clusters, available-lipgloss.Width(marker)) + marker
	}
	return LineEdit(ctx, truncateClusters(clusters[clusterAtCursor(clusters, cursor):], available), 0)
}

// LineEdit renders a cursor over one complete grapheme cluster. tui.lineEdit
// retains its rune-indexed mutation semantics; this presentation layer maps a
// cursor that lands on a combining rune or ZWJ continuation back to its whole
// user-visible cluster.
func LineEdit(ctx theme.Context, text string, cursor int) string {
	clusters := graphemes(text)
	if len(clusters) == 0 || cursor >= clusters[len(clusters)-1].end {
		return text + ctx.Glyph(theme.Cursor)
	}
	if cursor < 0 {
		cursor = 0
	}
	index := clusterAtCursor(clusters, cursor)
	var out strings.Builder
	for _, cluster := range clusters[:index] {
		out.WriteString(cluster.text)
	}
	out.WriteString(lipgloss.NewStyle().Reverse(true).Render(clusters[index].text))
	for _, cluster := range clusters[index+1:] {
		out.WriteString(cluster.text)
	}
	return out.String()
}

type grapheme struct {
	text       string
	start, end int
}

func graphemes(value string) []grapheme {
	segmenter := uniseg.NewGraphemes(value)
	clusters := make([]grapheme, 0, utf8.RuneCountInString(value))
	start := 0
	for segmenter.Next() {
		text := segmenter.Str()
		end := start + utf8.RuneCountInString(text)
		clusters = append(clusters, grapheme{text: text, start: start, end: end})
		start = end
	}
	return clusters
}

func clusterAtCursor(clusters []grapheme, cursor int) int {
	for i, cluster := range clusters {
		if cursor < cluster.end {
			return i
		}
	}
	return len(clusters) - 1
}

func truncateClusters(clusters []grapheme, width int) string {
	if width <= 0 {
		return ""
	}
	var out strings.Builder
	used := 0
	for _, cluster := range clusters {
		clusterWidth := lipgloss.Width(cluster.text)
		if used+clusterWidth > width {
			break
		}
		out.WriteString(cluster.text)
		used += clusterWidth
	}
	return out.String()
}

func bracketed(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if width == 1 {
		return "["
	}
	if width == 2 {
		return "[]"
	}
	return "[" + truncateBounded(value, width-2) + "]"
}

func suffixClusters(clusters []grapheme, width int) string {
	if width <= 0 {
		return ""
	}
	start, used := len(clusters), 0
	for start > 0 {
		clusterWidth := lipgloss.Width(clusters[start-1].text)
		if used+clusterWidth > width {
			break
		}
		start--
		used += clusterWidth
	}
	var out strings.Builder
	for _, cluster := range clusters[start:] {
		out.WriteString(cluster.text)
	}
	return out.String()
}

func truncateBounded(value string, width int) string {
	return truncateClusters(graphemes(value), width)
}

// truncate preserves existing direct-model test behavior: a non-positive width
// means no WindowSizeMsg has arrived, so no terminal width has been supplied.
func truncate(value string, width int) string {
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	// A cut at a word boundary may leave a literal space as the final cell.
	// Drop it so source-controlled goldens never hide a trailing-whitespace
	// change while still keeping the result within the supplied terminal width.
	return strings.TrimRight(ansi.Truncate(value, width, ""), " ")
}

func role(ctx theme.Context, color lipgloss.Color) lipgloss.Style {
	if ctx.ColorProfile >= theme.Mono {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(color)
}

func background(ctx theme.Context, color lipgloss.Color) lipgloss.Color {
	if ctx.ColorProfile >= theme.Mono {
		return ""
	}
	return color
}
