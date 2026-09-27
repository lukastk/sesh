package agents

import (
	"strings"
	"testing"
)

// countVar returns how many entries in env set name.
func countVar(env []string, name string) int {
	n := 0
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if k == name {
			n++
		}
	}
	return n
}

func valueOf(env []string, name string) (string, bool) {
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		if k == name {
			return v, true
		}
	}
	return "", false
}

// THE ONE THAT MATTERS: $TMUX/$TMUX_PANE must not reach a headless turn.
//
// A headless turn has no pane, but it inherits the DAEMON's environment — and
// current-thread inference reads exactly those two variables to find the pane
// marker it treats as GROUND TRUTH, the most-trusted source in the model. A
// daemon started from inside a tmux pane (never in production, trivially so for
// a hand-started or test daemon) would otherwise hand every worker a live
// reference to whichever pane started the daemon, letting it resolve a
// stranger's thread through the one source that is meant to be unfakeable.
func TestTurnEnvStripsTheInheritedPaneContext(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"TMUX=/tmp/tmux-1000/sesh,1234,0",
		"TMUX_PANE=%42",
		"HOME=/home/lukastk",
	}
	env := TurnEnv(base, "thread-a", "/opt/sesh", "")
	for _, banned := range []string{"TMUX", "TMUX_PANE"} {
		if _, ok := valueOf(env, banned); ok {
			t.Errorf("$%s survived into a headless turn's environment: a pane-less worker must not be able to read a pane marker", banned)
		}
	}
	// Everything else is passed through untouched — the turn is the same
	// conversation as a pane turn and must see the same environment.
	for _, keep := range []string{"PATH", "HOME"} {
		if _, ok := valueOf(env, keep); !ok {
			t.Errorf("$%s was dropped; only the pane carriers should be", keep)
		}
	}
}

func TestTurnEnvCarriesTheIdentityAndTheBinaryExactlyOnce(t *testing.T) {
	// The base already carries stale values of both, as it does for every turn
	// after the first on a daemon that spawned panes: they must be REPLACED, not
	// appended to. Trailing duplicates win in practice, but an environment with
	// two SESH_THREAD_IDs is a thing nobody reading /proc/<pid>/environ should
	// have to reason about.
	base := []string{"SESH_THREAD_ID=stale-thread", "SESH_BIN=/old/sesh", "PATH=/usr/bin"}
	env := TurnEnv(base, "thread-a", "/opt/sesh", "")

	if n := countVar(env, EnvThreadID); n != 1 {
		t.Errorf("$%s appears %d times, want exactly 1", EnvThreadID, n)
	}
	if v, _ := valueOf(env, EnvThreadID); v != "thread-a" {
		t.Errorf("$%s = %q, want the turn's own thread", EnvThreadID, v)
	}
	if n := countVar(env, EnvSeshBin); n != 1 {
		t.Errorf("$%s appears %d times, want exactly 1", EnvSeshBin, n)
	}
	if v, _ := valueOf(env, EnvSeshBin); v != "/opt/sesh" {
		t.Errorf("$%s = %q, want the launching daemon's own executable", EnvSeshBin, v)
	}
}

// An unknown binary path is omitted rather than guessed: a wrong $SESH_BIN sends
// the worker to something that is not sesh, where PATH resolution would at least
// have had a chance.
func TestTurnEnvOmitsAnUnknownBinary(t *testing.T) {
	env := TurnEnv([]string{"SESH_BIN=/inherited/sesh"}, "thread-a", "", "")
	if _, ok := valueOf(env, EnvSeshBin); ok {
		t.Error("an empty seshBin must leave no $SESH_BIN at all, not an inherited one")
	}
}

// CODEX_HOME: set when the caller resolved one, LEFT INHERITED otherwise. That
// asymmetry is the pre-existing behavior and changing it would silently move
// codex's session store for a daemon that relies on the inherited value.
func TestTurnEnvCodexHome(t *testing.T) {
	withHome := TurnEnv([]string{"CODEX_HOME=/inherited"}, "t", "/opt/sesh", "/sandbox/codex")
	if v, _ := valueOf(withHome, "CODEX_HOME"); v != "/sandbox/codex" {
		t.Errorf("CODEX_HOME = %q, want the resolved home", v)
	}
	if n := countVar(withHome, "CODEX_HOME"); n != 1 {
		t.Errorf("CODEX_HOME appears %d times, want 1", n)
	}
	noHome := TurnEnv([]string{"CODEX_HOME=/inherited"}, "t", "/opt/sesh", "")
	if v, _ := valueOf(noHome, "CODEX_HOME"); v != "/inherited" {
		t.Errorf("CODEX_HOME = %q, want the inherited value untouched", v)
	}
}
