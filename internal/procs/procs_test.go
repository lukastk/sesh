package procs

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// parseStatPPID's only job is to survive comm. /proc/<pid>/stat writes the
// executable name unquoted and unescaped between parentheses, so a process
// called "tmux: server" or one whose name contains a ')' shifts every
// whitespace-split field — the exact trap that has already made a field-counting
// reader in this repo misreport a process's start time.
func TestParseStatPPID(t *testing.T) {
	cases := []struct {
		name string
		line string
		want int
	}{
		{"ordinary", "4242 (zsh) S 4200 4242 4242 34816 …", 4200},
		{"comm with a space", "4242 (tmux: server) S 1 4242 …", 1},
		{"comm with parens", "4242 ((weird)) S 99 4242 …", 99},
		{"comm with a space AND parens", "4242 (foo (bar) baz) S 7 4242 …", 7},
		{"orphan reparented to init", "4242 (sleep) S 1 4242 …", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStatPPID([]byte(tc.line))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("ppid = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestParseStatPPIDRejectsGarbage(t *testing.T) {
	for _, line := range []string{
		"",
		"4242 zsh S 4200",            // no comm parens at all
		"4242 (zsh)",                 // nothing after comm
		"4242 (zsh) S",               // state but no ppid
		"4242 (zsh) S notanumber 42", // unparseable ppid
	} {
		if _, err := parseStatPPID([]byte(line)); err == nil {
			t.Errorf("parseStatPPID(%q) must be a loud error, got nil", line)
		}
	}
}

// The real tree, with real processes: a child is a descendant, the relation is
// NOT symmetric (the bug that would make every process claim every other), and a
// multi-level walk reaches init.
func TestIsAncestorRealProcesses(t *testing.T) {
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		// By recorded pid, never by pattern: a `pkill -f "sleep 30"` here would
		// reach into other sessions' polling loops, which has happened.
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	})
	me, kid := os.Getpid(), child.Process.Pid

	if ok, err := IsAncestor(me, kid); err != nil || !ok {
		t.Errorf("IsAncestor(me=%d, child=%d) = (%v, %v), want (true, nil)", me, kid, ok, err)
	}
	if ok, err := IsAncestor(kid, me); err != nil || ok {
		t.Errorf("IsAncestor(child=%d, me=%d) = (%v, %v), want (false, nil) — the relation is directional", kid, me, ok, err)
	}
	if ok, err := IsAncestor(me, me); err != nil || !ok {
		t.Errorf("a process must be its own ancestor (the turn's root process asking about itself): (%v, %v)", ok, err)
	}
	// A multi-level walk: every process descends from init, so this exercises the
	// loop rather than a single link.
	if ok, err := IsAncestor(1, me); err != nil || !ok {
		t.Errorf("IsAncestor(1, me) = (%v, %v), want (true, nil)", ok, err)
	}
	// And the sibling-ish negative: init is not descended from us.
	if ok, err := IsAncestor(me, 1); err != nil || ok {
		t.Errorf("IsAncestor(me, 1) = (%v, %v), want (false, nil)", ok, err)
	}
}

// An unreadable tree must be an ERROR, never a quiet false. The caller's whole
// job is to distinguish "provably not yours" from "could not tell" — reporting
// the second as the first would turn an unknown into a confident answer, which is
// the failure class this package serves to prevent.
func TestIsAncestorUnreadableIsLoud(t *testing.T) {
	const impossible = 2147483646 // above any Linux pid_max; no such process
	_, err := IsAncestor(os.Getpid(), impossible)
	if err == nil {
		t.Fatal("walking up from a nonexistent pid must be a loud error, got nil")
	}
	if !strings.Contains(err.Error(), "procs:") {
		t.Errorf("error should name this package: %v", err)
	}
	for _, pair := range [][2]int{{0, 1}, {1, 0}, {-1, 5}, {5, -1}} {
		if _, err := IsAncestor(pair[0], pair[1]); err == nil {
			t.Errorf("IsAncestor%v must reject a non-positive pid", pair)
		}
	}
}
