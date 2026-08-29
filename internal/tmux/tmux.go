// Package tmux is sesh's typed wrapper over the tmux CLI. It owns the fiddly
// addressing — (machine, socket, session, window, pane) — that the spec keeps in
// Go rather than shell. A Server is bound to one tmux socket NAME (tmux -L);
// nothing here persists runtime state, it is always re-derived live.
package tmux

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/lukastk/sesh/internal/api"
)

// ThreadIDOption is the tmux pane user-option that stamps a pane with its owning
// sesh thread id. It is invisible, survives pane moves/resizes, and is queryable
// in one list-panes pass.
const ThreadIDOption = "@sesh-thread-id"

// ShellIDOption is the tmux SESSION user-option that stamps a session with its
// owning sesh SHELL thread id — the session-level mirror of ThreadIDOption.
//
// IT MUST NOT BE THE SAME KEY AS ThreadIDOption, and that is not stylistic.
// tmux resolves user options with INHERITANCE during format expansion (pane →
// window → session → global, from the deepest object in the format's context),
// so a session-scoped @sesh-thread-id would be reported by EVERY unmarked pane
// in that session — silently corrupting FindPaneByThreadID, ThreadIDOfPane,
// `sesh tmux current`, adopt's ownership guard and nav's window resolution, each
// returning a plausible-but-wrong thread. Measured; see _dev/SHELL.md. The two
// keys are distinct precisely so both can be read without ambiguity, and
// TestMarkerScopesDoNotCollide guards it.
const ShellIDOption = "@sesh-shell-id"

// Server addresses one tmux server by socket name (tmux -L <socket>).
type Server struct {
	socket string
	conf   string // optional `tmux -f` config, sourced when THIS server starts; "" = tmux default (~/.tmux.conf)
}

// NewServer binds to a socket name (tmux's default config).
func NewServer(socket string) *Server { return &Server{socket: socket} }

// NewServerWithConf binds to a socket name with an explicit `-f` config, so sesh's
// work tmux can carry its own UI (e.g. the per-thread status bar) instead of the
// user's default ~/.tmux.conf. An empty conf means tmux's default.
func NewServerWithConf(socket, conf string) *Server { return &Server{socket: socket, conf: conf} }

// Socket returns the bound socket name.
func (s *Server) Socket() string { return s.socket }

// base returns the `[-f conf] -L socket` prefix. `-f` is sourced only when the server
// starts; tmux ignores it when connecting to a running server (verified), so it is
// safe to pass on every invocation.
func (s *Server) base() []string {
	if s.conf != "" {
		return []string{"-f", s.conf, "-L", s.socket}
	}
	return []string{"-L", s.socket}
}

