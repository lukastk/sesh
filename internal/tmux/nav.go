package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MarkerClientCurrent resolves what the marker's recorded client is CURRENTLY
// viewing — i.e. what the master window at markerPath is showing: its active-pane
// @sesh-thread-id (the thread, "" for a plain-shell pane) AND its session name. Both
// are "" (NOT an error) when there's nothing to resolve: the marker is absent or
// malformed, or the recorded client is gone (stale tty). It is LOUD only on a real
// tmux failure. The client is verified by name AND pid (a recycled tty must not be
// mistaken for it) — the same liveness contract as nav's inner switch.
func (s *Server) MarkerClientCurrent(markerPath string) (session, threadID string, window int, err error) {
	b, err := os.ReadFile(markerPath)
	if err != nil {
		return "", "", -1, nil // no master client here → nothing to resolve
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return "", "", -1, nil
	}
	name, pid := f[0], f[1]
	// ONE list-clients pass answers everything, and it MUST be list-clients.
	//
	// `display-message -p -c <client>` does NOT scope its format to that client: -c
	// says where to PRINT, not what to expand against, so the format resolves against
	// whatever tmux ambiently considers current. MEASURED on two real clients sitting
	// on two different sessions, each pane carrying its own @sesh-thread-id:
	//
	//     -c /dev/pts/43 => THREAD-B | B      <- WRONG, that is the other client
	//     -c /dev/pts/67 => THREAD-B | B
	//
	// list-clients iterates CLIENTS and expands the format once per client, so the
	// pane-scoped @sesh-thread-id, the session and the window index all resolve
	// against THAT client's own active pane:
	//
	//     /dev/pts/43  tid=THREAD-A sess=A win=0
	//     /dev/pts/67  tid=THREAD-B sess=B win=1
	//
	// This was invisible for as long as a work server had ONE master attached, because
	// then the ambient pick happened to be the right client. It goes wrong exactly when
	// several masters watch one machine — which is the normal state of a busy box — and
	// it made `master-current` (the sidebar's cursor, the prefix+, / prefix+. start
	// point, prefix+L) report another master's thread. The same call also does the
	// liveness check, so a stale marker still resolves to nothing: the recorded client
	// is matched on name AND pid (a recycled tty must not be mistaken for it).
	out, err := s.run("list-clients", "-F",
		"#{client_name}"+fieldSep+"#{client_pid}"+fieldSep+"#{"+ThreadIDOption+"}"+fieldSep+"#{client_session}"+fieldSep+"#{window_index}")
	if err != nil {
		return "", "", -1, err
	}
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(strings.TrimRight(ln, "\r"), fieldSep)
		if len(fields) < 5 {
			continue
		}
		if strings.TrimSpace(fields[0]) != name || strings.TrimSpace(fields[1]) != pid {
			continue
		}
		return strings.TrimSpace(fields[3]), strings.TrimSpace(fields[2]), atoi(strings.TrimSpace(fields[4])), nil
	}
	return "", "", -1, nil // stale marker (the recorded client is gone)
}

// MarkerClientThreadID is the thread-only view of MarkerClientCurrent (the TUI
// master-cursor preselect; prefix+a uses the session via the full resolver).
func (s *Server) MarkerClientThreadID(markerPath string) (string, error) {
	_, tid, _, err := s.MarkerClientCurrent(markerPath)
	return tid, err
}

// SelectWindow makes window (by name) the active window — the OUTER half of
// `tmux nav`: switching the mymastertmux client to a machine's window.
func (s *Server) SelectWindow(window string) error {
	if window == "" {
		return fmt.Errorf("tmux: select-window: empty window")
	}
	_, err := s.run("select-window", "-t", window)
	return err
}

// MasterClientMarker is the path of the file in which a master window's attach
// records its tmux client identity ("<client_name> <client_pid>") on the work
// machine — written by the attach script `sesh master window` runs (see
// cmd/sesh/master.go workAttach), read by InnerSwitchScript so nav switches
// EXACTLY the master-window client. One file per ORIGIN master machine, so two
// masters (e.g. mymain's and macbook's) watching the same work server never
// clobber each other's marker.
func MasterClientMarker(home, origin string) string {
	return filepath.Join(home, "master-client."+origin)
}

