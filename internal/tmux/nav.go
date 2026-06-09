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
func InnerSwitchScript(socket, session string) string {
	sw := fmt.Sprintf("tmux -L %s switch-client -t %s", shArg(socket), shArg("="+session))
	// The kick: a detached session whose pane attaches to the target session,
	// which creates a client there (a client must exist before a switch). NOTE:
	// `attach -t` does NOT honor the "=" exact-match prefix (it silently attaches
	// nothing) — switch-client does — so the attach target is the bare name.
	attach := fmt.Sprintf("env -u TMUX tmux -L %s attach -t %s", socket, session)
	kick := fmt.Sprintf("tmux -L %s new-session -d -s _seshnavkick %s", shArg(socket), shArg(attach))
	return sw + " 2>/dev/null || " + kick
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
