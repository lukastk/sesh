package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// copyToClipboard writes text to the system clipboard.
// macOS: pbcopy. Linux: wl-copy (Wayland), xclip, or xsel (X11).
// Termux: termux-clipboard-set.
func copyToClipboard(text string) error {
	cmd, err := clipboardCmd()
	if err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func clipboardCmd() (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("pbcopy"), nil
	case "linux":
		for _, candidate := range []struct{ bin, arg string }{
			{"wl-copy", ""},
			{"xclip", "-selection\x00clipboard"},
			{"xsel", "--clipboard\x00--input"},
			{"termux-clipboard-set", ""},
		} {
			if _, err := exec.LookPath(candidate.bin); err != nil {
				continue
			}
			if candidate.arg == "" {
				return exec.Command(candidate.bin), nil
			}
			args := strings.Split(candidate.arg, "\x00")
			return exec.Command(candidate.bin, args...), nil
		}
		return nil, fmt.Errorf("no clipboard tool found (install wl-copy, xclip, or xsel)")
	default:
		return nil, fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
	}
}