// InnerSwitchScript returns a /bin/sh snippet that switches the work server's
// client on socket to session — the INNER half of `tmux nav` — with the
// detached "bare-shell kick": if no client is attached at all, attach one to
// the target so a client exists. The same snippet runs locally (sh -c) or on
// the far machine (ssh), so the fiddly logic lives in exactly one place.
//
// WHICH client (the crux): the work server can have several — the calling
// master's window supervisor, OTHER masters' supervisors, and the user's
// direct attaches. The master path must move what the calling master's window
// shows and nothing else, so it switches the client recorded in the marker
// file (written by that window's own attach script; verified live against
// `list-clients` by name AND pid, so a recycled tty can't be mistaken for it).
// With no live marker: a single client is unambiguous — switch it; multiple
// clients are NOT — fail loudly (an old attach predating the marker needs a
// master restart) rather than moving someone else's view, which is exactly
// the bug the old `list-clients | head -1` resolution had.
func InnerSwitchScript(socket, session, threadID, windowIndex, marker string) string {
	s := shArg(socket)
	tgt := shArg("=" + session)
	mk := shArg(marker)
	// The kick: a detached session whose pane attaches to the target session,
	// which creates a client there (a client must exist before a switch). NOTE:
	// `attach -t` does NOT honor the "=" exact-match prefix (it silently attaches
	// nothing) — switch-client does — so the attach target is the bare name.
	attach := fmt.Sprintf("env -u TMUX tmux -L %s attach -t %s", socket, session)
	kick := fmt.Sprintf("tmux -L %s new-session -d -s _seshnavkick %s", s, shArg(attach))
	// Window targeting: a thread's session can hold several windows (the user opens
	// more), and switch-client to a bare session lands on the session's LAST-ACTIVE
	// window. An EXPLICIT windowIndex (prefix+L replaying a recorded location) wins;
	// otherwise, with a thread id, resolve the window of the @sesh-thread-id-marked pane
	// and switch to `=session:window`. Unresolvable falls back to the bare session. `w`
	// is reused by the no-client kick branch so a fresh attach lands on the right window.
	resolveWin := `w=''`
	switch {
	case windowIndex != "":
		resolveWin = fmt.Sprintf(`w=%s; tgt="=%s:$w"`, shArg(windowIndex), session)
	case threadID != "":
		filter := shArg(fmt.Sprintf("#{==:#{%s},%s}", ThreadIDOption, threadID))
		resolveWin = fmt.Sprintf(`w=$(tmux -L %[1]s list-panes -s -t %[2]s -f %[3]s -F '#{window_index}' 2>/dev/null | head -n1); [ -n "$w" ] && tgt="=%[4]s:$w"`,
			s, tgt, filter, session)
	}
	return fmt.Sprintf(`tmux -L %[1]s has-session -t %[6]s 2>/dev/null || { echo "nav: no session %[7]s on work socket %[2]s (it may have just died — re-check and revive it)" >&2; exit 1; }; `+
		`tgt=%[6]s; %[8]s; `+
		`sel=""; `+
		`mk=$(cat %[4]s 2>/dev/null); `+
		`if [ -n "$mk" ] && tmux -L %[1]s list-clients -F '#{client_name} #{client_pid}' 2>/dev/null | grep -Fxq "$mk"; then sel="${mk%% *}"; fi; `+
		`if [ -z "$sel" ]; then `+
		`cls=$(tmux -L %[1]s list-clients -F '#{client_name}' 2>/dev/null); `+
		`n=$(printf '%%s' "$cls" | grep -c .); `+
		`if [ "$n" -eq 1 ]; then sel="$cls"; `+
		`elif [ "$n" -eq 0 ]; then [ -n "$w" ] && tmux -L %[1]s select-window -t "=%[7]s:$w" 2>/dev/null; %[3]s; exit 0; `+
		`else echo "nav: $n clients on work socket %[2]s and no live master marker (%[5]s) — restart the master so its attach records itself" >&2; exit 1; fi; `+
		`fi; `+
		`exec tmux -L %[1]s switch-client -c "$sel" -t "$tgt"`,
		s, socket, kick, mk, marker, tgt, session, resolveWin)
}

// NOTE: the --in-client switch is implemented directly in cmd/sesh (tmuxNav), not as
// a script: WHICH client to switch must be resolved with a tty attached (tmux
// identifies a command client by its tty; a piped subprocess gets an ambient,
// arbitrary pick) or passed explicitly via --client. See tmuxNav.

// shArg single-quotes a string for a POSIX shell.
func shArg(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += `'\''`
		} else {
			out += string(r)
		}
	}
	return out + "'"
}

// NavBellPath is the file `sesh tmux nav` touches after every successful nav, on the
// machine the nav was ISSUED from — which for the cockpit is the master host, the same
// host a persistent sidebar (`sesh tui --sidebar`) runs on, so the two share it through
// their common SESH_HOME.
//
// It is a BELL, not the answer: it says only "the cockpit just moved", and carries no
// claim about WHERE, because a nav may have targeted a different origin master or a
// window this sidebar is not showing. The authoritative read stays MarkerClientCurrent.
// The point is latency — a sidebar that only polled would lag its own keybindings by
// its poll interval, which is exactly the confusion the tracking exists to remove.
func NavBellPath(home string) string { return filepath.Join(home, "nav-bell") }

// RingNavBell records that a nav just happened, for any watching sidebar. BEST-EFFORT
// BY CONTRACT — the error is deliberately dropped: a nav that actually worked must
// never be reported as failed because a cosmetic bell could not be written, and a
// sidebar that misses one still catches up on its backstop poll.
func RingNavBell(home string) {
	_ = os.WriteFile(NavBellPath(home), []byte(strconv.FormatInt(time.Now().UnixNano(), 10)+"\n"), 0o644)
}
