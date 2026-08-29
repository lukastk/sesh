package tmux

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// CreateWindowCmd adds a new window running command to an EXISTING session and
// returns the new window's pane id. Used to place a thread (its own window) into
// a session that already hosts other threads — e.g. reviving a thread whose
// session a sibling keeps alive. env is injected into the new window.
func (s *Server) CreateWindowCmd(session, dir string, env map[string]string, command string) (string, error) {
	if session == "" {
		return "", fmt.Errorf("tmux: create-window-cmd: empty session")
	}
	if command == "" {
		return "", fmt.Errorf("tmux: create-window-cmd: empty command")
	}
	if !s.HasSession(session) {
		return "", fmt.Errorf("tmux: session %q does not exist", session)
	}
	args := []string{"new-window", "-t", "=" + session, "-P", "-F", "#{pane_id}"}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "-e", k+"="+env[k])
	}
	args = append(args, command)
	out, err := s.run(args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// SplitWindowCmd splits target (a pane or window) running command in the new
// pane, and returns the new pane's id. Used to place a thread as a SPLIT beside
// an existing pane (`thread new --into-window`). env is injected into the new
// pane.
func (s *Server) SplitWindowCmd(target, dir string, env map[string]string, command string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("tmux: split-window-cmd: empty target")
	}
	if command == "" {
		return "", fmt.Errorf("tmux: split-window-cmd: empty command")
	}
	args := []string{"split-window", "-t", target, "-P", "-F", "#{pane_id}"}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "-e", k+"="+env[k])
	}
	args = append(args, command)
	out, err := s.run(args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
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

// PaneIndexByThreadID returns threadID -> PaneLocator for every marked pane on the
// server, built from a SINGLE enumeration. The maintainer resolves all threads'
// panes per tick through this map rather than calling FindPaneByThreadID (one Info
// enumeration) per thread — which, at ~100 threads, made the sweep so slow that the
// content-diff busy window could never accumulate enough samples.
func (s *Server) PaneIndexByThreadID() (map[string]api.PaneLocator, error) {
	sessions, err := s.Info("")
	if err != nil {
		return nil, err
	}
	out := make(map[string]api.PaneLocator)
	for _, sess := range sessions {
		for _, win := range sess.Windows {
			for _, pane := range win.Panes {
				if pane.ThreadID != "" {
					out[pane.ThreadID] = api.PaneLocator{
						Session: sess.Name,
						Window:  win.Index,
						Pane:    pane.Pane,
						PanePID: pane.PID,
					}
				}
			}
		}
	}
	return out, nil
}

// RuntimeIndex resolves BOTH runtime indices from a SINGLE server walk: agent
// threads by their pane marker (@sesh-thread-id) and shell threads by their
// session marker (@sesh-shell-id). The maintainer needs both on every tick and
// they come out of the same enumeration, so walking twice would double the
// per-tick tmux cost for nothing.
func (s *Server) RuntimeIndex() (map[string]api.PaneLocator, map[string]api.TmuxSession, error) {
	sessions, err := s.Info("")
	if err != nil {
		return nil, nil, err
	}
	panes := make(map[string]api.PaneLocator)
	shells := make(map[string]api.TmuxSession)
	for _, sess := range sessions {
		if sess.ShellID != "" {
			shells[sess.ShellID] = sess
		}
		for _, win := range sess.Windows {
			for _, pane := range win.Panes {
				if pane.ThreadID != "" {
					panes[pane.ThreadID] = api.PaneLocator{
						Session: sess.Name,
						Window:  win.Index,
						Pane:    pane.Pane,
						PanePID: pane.PID,
					}
				}
			}
		}
	}
	return panes, shells, nil
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

// capturePanesChunk bounds how many panes one CapturePanes invocation carries:
// the whole argv travels to the tmux server as ONE imsg (MAX_IMSGSIZE 16 KiB), so
// keep each batch far below it (~40 bytes per pane).
const capturePanesChunk = 100

// CapturePanes captures MANY panes in ONE tmux invocation (`capture-pane -p -t A ;
// display-message -p <sentinel> ; capture-pane -p -t B ; ...`), returning each
// pane's text keyed by pane id. The maintainer probes every headful pane every
// ~300 ms; as separate forks that cost 37 client connects per tick on mymain —
// measured 92 % of a core in reaped tmux clients plus the daemon's own fork/exec
// overhead — where one batched invocation costs ~10 ms. The per-pane text is
// byte-identical to CapturePane's (same sub-command), so the content-diff busy
// heuristic is unchanged.
//
// A pane that vanished between enumeration and capture makes tmux abort the
// REST of the command list ("can't find pane: %N" on stderr, the earlier
// captures already on stdout). That pane is dropped (absent from the result —
// the caller's "pane vanished mid-tick" path) and the remainder is retried in a
// fresh batch. Any other failure is returned loudly, never as partial data.
func (s *Server) CapturePanes(panes []string) (map[string]string, error) {
	out := make(map[string]string, len(panes))
	remaining := append([]string(nil), panes...)
	for len(remaining) > 0 {
		chunk := remaining
		if len(chunk) > capturePanesChunk {
			chunk = chunk[:capturePanesChunk]
		}
		sentinel := "@@sesh-capture-" + randomHex(16) + "@@"
		args := make([]string, 0, len(chunk)*8)
		for i, p := range chunk {
			if i > 0 {
				args = append(args, ";")
			}
			args = append(args, "capture-pane", "-p", "-t", p, ";", "display-message", "-p", sentinel)
		}
		stdout, stderr, err := s.runSplit(args...)
		captured := splitBySentinel(stdout, sentinel)
		if len(captured) > len(chunk) {
			return nil, fmt.Errorf("tmux capture-pane batch: %d captures for %d panes (sentinel collision?)", len(captured), len(chunk))
		}
		for i, c := range captured {
			out[chunk[i]] = c
		}
		if err == nil {
			if len(captured) != len(chunk) {
				return nil, fmt.Errorf("tmux capture-pane batch: %d captures for %d panes with no error", len(captured), len(chunk))
			}
			remaining = remaining[len(chunk):]
			continue
		}
		// Aborted: the failing sub-command is the one right after the last
		// complete capture. Only a vanished pane is retryable.
		failed := cantFindPane(stderr)
		if failed == "" || len(captured) >= len(chunk) || chunk[len(captured)] != failed {
			return nil, fmt.Errorf("tmux capture-pane batch: %w: %s", err, strings.TrimSpace(stderr))
		}
		remaining = remaining[len(captured)+1:] // skip the vanished pane, retry the rest
	}
	return out, nil
}

// splitBySentinel splits a batched capture-pane output into per-pane texts.
// Each pane's text is the lines between sentinels, joined exactly as
// capture-pane -p prints them (trailing newline included); a pane with no
// lines (all blank) is the empty string.
func splitBySentinel(stdout, sentinel string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.SplitAfter(stdout, "\n") {
		if strings.TrimSuffix(line, "\n") == sentinel {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteString(line)
	}
	return out
}

var cantFindPaneRe = regexp.MustCompile(`can't find pane:?\s*(%\d+)`)

// cantFindPane extracts the pane id from tmux's "can't find pane: %N" (empty
// if the stderr is anything else).
func cantFindPane(stderr string) string {
	if m := cantFindPaneRe.FindStringSubmatch(stderr); m != nil {
		return m[1]
	}
	return ""
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b)
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

// AttachedSessions returns, for every session with at least one client attached,
// the newest client_activity (unix seconds of the last INPUT received from a
// client — output/redraws never bump it, so it distinguishes a user driving the
// session from a cockpit client merely parked on it; a switch-client does not
// bump it either, verified live). One list-clients call for the WHOLE server
// (the state maintainer's per-tick attachment probe, instead of one ClientCount
// per thread). Presence in the map == attached.
func (s *Server) AttachedSessions() (map[string]int64, error) {
	// Activity first: it is a bare integer, so the session name (which may
	// contain spaces, but never the TAB tmux passes through verbatim) is
	// everything after the first TAB.
	out, err := s.run("list-clients", "-F", "#{client_activity}\t#{client_session}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return map[string]int64{}, nil
		}
		return nil, err
	}
	set := map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		act, sess, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("tmux: list-clients line %q has no field separator", line)
		}
		n, err := strconv.ParseInt(act, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("tmux: list-clients activity %q: %w", act, err)
		}
		if n > set[sess] {
			set[sess] = n
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

// StampSessionShellID marks a tmux SESSION as a shell thread's runtime. NB the
// `=exact` target prefix is NOT honored by set-option (it errors "no such
// session: =name" — unlike list-panes/has-session/kill-session, which do honor
// it), so the plain name is passed and callers match exactly in Go.
func (s *Server) StampSessionShellID(session, threadID string) error {
	if session == "" || threadID == "" {
		return fmt.Errorf("tmux: stamp shell id: empty session or thread id")
	}
	_, err := s.run("set-option", "-t", session, ShellIDOption, threadID)
	return err
}

// UnstampSessionShellID removes the marker, returning the session to an
// untracked ghost. Used when a shell thread's record is deleted: a session left
// carrying a marker whose record is gone would classify as `stale`.
func (s *Server) UnstampSessionShellID(session string) error {
	if session == "" {
		return fmt.Errorf("tmux: unstamp shell id: empty session")
	}
	_, err := s.run("set-option", "-t", session, "-u", ShellIDOption)
	return err
}

// FindSessionByShellID returns the live session carrying the given shell-thread
// marker. found=false means the shell thread is headless (no session).
//
// It reads the marker via `list-sessions -F` rather than `show-options -v` for
// two reasons: one tmux call covers the whole server (show-options is one call
// per session), and show-options -v EXITS 1 on an unset option rather than
// returning empty, so the natural single-session read needs -q to be usable.
func (s *Server) FindSessionByShellID(threadID string) (api.TmuxSession, bool, error) {
	if threadID == "" {
		return api.TmuxSession{}, false, fmt.Errorf("tmux: empty shell thread id")
	}
	sessions, err := s.Info("")
	if err != nil {
		return api.TmuxSession{}, false, err
	}
	for _, sess := range sessions {
		if sess.ShellID == threadID {
			return sess, true, nil
		}
	}
	return api.TmuxSession{}, false, nil
}
