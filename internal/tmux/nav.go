package tmux

import (
	"fmt"
	"path/filepath"
)

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
func InnerSwitchScript(socket, session, marker string) string {
	s := shArg(socket)
	tgt := shArg("=" + session)
	mk := shArg(marker)
	// The kick: a detached session whose pane attaches to the target session,
	// which creates a client there (a client must exist before a switch). NOTE:
	// `attach -t` does NOT honor the "=" exact-match prefix (it silently attaches
	// nothing) — switch-client does — so the attach target is the bare name.
	attach := fmt.Sprintf("env -u TMUX tmux -L %s attach -t %s", socket, session)
	kick := fmt.Sprintf("tmux -L %s new-session -d -s _seshnavkick %s", s, shArg(attach))
	return fmt.Sprintf(`sel=""; `+
		`mk=$(cat %[4]s 2>/dev/null); `+
		`if [ -n "$mk" ] && tmux -L %[1]s list-clients -F '#{client_name} #{client_pid}' 2>/dev/null | grep -Fxq "$mk"; then sel="${mk%% *}"; fi; `+
		`if [ -z "$sel" ]; then `+
		`cls=$(tmux -L %[1]s list-clients -F '#{client_name}' 2>/dev/null); `+
		`n=$(printf '%%s' "$cls" | grep -c .); `+
		`if [ "$n" -eq 1 ]; then sel="$cls"; `+
		`elif [ "$n" -eq 0 ]; then %[3]s; exit 0; `+
		`else echo "nav: $n clients on work socket %[2]s and no live master marker (%[5]s) — restart the master so its attach records itself" >&2; exit 1; fi; `+
		`fi; `+
		`exec tmux -L %[1]s switch-client -c "$sel" -t %[6]s`,
		s, socket, kick, mk, marker, tgt)
}

// InnerSwitchInClientScript switches THIS terminal's client to session, for
// `nav --in-client`. The crux: when several clients are attached to the SAME session
// (e.g. a master window AND a direct attach), we must switch the one whose keystroke
// we're handling — the user who pressed Enter — not just any client. tmux's
// `display-message -p '#{client_name}'` resolves exactly that "current client" (the
// one whose input is being processed), so switch IT explicitly. Falls back to bare
// switch-client if no current client can be resolved.
func InnerSwitchInClientScript(socket, session string) string {
	s := shArg(socket)
	tgt := shArg("=" + session)
	return fmt.Sprintf(
		`cl=$(tmux -L %[1]s display-message -p '#{client_name}' 2>/dev/null); `+
			`if [ -n "$cl" ]; then tmux -L %[1]s switch-client -c "$cl" -t %[2]s; `+
			`else tmux -L %[1]s switch-client -t %[2]s; fi`,
		s, tgt)
}

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
