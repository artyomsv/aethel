//go:build linux || darwin || freebsd

package clipboard

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

func read() (string, error) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbpaste")
	default:
		// Try xclip first, then xsel.
		if path, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command(path, "-selection", "clipboard", "-o")
		} else if path, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command(path, "--clipboard", "--output")
		} else {
			return "", fmt.Errorf("no clipboard tool found (install xclip or xsel)")
		}
	}

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("clipboard read: %w", err)
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}
