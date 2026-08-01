package tmux

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"
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

var sendBufferSequence atomic.Uint64

// nextSendBufferName returns a name unique to this process and SendText call.
// tmux buffers are shared by the whole server, so a fixed name lets concurrent
// requests overwrite or delete one another between set-buffer and paste-buffer.
func nextSendBufferName() string {
	return fmt.Sprintf("sesh-send-%d-%d", os.Getpid(), sendBufferSequence.Add(1))
}

// SendText sends literal text to target's pane. If enter is true, a trailing
// Enter key is sent after (so it submits).
//
// Multi-line text is sent as a BRACKETED PASTE (set-buffer + paste-buffer -p) rather
// than literal send-keys: with `send-keys -l`, each embedded newline reaches the agent's
// TUI as a submitting Enter, so a multi-paragraph prompt is fired off line-by-line and
// its structure is lost. The `-p` flag wraps the text in the bracketed-paste escapes
// (\e[200~…\e[201~), which a modern agent TUI (claude/codex/pi) honors by buffering the
// whole thing — newlines and all — as one input; the trailing Enter then submits it
// intact. Single-line text keeps the original send-keys path (no behavior change).
func (s *Server) SendText(target, text string, enter bool) error {
	if target == "" {
		return fmt.Errorf("tmux: send-text: empty target")
	}
	if strings.Contains(text, "\n") {
		buf := nextSendBufferName()
		if _, err := s.run("set-buffer", "-b", buf, text); err != nil {
			return err
		}
		if _, err := s.run("paste-buffer", "-p", "-d", "-b", buf, "-t", target); err != nil {
			// paste-buffer -d deletes only after a successful paste. Make a best-effort
			// cleanup on the error path without hiding the original delivery failure.
			_, _ = s.run("delete-buffer", "-b", buf)
			return err
		}
	} else if _, err := s.run("send-keys", "-t", target, "-l", text); err != nil {
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
	if err != nil {
		// Killing the LAST pane on the server tears the whole server down, and tmux
		// then reports the kill-pane command itself as failed ("server exited
		// unexpectedly") or the server as already gone ("no server running") even
		// though the pane is definitively gone — which is the intended outcome of a
		// kill. Treat ONLY those server-is-gone messages as success; any other failure
		// (e.g. a bad pane id: "can't find pane") still surfaces loudly.
		msg := err.Error()
		if strings.Contains(msg, "server exited") || strings.Contains(msg, "no server running") {
			return nil
		}
		return err
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
