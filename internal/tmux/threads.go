package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lukastk/sesh/internal/api"
)

// CreateSessionCmd creates a detached session named name that runs command
// (a shell command line) as its initial process — used to launch an agent. dir
// (optional) is the start directory; env is injected into the session.
func (s *Server) CreateSessionCmd(name, dir string, env map[string]string, command string) error {
	if name == "" {
		return fmt.Errorf("tmux: create-session-cmd: empty name")
	}
	if command == "" {
		return fmt.Errorf("tmux: create-session-cmd: empty command")
	}
	if s.HasSession(name) {
		return fmt.Errorf("tmux: session %q already exists", name)
	}
	args := []string{"new-session", "-d", "-s", name}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "-e", k+"="+env[k])
	}
	// The command is the trailing shell-command argument to new-session.
	args = append(args, command)
	_, err := s.run(args...)
	return err
}

// FindPaneByThreadID walks the server and returns the live pane bearing the
// given @sesh-thread-id marker. found=false means no such pane (the thread is
// dead). This is thread.resolve-pane: pane resolution from the marker, never a
// stored pane id.
func (s *Server) FindPaneByThreadID(threadID string) (api.PaneLocator, bool, error) {
	if threadID == "" {
		return api.PaneLocator{}, false, fmt.Errorf("tmux: empty thread id")
	}
	sessions, err := s.Info("")
	if err != nil {
		return api.PaneLocator{}, false, err
	}
	for _, sess := range sessions {
		for _, win := range sess.Windows {
			for _, pane := range win.Panes {
				if pane.ThreadID == threadID {
					return api.PaneLocator{
						Session: sess.Name,
						Window:  win.Index,
						Pane:    pane.Pane,
						PanePID: pane.PID,
					}, true, nil
				}
			}
		}
	}
	return api.PaneLocator{}, false, nil
}

// SessionFirstPane returns the pane id of a session's first pane (a freshly
// created session has exactly one).
func (s *Server) SessionFirstPane(session string) (string, error) {
	out, err := s.run("list-panes", "-t", "="+session, "-F", "#{pane_id}")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return "", fmt.Errorf("tmux: session %q has no panes", session)
	}
	return fields[0], nil
}

// CapturePane returns the visible text of a pane (capture-pane -p). Used by the
// content-diff activity probe: a pane whose bytes change over a short window is
// mid-turn (working); a byte-stable pane is idle (waiting).
func (s *Server) CapturePane(pane string) (string, error) {
	return s.run("capture-pane", "-t", pane, "-p")
}

// CapturePaneLines returns the text of a pane. lines==0 captures only the visible
// area; lines>0 captures the last N lines including scrollback (capture-pane -S -N).
// This backs `sesh thread capture` (peek at a thread's live pane).
func (s *Server) CapturePaneLines(pane string, lines int) (string, error) {
	if lines > 0 {
		return s.run("capture-pane", "-t", pane, "-p", "-S", fmt.Sprintf("-%d", lines))
	}
	return s.run("capture-pane", "-t", pane, "-p")
}

// ClientCount returns how many tmux clients are attached to a session — the
// attached/detached signal. Uses list-clients (the canonical source) rather than
// session_attached.
func (s *Server) ClientCount(session string) (int, error) {
	out, err := s.run("list-clients", "-t", "="+session, "-F", "#{client_name}")
	if err != nil {
		// list-clients errors if the session is gone; that is zero clients.
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "can't find") {
			return 0, nil
		}
		return 0, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return 0, nil
	}
	return len(strings.Split(out, "\n")), nil
}

// AttachedSessions returns the set of session names that have at least one client
// attached — one list-clients call for the WHOLE server (the state maintainer's
// per-tick attachment probe, instead of one ClientCount per thread).
func (s *Server) AttachedSessions() (map[string]bool, error) {
	out, err := s.run("list-clients", "-F", "#{client_session}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	set := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			set[line] = true
		}
	}
	return set, nil
}

// SessionAttached reports whether a client is attached to the session. It uses
// list-sessions and matches the name exactly in Go: display-message -t with the
// "=" exact-match prefix silently returns empty (the prefix is not honored
// there), which would mask a real attachment as detached.
func (s *Server) SessionAttached(session string) (bool, error) {
	out, err := s.run("list-sessions", "-F", "#{session_name}\t#{session_attached}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") {
			return false, nil
		}
		return false, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, attached, ok := strings.Cut(line, "\t")
		if ok && name == session {
			return strings.TrimSpace(attached) == "1", nil
		}
	}
	return false, nil // session not found => nothing attached
}

// PaneInfo describes one pane for adoption: its session, root process, cwd,
// and any existing thread mark.
type PaneInfo struct {
	Session  string
	Pane     string
	PanePID  int
	Cwd      string
	ThreadID string // existing @sesh-thread-id ("" = unmanaged)
}

// FindPaneByID locates a pane on this server by its %id.
func (s *Server) FindPaneByID(pane string) (PaneInfo, bool, error) {
	out, err := s.run("list-panes", "-a", "-F",
		"#{session_name}"+fieldSep+"#{pane_id}"+fieldSep+"#{pane_pid}"+fieldSep+"#{pane_current_path}"+fieldSep+"#{"+ThreadIDOption+"}")
	if err != nil {
		return PaneInfo{}, false, err
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, fieldSep)
		if len(f) != 5 {
			return PaneInfo{}, false, fmt.Errorf("tmux: unexpected list-panes fields %q", line)
		}
		if f[1] != pane {
			continue
		}
		pid, err := strconv.Atoi(f[2])
		if err != nil {
			return PaneInfo{}, false, fmt.Errorf("tmux: bad pane pid %q", f[2])
		}
		return PaneInfo{Session: f[0], Pane: f[1], PanePID: pid, Cwd: f[3], ThreadID: f[4]}, true, nil
	}
	return PaneInfo{}, false, nil
}

// StampPaneThreadID birth-stamps a pane as a thread's (adoption — spawned
// panes are stamped at creation).
func (s *Server) StampPaneThreadID(pane, threadID string) error {
	_, err := s.run("set-option", "-p", "-t", pane, ThreadIDOption, threadID)
	return err
}

// SocketPath returns this server's socket path (pi's RPC pane matching needs
// the caller's socket in path form).
func (s *Server) SocketPath() string {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), s.socket)
}
