package codexfs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// uuidRE matches a trailing canonical uuid (8-4-4-4-12).
var uuidRE = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// UUIDFromRolloutName extracts the uuid from a rollout filename
// `rollout-<ts>-<uuid>.jsonl` (the ts also has hyphens, so anchor on the
// trailing canonical-uuid shape).
func UUIDFromRolloutName(name string) string {
	return uuidRE.FindString(strings.TrimSuffix(filepath.Base(name), ".jsonl"))
}

// ResolveUUIDByPID returns the uuid of the codex session whose rollout file the
// process holds open. A running codex keeps its rollout-<ts>-<uuid>.jsonl open,
// so this is authoritative and per-PID (unambiguous even when several codex
// share a cwd — exp15 §3). found=false when the process has NO rollout open —
// notably a codex that hasn't taken its first turn yet (codex writes the
// rollout only on the first turn), which the caller surfaces as "unresolvable".
func ResolveUUIDByPID(pid int) (uuid string, found bool, err error) {
	files, err := openFiles(pid)
	if err != nil {
		return "", false, err
	}
	return uuidFromOpenFiles(files)
}

// uuidFromOpenFiles picks the single rollout-*.jsonl among a process's open
// file paths and returns its uuid. Factored out for unit-testing.
func uuidFromOpenFiles(files []string) (string, bool, error) {
	for _, f := range files {
		b := filepath.Base(f)
		if strings.HasPrefix(b, "rollout-") && strings.HasSuffix(b, ".jsonl") {
			if u := UUIDFromRolloutName(b); u != "" {
				return u, true, nil
			}
		}
	}
	return "", false, nil
}

// openFiles lists the regular-file paths a process holds open: `lsof` on
// darwin, `/proc/<pid>/fd` symlinks on linux/android(termux). Rollout files are
// regular files, so both resolve them. (Unix-socket paths do NOT resolve via
// /proc/fd — they read as `socket:[inode]` — but codex has no socket, so that
// doesn't matter here.)
func openFiles(pid int) ([]string, error) {
	if runtime.GOOS == "darwin" {
		return lsofPaths(pid)
	}
	return procFDPaths(pid)
}

func lsofPaths(pid int) ([]string, error) {
	// -F n: machine-readable, name fields prefixed with 'n'. lsof exits non-zero
	// when a pid has no matching files; tolerate that and parse what we got.
	out, _ := exec.Command("lsof", "-p", strconv.Itoa(pid), "-F", "n").Output()
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			paths = append(paths, line[1:])
		}
	}
	return paths, nil
}

func procFDPaths(pid int) ([]string, error) {
	dir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(target, "/") { // skip socket:[...] / pipe:[...] / anon_inode:
			paths = append(paths, target)
		}
	}
	return paths, nil
}
