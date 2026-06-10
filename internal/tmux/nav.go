package tmux

import "fmt"

// SelectWindow makes window (by name) the active window — the OUTER half of
// `tmux nav`: switching the mymastertmux client to a machine's window.
func (s *Server) SelectWindow(window string) error {
	if window == "" {
		return fmt.Errorf("tmux: select-window: empty window")
	}
	_, err := s.run("select-window", "-t", window)
	return err
}

// InnerSwitchScript returns a /bin/sh snippet that switches the mytmux client on
// socket to session — the INNER half of `tmux nav` — with the detached "bare-
// shell kick": if no client is attached to switch, attach one to the target so a
// client exists. The same snippet runs locally (sh -c) or on the far machine
// (ssh), so the fiddly logic lives in exactly one place.
//
// It targets the work server's client EXPLICITLY (`switch-client -c`): bare
// `switch-client` (no -c) is unreliable when invoked from a subprocess (it may
// resolve to the wrong client, or silently none), which left a freshly-entered
// thread not actually shown. If no client is attached, kick one.
func InnerSwitchScript(socket, session string) string {
	s := shArg(socket)
	tgt := shArg("=" + session)
	// The kick: a detached session whose pane attaches to the target session,
	// which creates a client there (a client must exist before a switch). NOTE:
	// `attach -t` does NOT honor the "=" exact-match prefix (it silently attaches
	// nothing) — switch-client does — so the attach target is the bare name.
	attach := fmt.Sprintf("env -u TMUX tmux -L %s attach -t %s", socket, session)
	kick := fmt.Sprintf("tmux -L %s new-session -d -s _seshnavkick %s", s, shArg(attach))
	return fmt.Sprintf(
		`cl=$(tmux -L %[1]s list-clients -F '#{client_name}' 2>/dev/null | head -1); `+
			`if [ -n "$cl" ]; then tmux -L %[1]s switch-client -c "$cl" -t %[2]s; else %[3]s; fi`,
		s, tgt, kick)
}

// InnerSwitchInClientScript switches THIS terminal's client to session, for
// `nav --in-client`. We're inside a tmux pane ($TMUX_PANE), so resolve the client
// attached to that pane's session and switch it EXPLICITLY — never bare
// switch-client (unreliable from a subprocess) and never the server-wide
// "first client" (which could be someone else's). No kick: in-client means a
// client already exists — us.
func InnerSwitchInClientScript(socket, session string) string {
	s := shArg(socket)
	tgt := shArg("=" + session)
	return fmt.Sprintf(
		`p="$TMUX_PANE"; sess=$(tmux -L %[1]s display-message -p -t "$p" '#{session_name}'); `+
			`cl=$(tmux -L %[1]s list-clients -t "$sess" -F '#{client_name}' 2>/dev/null | head -1); `+
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
