package tmux

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// HasSession reports whether a session by this name exists.
func (s *Server) HasSession(name string) bool {
	_, err := s.run("has-session", "-t", "="+name)
	return err == nil
}

// CreateSession creates a detached session named name. dir (optional) is the
// start directory; env (optional) is injected into the session environment
// (used for SESH_THREAD_ID at spawn). It is an error if the session exists —
// silently reusing one would be the kind of ambiguous behavior sesh avoids.
func (s *Server) CreateSession(name, dir string, env map[string]string) error {
	if name == "" {
		return fmt.Errorf("tmux: create-session: empty name")
	}
	if s.HasSession(name) {
		return fmt.Errorf("tmux: session %q already exists", name)
	}
	args := []string{"new-session", "-d", "-s", name}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	// Deterministic env ordering for reproducible commands.
	for _, k := range sortedKeys(env) {
		args = append(args, "-e", k+"="+env[k])
	}
	_, err := s.run(args...)
	return err
}

// CreatePane splits target and returns the new pane's id. dir (optional) is its
// start directory.
func (s *Server) CreatePane(target, dir string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("tmux: create-pane: empty target")
	}
	args := []string{"split-window", "-t", target, "-P", "-F", "#{pane_id}"}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	out, err := s.run(args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// sendEnterDelay gives a TUI a moment to register typed text before the Enter
// that submits it. Without it, some agents (codex) drop the input entirely — a
// human never types and hits Enter in the same instant either.
const sendEnterDelay = 250 * time.Millisecond

// SendText sends literal text to target's pane. If enter is true, a trailing
// Enter key is sent after (so it submits).
func (s *Server) SendText(target, text string, enter bool) error {
	if target == "" {
		return fmt.Errorf("tmux: send-text: empty target")
	}
	if _, err := s.run("send-keys", "-t", target, "-l", text); err != nil {
		return err
	}
	if enter {
		time.Sleep(sendEnterDelay)
		if _, err := s.run("send-keys", "-t", target, "Enter"); err != nil {
			return err
		}
	}
	return nil
}

// SetPaneThreadID stamps a pane with its owning thread id via the pane
// user-option.
func (s *Server) SetPaneThreadID(pane, threadID string) error {
	_, err := s.run("set-option", "-p", "-t", pane, ThreadIDOption, threadID)
	return err
}

// KillSession kills a session by name.
func (s *Server) KillSession(name string) error {
	_, err := s.run("kill-session", "-t", "="+name)
	return err
}

// KillPane kills a single pane by id. When it is the last pane of its window
// the window dies, and the last window's death takes the session — so for a
// 1-pane/1-window/1-session thread this is equivalent to KillSession, but for a
// thread sharing a session (its own window, or a split) it ends ONLY that
// thread's runtime and leaves its siblings alive.
func (s *Server) KillPane(pane string) error {
	_, err := s.run("kill-pane", "-t", pane)
	return err
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
