package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lukastk/sesh/internal/api"
)

// CurrentFromEnv resolves the locator of the calling terminal from the caller's
// $TMUX and $TMUX_PANE. This is intrinsically client-side: only the calling
// process knows which tmux client/pane it lives in. It uses the socket PATH from
// $TMUX (tmux -S), not a configured socket name, so it works no matter which
// server the caller is attached to.
//
// It errors loudly if not inside tmux rather than guessing — "which pane am I"
// has no sane default.
func CurrentFromEnv(machine string) (api.TmuxCurrentResponse, error) {
	var out api.TmuxCurrentResponse
	tmuxEnv := os.Getenv("TMUX")
	pane := os.Getenv("TMUX_PANE")
	if tmuxEnv == "" || pane == "" {
		return out, fmt.Errorf("tmux: not inside a tmux pane ($TMUX/$TMUX_PANE unset)")
	}
	// $TMUX = "<socket-path>,<server-pid>,<session-index>"
	socketPath := tmuxEnv
	if i := strings.IndexByte(tmuxEnv, ','); i >= 0 {
		socketPath = tmuxEnv[:i]
	}

	format := strings.Join([]string{
		"#{session_name}",
		"#{window_index}",
		"#{pane_id}",
		"#{" + ThreadIDOption + "}",
	}, fieldSep)

	cmd := exec.Command("tmux", "-S", socketPath, "display-message", "-p", "-t", pane, "-F", format)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return out, fmt.Errorf("tmux display-message: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	f := strings.Split(strings.TrimRight(stdout.String(), "\n"), fieldSep)
	if len(f) < 4 {
		return out, fmt.Errorf("tmux: unexpected display-message output %q", stdout.String())
	}

	out = api.TmuxCurrentResponse{
		Schema:   api.SchemaVersion,
		Machine:  machine,
		Socket:   filepath.Base(socketPath),
		Session:  f[0],
		Window:   atoi(f[1]),
		Pane:     f[2],
		ThreadID: f[3],
	}
	return out, nil
}

// ThreadIDOfPane reads the @sesh-thread-id birth-stamp of an EXPLICIT pane on an
// EXPLICIT work socket — the popup-binding variant of CurrentFromEnv (a popup's own
// $TMUX_PANE is the popup, not the pane the user was on; the binding carries the
// real pane id). Returns "" when the pane carries no thread mark (a plain shell —
// a legitimate state, not an error). A missing PANE is a loud error.
func ThreadIDOfPane(socket, pane string) (string, error) {
	return threadIDOfPane([]string{"-L", socket}, pane)
}

// ThreadIDOfPaneAtPath is ThreadIDOfPane for an explicit socket PATH (the
// caller's own $TMUX), so inference works on whatever server the caller is in.
func ThreadIDOfPaneAtPath(socketPath, pane string) (string, error) {
	return threadIDOfPane([]string{"-S", socketPath}, pane)
}

func threadIDOfPane(sockArgs []string, pane string) (string, error) {
	cmd := exec.Command("tmux", append(append([]string{}, sockArgs...), "display-message", "-p", "-t", pane, "-F", "#{"+ThreadIDOption+"}")...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux display-message -t %s: %w: %s", pane, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
