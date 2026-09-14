package daemon

// Spawn environment (issue #10). The work tmux server is started by the daemon
// at boot, BEFORE any graphical login, so it holds zero graphical-session
// variables — and every pane spawned into it inherits that void (Unix env is
// immutable after exec). From such a pane, apps that need the session env
// break in ways that look random: Chromium/Electron fall back to X11
// (cold-launched `brave --app` windows come up XWayland with the generic
// `Brave-browser` class, floating, matching no windowrule; Slack/Signal abort
// with "Missing X server or $DISPLAY"), and `hyprctl` has no
// HYPRLAND_INSTANCE_SIGNATURE to talk to.
//
// The fix: inject the canonical session env, read from the systemd user manager
// (see internal/sessionenv), into the pane via tmux -e. Per-spawn (never cached)
// so the next spawn after a compositor restart picks up the new instance
// signature.
//
// Graceful no-op when unavailable — non-Linux, no user manager, no session,
// systemctl missing/failing (pre-login, headless servers, termux are all
// LEGITIMATE states): inject nothing and behave as before. Errors are logged,
// not hidden. Not retroactive: existing panes keep their frozen env.

import (
	"log"
	"os"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/sessionenv"
)

// spawnEnv is the env injected into every spawned/revived/into-pane agent: the
// graphical-session whitelist (when a session manager env is available, see
// above), the thread id (self-identification), and this daemon's own binary
// path (SESH_BIN) for in-agent state reporters — see agents.EnvSeshBin for why
// PATH resolution is not trustworthy from inside a pane.
func (d *Daemon) spawnEnv(id string) map[string]string {
	env, err := sessionenv.Graphical()
	if err != nil {
		log.Printf("spawnEnv: %v (spawning without session env)", err)
	}
	if env == nil {
		env = map[string]string{}
	}
	env[agents.EnvThreadID] = id
	if exe, err := os.Executable(); err == nil {
		env[agents.EnvSeshBin] = exe
	}
	return env
}
