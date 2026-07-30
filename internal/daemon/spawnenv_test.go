package daemon

import "testing"

// TestParseSessionEnv proves the whitelist filtering of `systemctl --user
// show-environment` output (issue #10): only graphical-session vars survive,
// empty values and non-whitelist keys are dropped, and systemd's $'...'
// escaping on OTHER keys does not confuse the parser.
func TestParseSessionEnv(t *testing.T) {
	out := "HOME=/home/lukastk\n" +
		"PATH=/usr/bin:/bin\n" +
		"WAYLAND_DISPLAY=wayland-1\n" +
		"DISPLAY=:0\n" +
		"XDG_RUNTIME_DIR=/run/user/1000\n" +
		"XDG_SESSION_TYPE=wayland\n" +
		"XDG_SESSION_CLASS=user\n" +
		"XDG_CURRENT_DESKTOP=Hyprland\n" +
		"XDG_SESSION_DESKTOP=Hyprland\n" +
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus\n" +
		"HYPRLAND_INSTANCE_SIGNATURE=5c9377c15f85c50648f35ca5a213754f95b93ca0_1785370076_777186435\n" +
		"UWSM_FINALIZE_VARNAMES=$'HYPRLAND_INSTANCE_SIGNATURE HYPRLAND_CMD XCURSOR_SIZE'\n" +
		"LANG=C.UTF-8\n"

	env := parseSessionEnv(out)
	if env == nil {
		t.Fatal("parseSessionEnv returned nil for populated manager env")
	}
	want := map[string]string{
		"WAYLAND_DISPLAY":             "wayland-1",
		"DISPLAY":                     ":0",
		"XDG_RUNTIME_DIR":             "/run/user/1000",
		"XDG_SESSION_TYPE":            "wayland",
		"XDG_SESSION_CLASS":           "user",
		"XDG_CURRENT_DESKTOP":         "Hyprland",
		"XDG_SESSION_DESKTOP":         "Hyprland",
		"DBUS_SESSION_BUS_ADDRESS":    "unix:path=/run/user/1000/bus",
		"HYPRLAND_INSTANCE_SIGNATURE": "5c9377c15f85c50648f35ca5a213754f95b93ca0_1785370076_777186435",
	}
	if len(env) != len(want) {
		t.Fatalf("got %d vars %v, want %d", len(env), env, len(want))
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

// TestParseSessionEnvEmpty proves the graceful no-op path: a manager env with
// no session vars (headless server, pre-login) yields nil, not an empty map —
// so spawnEnv injects nothing and behaves as before.
func TestParseSessionEnvEmpty(t *testing.T) {
	for _, out := range []string{
		"",
		"\n",
		"HOME=/home/lukastk\nLANG=C.UTF-8\nPATH=/usr/bin\n",
		"WAYLAND_DISPLAY=\n", // empty value must not be injected
	} {
		if env := parseSessionEnv(out); env != nil {
			t.Errorf("parseSessionEnv(%q) = %v, want nil", out, env)
		}
	}
}
