package tmux

import (
	"fmt"
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

// SessionAttached reports whether a client is attached to the session.
func (s *Server) SessionAttached(session string) (bool, error) {
	out, err := s.run("display-message", "-p", "-t", "="+session, "-F", "#{session_attached}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "1", nil
}
