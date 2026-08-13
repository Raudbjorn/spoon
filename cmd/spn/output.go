package main

import (
	"encoding/csv"
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

// StreamKind classifies output before it reaches a writer. Data is never
// decorated; Human and Error are presentation-only streams.
type StreamKind string

const (
	StreamData  StreamKind = "data"
	StreamHuman StreamKind = "human"
	StreamError StreamKind = "error"
)

// Every cmd/spn output path is one of these kinds. Raw writes are confined to
// this file; callers use typed data, human, or error helpers below.

type semanticRole string

const (
	roleText       semanticRole = "text"
	roleTextStrong semanticRole = "text-strong"
	roleTextMuted  semanticRole = "text-muted"
	roleTextFaint  semanticRole = "text-faint"
	roleAccent     semanticRole = "accent"
	roleSuccess    semanticRole = "success"
	roleWarning    semanticRole = "warning"
	roleError      semanticRole = "error"
	roleInfo       semanticRole = "info"
)

type presentation struct {
	ctx theme.Context
}

// resolvePresentation implements spn's deliberately pipeline-first precedence:
// its NO_COLOR rule is stricter than the interactive TUI's.
func resolvePresentation(noColor bool, color, noColorEnv string, stdoutIsTTY bool) (presentation, error) {
	profile := theme.TrueColor
	switch {
	case noColor:
		profile = theme.NoColor
	case noColorEnv != "":
		profile = theme.NoColor
	case color != "":
		var err error
		profile, err = theme.ParseColorProfile(color)
		if err != nil {
			return presentation{}, err
		}
	case !stdoutIsTTY:
		profile = theme.NoColor
	}
	ctx, err := theme.ResolveContext("", "", profile.String(), "", "")
	if err != nil {
		return presentation{}, err
	}
	theme.PinColorProfile(ctx)
	return presentation{ctx: ctx}, nil
}

func resolveStartupPresentation(noColor bool, stdout io.Writer) (presentation, error) {
	return resolvePresentation(noColor, os.Getenv("SPOON_TUI_COLOR"), os.Getenv("NO_COLOR"), isTerminalWriter(stdout))
}

func isTerminalWriter(w io.Writer) bool {
	file, ok := w.(*os.File)
	return ok && (isatty.IsTerminal(file.Fd()) || isatty.IsCygwinTerminal(file.Fd()))
}

func (p presentation) render(kind StreamKind, role semanticRole, text string) string {
	if kind == StreamData || p.ctx.ColorProfile == theme.NoColor {
		return text
	}
	return lipgloss.NewStyle().Foreground(p.color(role)).Render(text)
}

func writeHuman(target io.Writer, p presentation, role semanticRole, text string) error {
	return writeRendered(target, p.render(StreamHuman, role, text))
}

func writeError(target io.Writer, p presentation, role semanticRole, text string) error {
	return writeRendered(target, p.render(StreamError, role, text))
}

func writeRendered(target io.Writer, text string) error {
	for bytes := []byte(text); len(bytes) > 0; {
		n, err := target.Write(bytes)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		bytes = bytes[n:]
	}
	return nil
}

func writeDataJSON(target io.Writer, value any) error {
	return agentio.WriteJSON(target, value)
}

func writeDataNDJSON(target io.Writer, value any) error {
	return agentio.WriteNDJSON(target, value)
}

func emitDataError(target io.Writer, err *agentio.Error) int {
	return err.Emit(target)
}

type dataCSV struct{ writer *csv.Writer }

func newDataCSV(target io.Writer) dataCSV {
	return dataCSV{writer: csv.NewWriter(target)}
}

func (w dataCSV) writeRecord(record []string) error {
	return w.writer.Write(record)
}

func (w dataCSV) flush() error {
	w.writer.Flush()
	return w.writer.Error()
}

// debugDataLogger is the only sanctioned writer handoff to fork streaming:
// debugging remains a machine-data-adjacent diagnostic, never presentation.
func debugDataLogger(target io.Writer) io.Writer { return target }

func (p presentation) color(role semanticRole) lipgloss.Color {
	switch role {
	case roleText:
		return p.ctx.Palette.Text
	case roleTextStrong:
		return p.ctx.Palette.TextStrong
	case roleTextMuted:
		return p.ctx.Palette.TextMuted
	case roleTextFaint:
		return p.ctx.Palette.TextFaint
	case roleAccent:
		return p.ctx.Palette.Accent
	case roleSuccess:
		return p.ctx.Palette.Success
	case roleWarning:
		return p.ctx.Palette.Warning
	case roleError:
		return p.ctx.Palette.Error
	case roleInfo:
		return p.ctx.Palette.Info
	default:
		panic("unknown semantic role: " + string(role))
	}
}
