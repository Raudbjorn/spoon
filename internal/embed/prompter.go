package embed

import (
	"bufio"
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
// Empty input is treated as yes; an answer beginning with 'n' or 'N' is no;
// any other input is yes.
func (s *StdinPrompter) AskPull(model string, sizeMB int) (bool, error) {
	s.mu.Lock()
	s.model = model
	s.mu.Unlock()

	if _, err := fmt.Fprintf(s.stderr(), "No embedding model found on Ollama. Pull %s (%d MB)? [Y/n] ", model, sizeMB); err != nil {
		return false, err
	}
	reader := bufio.NewReader(s.stdin())
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
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
