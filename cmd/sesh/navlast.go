package main

// prefix+L "last window" toggle (Lukas, 2026-06-13). The cockpit can't lean on
// tmux's native last-window/last-session: those are fragmented across three layers
// (master window = machine, work-server session, work-server window) and can't express
// a single cross-machine "the window I was just on". So sesh records the location it
// LEAVES on every master-path nav into <home>/nav-prev, and `tmux nav --last` switches
// back to it (recording the current location in turn — a clean toggle). It is anchored
// to sesh navigation (the TUI's Enter / the pickers), which is how the user moves in
// the cockpit; a purely-native prefix+n/p switch is not tracked, by design.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// navPrevPath is the file holding the previous master location ("machine\tsession\twindow").
func navPrevPath(home string) string { return filepath.Join(home, "nav-prev") }

// readNavPrev reads the recorded previous location. ok=false if absent/malformed.
func readNavPrev(home string) (machine, session string, window int, ok bool) {
	b, err := os.ReadFile(navPrevPath(home))
	if err != nil {
		return "", "", -1, false
	}
	f := strings.Split(strings.TrimRight(string(b), "\n"), "\t")
	if len(f) != 3 || f[0] == "" || f[1] == "" {
		return "", "", -1, false
	}
	w, err := strconv.Atoi(f[2])
	if err != nil {
		w = -1
	}
	return f[0], f[1], w, true
}

// writeNavPrev records the previous location (best-effort; the caller ignores errors so
// a history hiccup never blocks a nav).
func writeNavPrev(home, machine, session string, window int) {
	_ = os.WriteFile(navPrevPath(home), []byte(fmt.Sprintf("%s\t%s\t%d\n", machine, session, window)), 0o600)
}

// resolveMasterLocation resolves where the master cockpit is CURRENTLY pointing — the
// machine of the carrier client's active window, plus that machine's marker client's
// session + window (routed for a remote machine via `tmux master-current`). ok=false
// when it can't resolve (no carrier, no live marker, a plain-shell pane) — a legitimate
// "nothing to record", never an error.
func resolveMasterLocation(cfg config.Config) (machine, session string, window int, ok bool) {
	carrier := os.Getenv("SESH_NAV_CLIENT")
	if carrier == "" {
		return "", "", -1, false
	}
	out, err := exec.Command("tmux", "-L", cfg.MasterSocket, "display-message", "-p", "-c", carrier, "#{window_name}").Output()
	if err != nil {
		return "", "", -1, false
	}
	activeMachine := strings.TrimSpace(string(out))
	if activeMachine == "" {
		return "", "", -1, false
	}
	resp, ok := masterCurrentJSON(cfg, activeMachine)
	if !ok || resp.Session == "" {
		return "", "", -1, false
	}
	return activeMachine, resp.Session, resp.Window, true
}

// masterCurrentJSON runs `sesh tmux master-current --origin <self> [--machine M] --json`
// (routed to M for a remote machine via the global --machine router) and parses it.
func masterCurrentJSON(cfg config.Config, machine string) (api.MasterCurrentResponse, bool) {
	bin, err := os.Executable()
	if err != nil {
		bin = "sesh"
	}
	args := []string{"tmux", "master-current", "--origin", cfg.Machine, "--json"}
	if machine != cfg.Machine {
		args = append(args, "--machine", machine)
	}
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		return api.MasterCurrentResponse{}, false
	}
	var r api.MasterCurrentResponse
	if json.Unmarshal(out, &r) != nil {
		return api.MasterCurrentResponse{}, false
	}
	return r, true
}
