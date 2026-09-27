//go:build linux

package procs

import (
	"fmt"
	"os"
)

// ParentOf reads a process's parent pid straight from /proc — no fork, which
// matters because the daemon answers this on request and a `ps` per level would
// be several forks per query (the H102 lesson, one layer down).
func ParentOf(pid int) (int, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	return parseStatPPID(b)
}
