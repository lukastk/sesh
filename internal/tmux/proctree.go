package tmux

import (
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// proctree.go is ported from sesh v1. It identifies whether a REAL coding-agent
// process is running under a pane by walking the pane's process subtree — the
// honest answer to "is the agent actually alive here", robust to wrappers (codex
// runs as a child of a `node` pane process, so pane_current_command alone lies).

type proc struct {
	pid     int
	ppid    int
	command string
}

// agentRe matches a coding-agent argv0 (optionally path-prefixed), anchored so
// "pinta"/"claude-foo"/"codex-x" don't false-match.
var agentRe = regexp.MustCompile(`^(?:\S*/)?(pi|claude|codex)(?:\s|$)`)

type procIndex struct {
	byPID    map[int]proc
	children map[int][]int
}

func snapshotProcs() (*procIndex, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("ps", "-axww", "-o", "pid=,ppid=,command=")
	} else {
		cmd = exec.Command("ps", "-eww", "-o", "pid=,ppid=,args=")
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	idx := &procIndex{byPID: map[int]proc{}, children: map[int][]int{}}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		command := strings.TrimSpace(line[strings.Index(line, fields[1])+len(fields[1]):])
		idx.byPID[pid] = proc{pid: pid, ppid: ppid, command: command}
		idx.children[ppid] = append(idx.children[ppid], pid)
	}
	return idx, nil
}

// AgentProc is a coding-agent process found under a pane.
type AgentProc struct {
	PID     int
	Kind    string // pi | claude | codex
	Command string
	Depth   int
}

// findAgent BFS-walks descendants of rootPID up to maxDepth, returning the first
// coding-agent process. Agents are usually direct children (depth 1); deeper
// catches wrappers.
func (idx *procIndex) findAgent(rootPID, maxDepth int) (AgentProc, bool) {
	type item struct{ pid, depth int }
	queue := []item{{rootPID, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth > maxDepth {
			continue
		}
		if p, ok := idx.byPID[cur.pid]; ok {
			if m := agentRe.FindStringSubmatch(p.command); m != nil {
				return AgentProc{PID: p.pid, Kind: m[1], Command: p.command, Depth: cur.depth}, true
			}
		}
		for _, c := range idx.children[cur.pid] {
			queue = append(queue, item{c, cur.depth + 1})
		}
	}
	return AgentProc{}, false
}

// AgentUnderPane reports the coding-agent process running under panePID (the
// pane's process), if any. maxDepth 4 covers shell/wrapper nesting.
func AgentUnderPane(panePID int) (AgentProc, bool) {
	idx, err := snapshotProcs()
	if err != nil {
		return AgentProc{}, false
	}
	return idx.findAgent(panePID, 4)
}

// ProcSnapshot is a one-shot capture of the process table, so a caller resolving
// the agent under MANY panes in a single pass runs `ps` ONCE instead of once per
// pane. The maintainer needs this: at ~100 threads, a per-pane `ps` made its sweep
// take seconds, so each pane was sampled far less than twice per busy window and the
// content-diff `busy` signal could never latch.
type ProcSnapshot struct{ idx *procIndex }

// NewProcSnapshot captures the process table once (one `ps`).
func NewProcSnapshot() (*ProcSnapshot, error) {
	idx, err := snapshotProcs()
	if err != nil {
		return nil, err
	}
	return &ProcSnapshot{idx: idx}, nil
}

// AgentUnderPane reports the coding-agent process under panePID using this
// already-captured snapshot — identical resolution to the package-level
// AgentUnderPane, just without re-running `ps`.
func (p *ProcSnapshot) AgentUnderPane(panePID int) (AgentProc, bool) {
	return p.idx.findAgent(panePID, 4)
}
