package tui

// Copying to the system clipboard (the uuid popup's `c`). Worked on macOS and
// nowhere else, for three independent reasons (ticket b7da691e), each of which
// shapes the code below:
//
//   - termux builds are GOOS=android (plain `go build`, H22), not linux, so a
//     linux-only branch never reached termux-clipboard-set: the copy failed with
//     "clipboard not supported on android".
//   - wl-copy and xclip FORK a child that stays behind to serve the selection and
//     inherits the tool's stdio. Capturing output through a pipe (CombinedOutput)
//     makes Wait block until that child exits — i.e. until something else takes
//     the clipboard — and because the copy ran inside Update, the whole TUI froze.
//     So no pipe is ever handed to the tool: stdin is an *os.File pipe written and
//     closed before start, stdout is /dev/null, and stderr goes to a temp FILE.
//     Wait then returns when the tool itself exits, whatever it leaves running.
//   - a TUI in a work-server popup inherits the boot-started tmux server's env,
//     which holds no WAYLAND_DISPLAY/DISPLAY, so wl-copy had no compositor to talk
//     to. That env is read from its canonical source (internal/sessionenv), the
//     same one the daemon injects into spawned panes.
//
// The copy runs as a tea.Cmd, never inline in Update: termux-clipboard-set takes
// ~0.8s (it round-trips through the Termux:API app), and a tool that wedges must
// cost a bounded, LOUD error rather than a frozen TUI.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/sessionenv"
)

// clipboardTimeout bounds one copy. Generous against termux's ~0.8s; its point is
// the tool that never returns (termux-clipboard-set without the Termux:API app
// installed blocks forever).
var clipboardTimeout = 10 * time.Second // var: tests shorten it

// clipboardDoneMsg reports a finished copy. what names the thing copied, for the
// confirmation note.
type clipboardDoneMsg struct {
	what string
	err  error
}

// copyToClipboardCmd copies text off the Update loop; the result arrives as a
// clipboardDoneMsg.
func copyToClipboardCmd(text, what string) tea.Cmd {
	return func() tea.Msg {
		return clipboardDoneMsg{what: what, err: copyToClipboard(text)}
	}
}

// copyToClipboard writes text to this machine's system clipboard.
func copyToClipboard(text string) error {
	name, args, err := clipboardTool(runtime.GOOS, exec.LookPath)
	if err != nil {
		return err
	}
	env, envErr := clipboardEnv(runtime.GOOS, os.Environ(), sessionenv.Graphical)

	ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env // nil = inherit this process's env

	stdin, stdinW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("clipboard stdin pipe: %w", err)
	}
	defer stdin.Close()
	// Clipboard payloads here are a uuid — far under the pipe buffer, so the write
	// completes before the reader exists.
	if _, err := stdinW.WriteString(text); err != nil {
		stdinW.Close()
		return fmt.Errorf("clipboard stdin: %w", err)
	}
	if err := stdinW.Close(); err != nil {
		return fmt.Errorf("clipboard stdin: %w", err)
	}
	cmd.Stdin = stdin

	stderr, err := os.CreateTemp("", "sesh-clipboard-*")
	if err != nil {
		return fmt.Errorf("clipboard stderr file: %w", err)
	}
	defer os.Remove(stderr.Name())
	defer stderr.Close()
	cmd.Stderr = stderr // a FILE, not a pipe: see the file comment

	runErr := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s did not finish within %s%s", name, clipboardTimeout, toolHint(name))
	}
	if runErr == nil {
		return nil
	}
	out, _ := os.ReadFile(stderr.Name())
	msg := fmt.Sprintf("%s: %v", name, runErr)
	if s := strings.TrimSpace(string(out)); s != "" {
		msg += ": " + s
	}
	if envErr != nil {
		msg += fmt.Sprintf(" (this process has no WAYLAND_DISPLAY/DISPLAY, and the graphical session env could not be read: %v)", envErr)
	}
	return errors.New(msg)
}

// clipboardTool picks the clipboard writer for goos from what is installed.
func clipboardTool(goos string, lookPath func(string) (string, error)) (string, []string, error) {
	type tool struct {
		bin  string
		args []string
	}
	var candidates []tool
	var missing string
	switch goos {
	case "darwin":
		candidates = []tool{{"pbcopy", nil}}
		missing = "pbcopy not found"
	case "android": // termux
		candidates = []tool{{"termux-clipboard-set", nil}}
		missing = "termux-clipboard-set not found" + toolHint("termux-clipboard-set")
	case "linux":
		candidates = []tool{
			{"wl-copy", nil},
			{"xclip", []string{"-selection", "clipboard"}},
			{"xsel", []string{"--clipboard", "--input"}},
		}
		missing = "no clipboard tool found (install wl-copy, xclip, or xsel)"
	default:
		return "", nil, fmt.Errorf("clipboard not supported on %s", goos)
	}
	for _, c := range candidates {
		if _, err := lookPath(c.bin); err == nil {
			return c.bin, c.args, nil
		}
	}
	return "", nil, errors.New(missing)
}

// clipboardEnv returns the env to run the clipboard tool with: nil (inherit) when
// this process already names a display, or its env extended with the graphical
// session vars when it names none — the work-server popup case. The error is the
// session-env lookup's, kept only to explain a tool failure; it never fails the
// copy by itself, since a tool that needs no display may still succeed.
func clipboardEnv(goos string, environ []string, graphical func() (map[string]string, error)) ([]string, error) {
	if goos != "linux" {
		return nil, nil
	}
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		if (k == "WAYLAND_DISPLAY" || k == "DISPLAY") && v != "" {
			return nil, nil
		}
	}
	session, err := graphical()
	if err != nil || session == nil {
		return nil, err
	}
	env := append([]string{}, environ...)
	for k, v := range session {
		env = append(env, k+"="+v) // exec keeps the LAST value for a repeated key
	}
	return env, nil
}

func toolHint(name string) string {
	if name == "termux-clipboard-set" {
		return " (needs `pkg install termux-api` AND the Termux:API Android app)"
	}
	return ""
}
