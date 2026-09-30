package daemon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/lukastk/sesh/internal/agents"
)

// ensureCodexNoDaemon pins `[features] daemon_auto_start = false` in codexHome
// (agents.EnsureCodexNoDaemon, sesh#15) for one trigger. A user's explicit `true`
// is overridden — sesh cannot work with codex's shared app-server — but LOUDLY:
// logged, and remembered for doctor.
func (d *Daemon) ensureCodexNoDaemon(codexHome, trigger string) error {
	prev, err := agents.EnsureCodexNoDaemon(codexHome)
	if err != nil {
		note := fmt.Sprintf("%s: could not pin daemon_auto_start=false in %s: %v", trigger, codexHome, err)
		log.Printf("codex: %s", note)
		d.codexDaemonNote.Store(note)
		return err
	}
	if prev == "true" {
		note := fmt.Sprintf("%s: OVERRODE the user's [features] daemon_auto_start = true in %s/config.toml — sesh cannot work with codex's shared app-server daemon (it holds thread writer locks after a pane is killed; sesh#15)", trigger, codexHome)
		log.Printf("codex: %s", note)
		d.codexDaemonNote.Store(note)
	}
	return nil
}

// startupCodexNoDaemon runs the pin for this machine's codex home at daemon start,
// but only where codex has been used (the home exists): creating ~/.codex on a
// machine with no codex (termux) would be litter. Errors are logged, not fatal.
func (d *Daemon) startupCodexNoDaemon() {
	home, err := agents.CodexHome(d.cfg.CodexHome)
	if err != nil {
		log.Printf("codex: startup: resolve codex home: %v", err)
		d.codexDaemonNote.Store("startup: resolve codex home: " + err.Error())
		return
	}
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		return
	}
	d.ensureCodexNoDaemon(home, "startup") //nolint:errcheck — logged + surfaced by doctor
}

// codexManagedDaemons lists running `codex app-server … --managed-daemon` processes
// (and their updater) whose EXECUTABLE lives inside codexHome — codex copies the
// managed binary into <home>/packages/app-server-daemon, so an argv[0] under the
// home is exactly that home's daemon and can match nothing else. Read-only.
func codexManagedDaemons(codexHome string) ([]string, error) {
	prefix := strings.TrimRight(codexHome, "/") + "/packages/app-server-daemon/"
	return procsWithExePrefix(prefix)
}

// procsWithExePrefix returns "pid argv…" for every process whose argv[0] starts
// with prefix. It only READS the process table (ps works on Linux, macOS and
// termux); it never signals anything.
func procsWithExePrefix(prefix string) ([]string, error) {
	out, err := exec.Command("ps", "-eo", "pid=,args=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return matchExePrefix(string(out), prefix), nil
}

// matchExePrefix is the pure matcher over `ps -eo pid=,args=` output, kept
// separate so its SCOPE is tested without touching any process.
func matchExePrefix(psOut, prefix string) []string {
	var hits []string
	for _, line := range strings.Split(psOut, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.HasPrefix(f[1], prefix) {
			hits = append(hits, strings.Join(f, " "))
		}
	}
	return hits
}

// doctorCodexDaemon adds the codex-daemon checks. Only for a machine where codex
// has been used (the home exists), like the startup pin.
func (d *Daemon) doctorCodexDaemon(add func(name, status, detail string)) {
	home, err := agents.CodexHome(d.cfg.CodexHome)
	if err != nil {
		add("codex daemon", "warn", "cannot resolve the codex home: "+err.Error())
		return
	}
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		return // codex never used here
	}
	setting, err := agents.CodexDaemonSetting(home)
	switch {
	case err != nil:
		add("codex daemon", "fail", err.Error())
	case setting == "false":
		add("codex daemon", "ok", "[features] daemon_auto_start = false in "+home+"/config.toml")
	default:
		add("codex daemon", "fail", "[features] daemon_auto_start is "+setting+" in "+home+"/config.toml — sesh pins it false at startup and every codex launch; a later edit re-enabled it")
	}
	if note, _ := d.codexDaemonNote.Load().(string); note != "" {
		add("codex daemon pin", "warn", note)
	}
	running, err := codexManagedDaemons(home)
	switch {
	case err != nil:
		add("codex app-server", "warn", "cannot list processes: "+err.Error())
	case len(running) > 0:
		// `daemon stop` stops the app-server (the writer-lock holder) but NOT its
		// auto-updater (`… daemon pid-update-loop`, measured on 0.159.0), whose pid
		// codex records in <home>/app-server-daemon/daemon-updater.pid.
		add("codex app-server", "fail", fmt.Sprintf(
			"%d process(es) of codex's shared app-server running from %s/packages/app-server-daemon — started before the pin, the app-server keeps holding thread writer locks (stop/resume/adopt break). "+
				"Once no codex pane is mid-turn: `CODEX_HOME=%s codex app-server daemon stop`, then kill the leftover updater (`… daemon pid-update-loop`, pid in %s/app-server-daemon/daemon-updater.pid). Running: %s",
			len(running), home, home, home, strings.Join(running, " | ")))
	default:
		add("codex app-server", "ok", "none running for "+home)
	}
}

func firstFields(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if f := strings.Fields(l); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}
