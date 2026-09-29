package conformance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// CODEX'S MANAGED APP-SERVER DAEMON outlives every sandbox unless we stop it.
//
// Since codex-cli 0.157 an interactive `codex` TUI auto-starts a shared app-server
// daemon per CODEX_HOME (feature `daemon_auto_start`, Stable, default on): it copies
// its own package into <CODEX_HOME>/packages/app-server-daemon and runs
// `codex app-server --listen unix:// --managed-daemon` plus a
// `codex app-server daemon pid-update-loop` updater, both DETACHED (ppid 1, their own
// session). Killing the sandbox's tmux server kills the TUI, never them, and deleting
// the codex home does not stop a running binary either.
//
// Measured on mymain 2026-09-29: 127 of them leaked by earlier conformance runs,
// 3.4 GB RSS, the oldest 49 h, most with their codex home already deleted.
//
// The handle used here is deliberately NOT codex's pid files (codex-internal JSON that
// can change between versions): the managed binary lives INSIDE the codex home, so a
// process whose executable path starts with that home is exactly that home's daemon
// or one of its children (e.g. codex-code-mode-host) — nothing else can match.

// codexHomeProcs lists processes whose argv[0] starts with prefix, with their age.
type codexHomeProc struct {
	pid  int
	age  time.Duration
	argv string
}

func listCodexHomeProcs(prefix string) ([]codexHomeProc, error) {
	// etime (not etimes) is the format both procps and macOS ps support.
	out, err := exec.Command("ps", "-eo", "pid=,etime=,args=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	var procs []codexHomeProc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.HasPrefix(f[2], prefix) {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		age, err := parsePsEtime(f[1])
		if err != nil {
			return nil, fmt.Errorf("ps etime %q for pid %d: %w", f[1], pid, err)
		}
		procs = append(procs, codexHomeProc{pid: pid, age: age, argv: strings.Join(f[2:], " ")})
	}
	return procs, nil
}

// parsePsEtime parses ps's elapsed-time format, [[dd-]hh:]mm:ss.
func parsePsEtime(s string) (time.Duration, error) {
	days := 0
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, err
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("unrecognised elapsed time")
	}
	secs := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, err
		}
		secs = secs*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(secs)*time.Second, nil
}

// killCodexHomeProcs SIGTERMs every process under prefix that is at least minAge old,
// waits for them, and SIGKILLs any survivor. It returns how many it killed and an
// error naming any process that is STILL alive afterwards — a daemon we could not stop
// is a leak, and must not be reported as cleaned up.
func killCodexHomeProcs(prefix string, minAge time.Duration) (int, error) {
	procs, err := listCodexHomeProcs(prefix)
	if err != nil {
		return 0, err
	}
	var victims []codexHomeProc
	for _, p := range procs {
		if p.age >= minAge {
			victims = append(victims, p)
			syscall.Kill(p.pid, syscall.SIGTERM) //nolint:errcheck — already gone is fine; verified below
		}
	}
	alive := func(pid int) bool { return syscall.Kill(pid, 0) == nil }
	deadline := time.Now().Add(3 * time.Second)
	for _, p := range victims {
		for alive(p.pid) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if alive(p.pid) {
			syscall.Kill(p.pid, syscall.SIGKILL) //nolint:errcheck — verified below
		}
	}
	var stuck []string
	for _, p := range victims {
		for i := 0; i < 20 && alive(p.pid); i++ {
			time.Sleep(50 * time.Millisecond)
		}
		if alive(p.pid) {
			stuck = append(stuck, fmt.Sprintf("%d (%s)", p.pid, p.argv))
		}
	}
	if len(stuck) > 0 {
		return len(victims) - len(stuck), fmt.Errorf("could not stop: %s", strings.Join(stuck, ", "))
	}
	return len(victims), nil
}

// codexHomePrefix is the path prefix every sandbox codex home shares (setupCodexHome
// makes them with os.MkdirTemp("", "sesh-codex-home-")).
func codexHomePrefix() string {
	return filepath.Join(os.TempDir(), "sesh-codex-home-")
}

// reapStaleCodexDaemons kills codex managed daemons that PREVIOUS runs leaked. Like
// reapStaleTestServers it only touches ones older than staleTestServerAge, so a
// concurrent run's live daemons are safe; and it reports what it killed rather than
// sweeping silently, so a leak that keeps recurring stays visible.
func reapStaleCodexDaemons() {
	n, err := killCodexHomeProcs(codexHomePrefix(), staleTestServerAge)
	if n > 0 {
		fmt.Fprintf(os.Stderr, "conformance: reaped %d leaked codex managed-daemon process(es) (from earlier runs)\n", n)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "conformance: codex daemon reaper: %v\n", err)
	}
}