// run executes `tmux [-f conf] -L <socket> <args...>` and returns stdout. stderr is
// folded into the error so failures are loud and diagnosable.
func (s *Server) run(args ...string) (string, error) {
	full := append(s.base(), args...)
	cmd := exec.Command("tmux", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", strings.Join(full, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// runSplit is run() that also hands back stdout on FAILURE: a tmux command
// LIST (`cmd ; cmd ; ...`) aborts at the first failing sub-command, and the
// caller needs everything the succeeding ones printed to decide what to retry.
func (s *Server) runSplit(args ...string) (stdout, stderr string, err error) {
	full := append(s.base(), args...)
	cmd := exec.Command("tmux", full...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

// runStdin is run() with input piped to tmux's stdin. It exists for load-buffer,
// which reads its payload from stdin (or a file) rather than as an argv. That
// distinction is load-bearing: tmux's client->server imsg protocol caps a single
// COMMAND at MAX_IMSGSIZE (16 KiB), so a large payload passed as a set-buffer argv
// fails "command too long"; the same payload streamed on stdin has no such cap.
func (s *Server) runStdin(input string, args ...string) (string, error) {
	full := append(s.base(), args...)
	cmd := exec.Command("tmux", full...)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", strings.Join(full, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// serverRunning reports whether this tmux server is up. tmux returns a specific
// error when no server is running; that is an empty state, not a failure.
func (s *Server) serverRunning() (bool, error) {
	full := append(s.base(), "has-session")
	cmd := exec.Command("tmux", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	msg := stderr.String()
	// "no server running on ..." => server down. "no sessions" / other => up but
	// empty (has-session with no target still connects to a running server).
	if strings.Contains(msg, "no server running") || strings.Contains(msg, "error connecting") || strings.Contains(msg, "No such file") {
		return false, nil
	}
	// Server is up (e.g. "can't find session"): treat as running.
	return true, nil
}

// fieldSep separates format fields. We use a tab: tmux passes it through
// verbatim, whereas it escapes true control bytes (e.g. 0x1f -> the literal
// "\037"), which would corrupt parsing. Tabs in a pane title/path are rare; a
// mismatched field count is treated as a loud error (see parsePanes), never a
// silently dropped pane.
const fieldSep = "\t"

// Info walks every session/window/pane on this server and returns them grouped.
// machine is stamped onto each session for the cross-machine model. If the
// server is not running, it returns an empty slice (a legitimate empty state).
func (s *Server) Info(machine string) ([]api.TmuxSession, error) {
	running, err := s.serverRunning()
	if err != nil {
		return nil, err
	}
	if !running {
		return nil, nil
	}

	format := strings.Join([]string{
		"#{session_name}",
		"#{session_attached}",
		"#{session_path}",
		"#{" + ShellIDOption + "}",
		"#{window_index}",
		"#{window_name}",
		"#{window_active}",
		"#{pane_id}",
		"#{pane_index}",
		"#{pane_active}",
		"#{pane_pid}",
		"#{pane_current_command}",
		"#{pane_title}",
		"#{pane_current_path}",
		"#{" + ThreadIDOption + "}",
	}, fieldSep)

	out, err := s.run("list-panes", "-a", "-F", format)
	if err != nil {
		// A running server with zero sessions makes list-panes fail; treat as empty.
		if strings.Contains(err.Error(), "no server running") {
			return nil, nil
		}
		return nil, err
	}
	return parsePanes(machine, s.socket, out)
}

const paneFieldCount = 15

func parsePanes(machine, socket, out string) ([]api.TmuxSession, error) {
	// Preserve first-seen order of sessions and windows.
	type winKey struct {
		session string
		index   int
	}
	sessOrder := []string{}
	sessions := map[string]*api.TmuxSession{}
	winOrder := map[string][]int{}
	windows := map[winKey]*api.TmuxWindow{}

	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, fieldSep)
		if len(f) != paneFieldCount {
			// Loud, not silent: a tab inside a title/path (or a tmux format
			// change) corrupted this record. Dropping it would hide a pane.
			return nil, fmt.Errorf("tmux: list-panes line has %d fields, want %d: %q", len(f), paneFieldCount, line)
		}
		sname := f[0]
		sattached := f[1] == "1"
		spath := f[2]
		shellID := f[3]
		windex := atoi(f[4])
		wname := f[5]
		wactive := f[6] == "1"
		pane := api.TmuxPane{
			Pane:     f[7],
			Index:    atoi(f[8]),
			Active:   f[9] == "1",
			PID:      atoi(f[10]),
			Command:  f[11],
			Title:    f[12],
			Path:     f[13],
			ThreadID: f[14],
		}

		sess, ok := sessions[sname]
		if !ok {
			sess = &api.TmuxSession{Machine: machine, Socket: socket, Name: sname, Attached: sattached, Path: spath, ShellID: shellID}
			sessions[sname] = sess
			sessOrder = append(sessOrder, sname)
		}
		wk := winKey{sname, windex}
		win, ok := windows[wk]
		if !ok {
			win = &api.TmuxWindow{Index: windex, Name: wname, Active: wactive}
			windows[wk] = win
			winOrder[sname] = append(winOrder[sname], windex)
		}
		win.Panes = append(win.Panes, pane)
	}

	var result []api.TmuxSession
	for _, sname := range sessOrder {
		sess := sessions[sname]
		for _, widx := range winOrder[sname] {
			sess.Windows = append(sess.Windows, *windows[winKey{sname, widx}])
		}
		result = append(result, *sess)
	}
	return result, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
