// Package sessionenv reads the graphical-session environment (WAYLAND_DISPLAY,
// DISPLAY, the D-Bus address, …) from its canonical source: the systemd user
// manager's activation environment.
//
// Processes started outside the graphical login carry none of it. The work tmux
// server is started by the daemon at boot, before any login, so every pane AND
// every popup opened on it inherits an empty session env (issue #10). uwsm/Hyprland
// publish the session env to the user manager at session start (uwsm finalize
// exports WAYLAND_DISPLAY/DISPLAY/UWSM_FINALIZE_VARNAMES — which on Hyprland
// includes HYPRLAND_INSTANCE_SIGNATURE — refreshed on every compositor start), and
// it is reachable from a boot-context process: systemctl --user derives the bus
// from XDG_RUNTIME_DIR alone.
//
// Two consumers: the daemon injects it into every spawned pane (spawnEnv), and the
// TUI hands it to the clipboard tool when the TUI itself runs without a display.
package sessionenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// whitelist is the set of graphical-session variables taken from the manager env.
var whitelist = map[string]bool{
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

// Graphical returns the whitelisted graphical-session variables from the systemd
// user manager's activation environment. nil with a nil error is a LEGITIMATE
// absence: not Linux, or a manager env holding no session (pre-login, headless
// server). A non-nil error means the query itself failed (no user manager,
// systemctl missing) — callers decide whether that is worth reporting.
//
// It queries every call rather than caching, so a compositor restart — which
// changes HYPRLAND_INSTANCE_SIGNATURE — is picked up by the next caller.
func Graphical() (map[string]string, error) {
	if runtime.GOOS != "linux" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", "--user", "show-environment")
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		// A boot-context process has no session env at all; systemctl --user
		// derives the user bus from XDG_RUNTIME_DIR alone.
		cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR=/run/user/"+strconv.Itoa(os.Getuid()))
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl --user show-environment: %w", err)
	}
	return Parse(string(out)), nil
}

// Parse filters `systemctl --user show-environment` output down to the whitelist.
// Lines are KEY=VALUE; values for whitelisted keys are always simple (paths, words,
// hex), so no unescaping is needed. Returns nil when nothing whitelisted is present.
func Parse(out string) map[string]string {
	env := map[string]string{}
	for line := range strings.Lines(out) {
		line = strings.TrimSuffix(line, "\n")
		k, v, ok := strings.Cut(line, "=")
		if !ok || !whitelist[k] || v == "" {
			continue
		}
		env[k] = v
	}
	if len(env) == 0 {
		return nil
	}
	return env
}
