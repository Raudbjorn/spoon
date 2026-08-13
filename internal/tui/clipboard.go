package tui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/forge"
)

type clipboardMsg struct {
	success bool
	cmd     string // the clone command
	err     error
}

// yankCloneCommand copies the git clone command for the selected fork.
func (m *Model) yankCloneCommand() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.forks) {
		return nil
	}
	sf := m.forks[m.cursor]
	branch := ""
	if sf.T2 != nil && sf.T2.IsBranchWork {
		branch = sf.T2.ActiveBranch
	}
	cmd := forge.FormatCloneCmd(sf.Fork.URL, sf.Fork.Name, branch, sf.Fork.DefaultBranch)

	return func() tea.Msg {
		err := copyToClipboard(cmd)
		return clipboardMsg{success: err == nil, cmd: cmd, err: err}
	}
}

// copyToClipboard tries platform-specific clipboard commands.
func copyToClipboard(text string) error {
	// 1. Wayland
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return pipeToCmd("wl-copy", text)
	}

	// 2. X11
	if os.Getenv("DISPLAY") != "" {
		if err := pipeToCmd("xclip", text, "-selection", "clipboard"); err == nil {
			return nil
		}
		return pipeToCmd("xsel", text, "-b")
	}

	// 3. macOS
	if runtime.GOOS == "darwin" {
		return pipeToCmd("pbcopy", text)
	}

	return fmt.Errorf("no clipboard command available")
}

// CopyToClipboard exposes the established platform clipboard boundary to
// settings without duplicating command selection.
func CopyToClipboard(text string) error {
	return copyToClipboard(text)
}

func pipeToCmd(name string, input string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = nil

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	_, _ = stdin.Write([]byte(input))
	stdin.Close()

	return cmd.Wait()
}
