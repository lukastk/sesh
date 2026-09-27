//go:build !linux

package procs

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ParentOf reads a process's parent pid with `ps` (macOS and anything else
// without /proc). One fork per level of the walk, which is acceptable because
// the walk happens once per identity query and is a handful of levels deep —
// unlike the maintainer's per-tick probes, where forks were the whole problem.
func ParentOf(pid int) (int, error) {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, fmt.Errorf("ps -p %d: %w", pid, err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0, fmt.Errorf("ps reported no parent for pid %d (process gone)", pid)
	}
	ppid, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("unparseable ppid %q for pid %d: %w", s, pid, err)
	}
	return ppid, nil
}
