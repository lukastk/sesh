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
// The fix: uwsm/Hyprland publish the canonical session env to the systemd user
// manager's activation environment at session start (uwsm finalize exports
// WAYLAND_DISPLAY/DISPLAY/UWSM_FINALIZE_VARNAMES — which on Hyprland includes
// HYPRLAND_INSTANCE_SIGNATURE — refreshed on every compositor start). It is
// reachable from a boot-context process: systemctl --user derives the bus from
// XDG_RUNTIME_DIR alone. So spawnEnv queries it per spawn and injects a
// whitelist into the pane via tmux -e. Per-spawn (never cached) so the next
// spawn after a compositor restart picks up the new instance signature.
//
// Graceful no-op when unavailable — non-Linux, no user manager, no session,
// systemctl missing/failing (pre-login, headless servers, termux are all
// LEGITIMATE states): inject nothing and behave as before. Errors are logged,
// not hidden. Not retroactive: existing panes keep their frozen env.

import (
	"context"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/agents"
)

// sessionEnvWhitelist is the set of graphical-session variables injected into
// spawned panes when a systemd user manager environment is available.
var sessionEnvWhitelist = map[string]bool{
	"WAYLAND_DISPLAY":             true,
	"DISPLAY":                     true,
	"XDG_RUNTIME_DIR":             true,
	"XDG_SESSION_TYPE":            true,
	"XDG_SESSION_CLASS":           true,
	"XDG_CURRENT_DESKTOP":         true,
	"XDG_SESSION_DESKTOP":         true,
	"DBUS_SESSION_BUS_ADDRESS":    true,
	"HYPRLAND_INSTANCE_SIGNATURE": true,
}

// spawnEnv is the env injected into every spawned/revived/into-pane agent: the
// graphical-session whitelist (when a session manager env is available, see
// above), the thread id (self-identification), and this daemon's own binary
// path (SESH_BIN) for in-agent state reporters — see agents.EnvSeshBin for why
// PATH resolution is not trustworthy from inside a pane.
func (d *Daemon) spawnEnv(id string) map[string]string {
	env := graphicalSessionEnv()
	if env == nil {
		env = map[string]string{}
	}
	env[agents.EnvThreadID] = id
	if exe, err := os.Executable(); err == nil {
		env[agents.EnvSeshBin] = exe
	}
	return env
}

// graphicalSessionEnv returns the whitelisted graphical-session variables from
// the systemd user manager's activation environment, or nil when unavailable.
// The query runs per spawn (rare) rather than being cached, so a compositor
// restart — which changes HYPRLAND_INSTANCE_SIGNATURE — is picked up by the
// next spawn automatically.
func graphicalSessionEnv() map[string]string {
	if runtime.GOOS != "linux" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", "--user", "show-environment")
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		// The daemon runs under a boot-context supervisor with no session env;
		// systemctl --user derives the user bus from XDG_RUNTIME_DIR alone.
		cmd.Env = append(os.Environ(), xdgRuntimeDir())
	}
	out, err := cmd.Output()
	if err != nil {
		log.Printf("spawnEnv: systemctl --user show-environment: %v (spawning without session env)", err)
		return nil
	}
	return parseSessionEnv(string(out))
}

func xdgRuntimeDir() string {
	return "XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(os.Getuid())
}

// parseSessionEnv filters `systemctl --user show-environment` output down to
// the whitelist. Lines are KEY=VALUE; values for our whitelist keys are always
// simple (paths, words, hex), so no unescaping is needed. Returns nil when
// nothing whitelisted is present.
func parseSessionEnv(out string) map[string]string {
	env := map[string]string{}
	for line := range strings.Lines(out) {
		line = strings.TrimSuffix(line, "\n")
		k, v, ok := strings.Cut(line, "=")
		if !ok || !sessionEnvWhitelist[k] || v == "" {
			continue
		}
		env[k] = v
	}
	if len(env) == 0 {
		return nil
	}
	return env
}
