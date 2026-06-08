package tmux

import (
	"fmt"
	"sort"
	"strings"
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

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
