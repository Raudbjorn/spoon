package embed

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// StdinPrompter is the default Prompter for non-TUI spoon paths.
//
// AskPull writes the prompt to Stderr and reads a single line from Stdin.
// ProgressFunc returns a callback that renders a CR-updated progress line to
// Stderr, clearing it (\r\033[K) and writing "spoon: pulled <model>\n" on
// the "done" phase. The model name printed by ProgressFunc is captured from
// the most recent AskPull call.
//
// Construct via NewStdinPrompter. Fields are exposed for tests.
type StdinPrompter struct {
	Stderr io.Writer
	Stdin  io.Reader

	mu    sync.Mutex
	model string
}

// NewStdinPrompter returns a StdinPrompter wired to os.Stderr and os.Stdin.
func NewStdinPrompter() *StdinPrompter {
	return &StdinPrompter{Stderr: os.Stderr, Stdin: os.Stdin}
}

func (s *StdinPrompter) stderr() io.Writer {
	if s.Stderr != nil {
		return s.Stderr
	}
	return os.Stderr
}

func (s *StdinPrompter) stdin() io.Reader {
	if s.Stdin != nil {
		return s.Stdin
	}
	return os.Stdin
}

// AskPull writes a yes/no prompt to Stderr and reads a line from Stdin.
// An empty line (user pressed Enter) is treated as yes; an answer beginning
// with 'n' or 'N' is no; any other input is yes. If stdin reaches EOF before
// any input is read (e.g. closed pipe, /dev/null, CI environment), AskPull
// returns (false, nil) so a non-interactive caller cannot silently consent
// to a pull.
func (s *StdinPrompter) AskPull(model string, sizeMB int) (bool, error) {
	s.mu.Lock()
	s.model = model
	s.mu.Unlock()

	if _, err := fmt.Fprintf(s.stderr(), "No embedding model found on Ollama. Pull %s (%d MB)? [Y/n] ", model, sizeMB); err != nil {
		return false, err
	}
	reader := bufio.NewReader(s.stdin())
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	// EOF with no bytes read: nothing on stdin at all. Decline rather than
	// silently consenting to a ~hundreds-of-MB pull.
	if line == "" && errors.Is(err, io.EOF) {
		return false, nil
	}
	answer := strings.TrimSpace(line)
	if answer == "" {
		return true, nil
	}
	if answer[0] == 'n' || answer[0] == 'N' {
		return false, nil
	}
	return true, nil
}

// ProgressFunc returns a callback that renders pull progress to Stderr.
// For non-done phases the line is "\rspoon: pulling <model>: <phase> <pct%>".
// On the "done" phase the line is cleared and a final "spoon: pulled <model>\n"
// is written.
func (s *StdinPrompter) ProgressFunc() func(phase string, pct float64) {
	return func(phase string, pct float64) {
		s.mu.Lock()
		model := s.model
		s.mu.Unlock()
		w := s.stderr()
		if phase == "done" {
			fmt.Fprint(w, "\r\033[K")
			fmt.Fprintf(w, "spoon: pulled %s\n", model)
			return
		}
		fmt.Fprintf(w, "\rspoon: pulling %s: %s %d%%", model, phase, int(pct*100))
	}
}
