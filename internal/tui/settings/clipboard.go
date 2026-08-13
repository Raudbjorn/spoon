package settings

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// copySettingValue is the only clipboard boundary in Settings. Tests inject a
// recorder through Model.WithClipboard; credential fields never reach it.
func copySettingValue(value string) error {
	var command string
	var args []string
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "":
		command = "wl-copy"
	case os.Getenv("DISPLAY") != "":
		command, args = "xclip", []string{"-selection", "clipboard"}
	case runtime.GOOS == "darwin":
		command = "pbcopy"
	default:
		return fmt.Errorf("no clipboard command available")
	}
	cmd := exec.Command(command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := stdin.Write([]byte(value)); err != nil {
		_ = stdin.Close()
		return err
	}
	if err := stdin.Close(); err != nil {
		return err
	}
	return cmd.Wait()
}
