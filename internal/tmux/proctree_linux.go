//go:build linux

package tmux

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// snapshotProcsUnder builds a process index of ONLY the subtrees under roots,
// straight from /proc — no `ps` fork. The maintainer resolves the agent under
// each marked pane every ~300 ms; `ps -e` over a busy box's ~1,100 processes
// cost 70 ms of CPU per tick on mymain (~23 % of a core) to answer a question
// about a few dozen subtrees that /proc answers in a few small reads:
// /proc/<pid>/task/*/children lists direct children (CONFIG_PROC_CHILDREN),
// /proc/<pid>/cmdline the argv. Every task must be consulted: Linux records a
// child's creating THREAD there, and a multi-threaded pane process can create
// its agent child from a non-leader task. Walk depth is bounded like
// findAgent's. An unreadable children file for a ROOT (a kernel without the
// feature, a pane pid that already exited) is returned as an error so the
// caller falls back to the full `ps` snapshot — never a silently empty index
// that would read a live agent as gone.
func snapshotProcsUnder(roots []int, maxDepth int) (*procIndex, error) {
	idx := &procIndex{byPID: map[int]proc{}, children: map[int][]int{}}
	type item struct{ pid, depth int }
	for _, root := range roots {
		cmd, ok := readCmdline(root)
		if !ok {
			return nil, fmt.Errorf("proc walk: pane pid %d unreadable", root)
		}
		idx.byPID[root] = proc{pid: root, ppid: 0, command: cmd}
		queue := []item{{root, 0}}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if cur.depth >= maxDepth {
				continue
			}
			kids, err := readChildren(cur.pid)
			if err != nil {
				if cur.pid == root {
					return nil, fmt.Errorf("proc walk: %w", err)
				}
				continue // a child that exited mid-walk: no subtree
			}
			for _, k := range kids {
				cmd, ok := readCmdline(k)
				if !ok {
					continue
				}
				idx.byPID[k] = proc{pid: k, ppid: cur.pid, command: cmd}
				idx.children[cur.pid] = append(idx.children[cur.pid], k)
				queue = append(queue, item{k, cur.depth + 1})
			}
		}
	}
	return idx, nil
}

func readChildren(pid int) ([]int, error) {
	taskDir := fmt.Sprintf("/proc/%d/task", pid)
	tasks, err := os.ReadDir(taskDir)
	if err != nil {
		return nil, err
	}
	seen := make(map[int]struct{})
	readable := false
	for _, task := range tasks {
		if _, err := strconv.Atoi(task.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(taskDir + "/" + task.Name() + "/children")
		if err != nil {
			continue // task exited while its process stayed live
		}
		readable = true
		for _, f := range strings.Fields(string(b)) {
			if n, err := strconv.Atoi(f); err == nil {
				seen[n] = struct{}{}
			}
		}
	}
	if !readable {
		return nil, fmt.Errorf("no readable task children files under %s", taskDir)
	}
	out := make([]int, 0, len(seen))
	for pid := range seen {
		out = append(out, pid)
	}
	sort.Ints(out)
	return out, nil
}

// readCmdline returns the process's argv joined by spaces — the same shape
// `ps -o args=` prints, which agentRe is written against. false when the
// process is gone or has no argv (a kernel thread / zombie: never an agent).
func readCmdline(pid int) (string, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(b) == 0 {
		return "", false
	}
	return strings.TrimRight(strings.ReplaceAll(string(b), "\x00", " "), " "), true
}

// NewProcSnapshotFor captures the process subtrees under the given pane pids
// from /proc (falling back to the full `ps` snapshot if /proc cannot serve
// them), so the maintainer's per-tick agent resolution costs a few file reads
// instead of a fork of the whole process table.
func NewProcSnapshotFor(panePIDs []int) (*ProcSnapshot, error) {
	if idx, err := snapshotProcsUnder(panePIDs, 4); err == nil {
		return &ProcSnapshot{idx: idx}, nil
	}
	return NewProcSnapshot()
}
