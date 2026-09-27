// Package procs answers one question about the OS process tree: is process A an
// ancestor of process B?
//
// WHY SESH NEEDS IT. Current-thread inference treats an identity as VERIFIED
// only when the calling process is INSIDE something an authority created and can
// be asked about live: a tmux pane, checked with tmux. A pane marker cannot be
// inherited by a process living elsewhere, which is exactly what makes it
// trustworthy — and $SESH_THREAD_ID, which CAN be, is why a detached background
// job once confidently reported itself as an unrelated thread (H82/H92/H112).
//
// A daemon-launched headless turn has no pane. What it does have is the process
// tree the daemon created for it, whose root pid the daemon remembers for as
// long as the turn is in flight. So "are you running inside the turn I started
// for this thread?" is answerable from the kernel's own parent links.
//
// THAT IS THE WHOLE REASON THIS IS AN ANCESTRY WALK AND NOT A TOKEN IN THE
// ENVIRONMENT. A per-turn secret injected into the turn's env would be inherited
// by precisely the detached and background processes the trust model distrusts —
// the failure shape of every mis-identification incident so far is a frozen
// environment outliving the context that produced it. An ancestry check has
// nothing to inherit: the only input is the caller's own pid and the answer is
// recomputed from the live tree, so a reparented process (ancestry reaching
// pid 1) is refused by construction.
package procs

import (
	"bytes"
	"fmt"
	"strconv"
)

// MaxDepth bounds the upward walk. A turn's tree is a handful of levels deep
// ($SHELL -c → agent → its tool shell → sesh); the bound exists so a cycle or a
// hostile /proc can never spin the daemon, and reaching it is a loud error
// rather than a silent "not a descendant".
const MaxDepth = 64

// IsAncestor reports whether `ancestor` is pid itself or a transitive parent of
// it. pid == ancestor is true on purpose: the turn's own root process asking is
// an even stronger case than a descendant asking.
//
// An error means the tree could not be read to a conclusion (a process in the
// chain exited mid-walk, /proc unreadable, the bound reached). Callers must
// treat that as "cannot confirm" and REFUSE — never as "not a descendant" that
// happens to be reported quietly, and never as a pass.
func IsAncestor(ancestor, pid int) (bool, error) {
	if ancestor <= 0 || pid <= 0 {
		return false, fmt.Errorf("procs: invalid pid pair (ancestor %d, pid %d)", ancestor, pid)
	}
	cur := pid
	for depth := 0; depth <= MaxDepth; depth++ {
		if cur == ancestor {
			return true, nil
		}
		if cur <= 1 {
			// Reached init (or a kernel-parented root): the chain is complete and
			// `ancestor` is not on it. A DETACHED process lands here — that is the
			// case this whole package exists to distinguish.
			return false, nil
		}
		ppid, err := ParentOf(cur)
		if err != nil {
			return false, fmt.Errorf("procs: reading the parent of %d while walking up from %d: %w", cur, pid, err)
		}
		if ppid == cur {
			return false, fmt.Errorf("procs: pid %d reports itself as its own parent", cur)
		}
		cur = ppid
	}
	return false, fmt.Errorf("procs: the parent chain above %d is longer than %d levels", pid, MaxDepth)
}

// parseStatPPID extracts the parent pid from the contents of /proc/<pid>/stat.
//
// The field layout is `pid (comm) state ppid …`, and comm is the trap: it is the
// executable name UNQUOTED and UNESCAPED, so it can contain spaces and
// parentheses — a tmux server's comm is literally "tmux: server", which shifts
// every later field by one if you split on whitespace. Parse after the LAST ')'
// instead, which is unambiguous because comm is the only bracketed field.
func parseStatPPID(b []byte) (int, error) {
	end := bytes.LastIndexByte(b, ')')
	if end < 0 {
		return 0, fmt.Errorf("procs: no comm field in stat line")
	}
	fields := bytes.Fields(b[end+1:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("procs: stat line has %d fields after comm, want at least 2", len(fields))
	}
	ppid, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		return 0, fmt.Errorf("procs: unparseable ppid %q: %w", fields[1], err)
	}
	if ppid < 0 {
		return 0, fmt.Errorf("procs: negative ppid %d", ppid)
	}
	return ppid, nil
}
