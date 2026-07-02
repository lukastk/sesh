package daemon

import (
	"fmt"
	"strings"
	"time"
)

// Spawn verification — turning a SILENT bad spawn into a LOUD error.
//
// When we launch an agent into a tmux pane (a fresh `thread new` or a `resume`/`headful`
// revival), the command IS the pane's process: if it exits immediately the pane closes and
// (for a lone-pane session) the session self-destructs. Historically the daemon stamped the
// marker, wrote the store, and returned 200 the instant it created the pane — WITHOUT ever
// confirming the agent stayed up. So a revive whose `claude --resume` refused the session
// ("that session is still running as a background agent") self-destructed a beat later, yet
// the caller was told it succeeded and the thread silently stayed un-enterable. That is the
// exact silent-success failure class this project forbids.
//
// A healthy agent holds its pane for the whole session; a launch that refuses to start exits
// within a second or two. So we poll the thread's marked pane briefly after spawn: if it
// vanishes (or goes dead) within the settle window the agent exited immediately and we fail
// LOUDLY (best-effort including the pane's last visible output — that is where claude prints
// WHY); if it is still alive at the end of the window the agent is up. There is no faster
// definitive signal — a still-booting agent that is ABOUT to fail on a lock also has a live
// process under the pane — so the window must simply outlast a cold start + the refusal.
const (
	spawnSettleWindow = 3 * time.Second
	spawnSettlePoll   = 200 * time.Millisecond
)

// confirmAgentLaunched polls until spawnSettleWindow that the pane bearing threadID is still
// alive. It returns a non-nil error (the agent exited immediately) or nil (the agent is up).
// It never kills anything — the caller tears down exactly what it created on error. The
// error carries the pane's last captured output when available so the operator sees the
// agent's own reason for exiting.
func (d *Daemon) confirmAgentLaunched(threadID string) error {
	return d.confirmAgentLaunchedWithin(threadID, spawnSettleWindow)
}

// confirmAgentLaunchedWithin is confirmAgentLaunched with an explicit window (tests use a
// short one; production uses spawnSettleWindow).
func (d *Daemon) confirmAgentLaunchedWithin(threadID string, window time.Duration) error {
	deadline := time.Now().Add(window)
	lastOutput := ""
	for {
		loc, found, err := d.tmux.FindPaneByThreadID(threadID)
		switch {
		case err != nil:
			// A transient tmux read error is not proof of death — keep polling and let
			// the window decide. (A truly broken pane never reappears and the maintainer
			// surfaces it; we do not want to fail a healthy spawn on one hiccup.)
		case !found:
			// The marked pane is gone: the launched command exited and its pane (and,
			// for a lone-pane session, the session) was torn down.
			msg := "agent exited immediately after launch — nothing to enter " +
				"(the underlying session may be held by another running agent, or the agent refused to start)"
			if o := strings.TrimSpace(lastOutput); o != "" {
				msg += "\n--- last agent output ---\n" + tailLines(o, 15)
			}
			return fmt.Errorf("%s", msg)
		default:
			// Still alive — remember its current output so a subsequent death reports why.
			if capd, cerr := d.tmux.CapturePane(loc.Pane); cerr == nil {
				if s := strings.TrimSpace(capd); s != "" {
					lastOutput = capd
				}
			}
		}
		if !time.Now().Before(deadline) {
			return nil // survived the settle window ⇒ the agent is up
		}
		time.Sleep(spawnSettlePoll)
	}
}

// tailLines returns the last n non-empty-trimmed lines of s (its final visible content),
// keeping error messages an agent printed just before exiting.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	trimmed := lines[:0]
	for _, ln := range lines {
		if strings.TrimSpace(ln) != "" {
			trimmed = append(trimmed, ln)
		}
	}
	if len(trimmed) > n {
		trimmed = trimmed[len(trimmed)-n:]
	}
	return strings.Join(trimmed, "\n")
}
