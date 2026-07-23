package conformance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

// thread.state-authority (schema 43, _dev/STATE_AUTHORITY.md): an in-agent
// reporter — the pi extension (integrations/pi/sesh-agent-state, registered
// globally in ~/.pi/agent/extensions via myagent) or the claude hooks
// (integrations/claude/sesh-agent-state.sh, registered in the myrig-managed
// ~/.claude/settings.json) — drives the thread's busy axis EXACTLY, overriding
// the pane content-diff heuristic, with the deciding mechanism always visible
// as state_authority. These cells exercise the REAL reporter end to end: the
// real agent process loads the real integration, which execs the real
// `$SESH_BIN thread report-state` back into the daemon under test (SESH_BIN is
// the spawning daemon's own binary — the pane's PATH-resolved `sesh` may be an
// older installed one). codex is a justified N/A (no in-agent turn-start
// surface; declared on the feature).
//
// ENVIRONMENTAL PREREQUISITE (like the agent binaries themselves): the pi
// extension symlink and the claude hook registration must be installed on the
// machine running the suite. A missing registration shows up here as the
// authority never leaving "heuristic" — a red cell, not a silent pass.
func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range []matrix.Agent{matrix.Claude, matrix.Pi} {
			a := a
			matrix.RegisterTest("thread.state-authority", a, loc,
				func(t *testing.T) { testStateAuthority(t, string(a), loc) })
		}
	}
}

// threadSnapshot reads one thread's maintained snapshot through the sandbox's
// runner (routed via --machine for remote cells — the owner's maintainer is
// what stamps state_authority, and the routed read is the honest client path).
func (sb *Sandbox) threadSnapshot(t *testing.T, id string) api.ThreadSnapshot {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "thread", "snapshot", "--json")
	if err != nil {
		t.Fatalf("thread snapshot: %v\n%s", err, stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var snap api.ThreadSnapshot
		if uerr := json.Unmarshal([]byte(line), &snap); uerr != nil {
			t.Fatalf("thread snapshot: bad JSONL line %q: %v", line, uerr)
		}
		if snap.ID == id {
			return snap
		}
	}
	return api.ThreadSnapshot{} // not yet published — callers poll
}

func testStateAuthority(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, agent, "auth", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, agent)

	// (1) A headful thread ALWAYS carries a state_authority — the floor is
	// visible, never blank. pi's extension reports at session_start so it may
	// already read reported; claude reports only from its first turn, so
	// pre-turn it MUST read heuristic (proving the floor engages when no
	// reporter has spoken).
	if !waitUntil(10*time.Second, func() bool {
		return sb.threadSnapshot(t, th.ID).StateAuthority != ""
	}) {
		t.Fatalf("headful %s thread never published a state_authority", agent)
	}
	if agent == "claude" {
		if got := sb.threadSnapshot(t, th.ID).StateAuthority; got != api.AuthorityHeuristic {
			t.Errorf("pre-turn claude state_authority = %q, want heuristic (no hook has fired yet)", got)
		}
	}

	// (2) A REAL turn through the REAL reporter, both directions: busy with
	// authority=reported (the reporter's turn_started overrides the diff),
	// then settled idle still reported (turn_ended).
	sb.sendKeys(t, pane, "Write a detailed 150-word explanation of how TCP congestion control works")
	if !waitUntil(30*time.Second, func() bool {
		s := sb.threadSnapshot(t, th.ID)
		return s.Busy == api.BusyBusy && s.StateAuthority == api.AuthorityReported
	}) {
		s := sb.threadSnapshot(t, th.ID)
		t.Fatalf("%s never reached busy+reported after send (busy=%s authority=%q) — is the %s reporter registered on this machine?",
			agent, s.Busy, s.StateAuthority, agent)
	}
	if !waitUntil(120*time.Second, func() bool {
		s := sb.threadSnapshot(t, th.ID)
		return s.Busy == api.BusyIdle && s.StateAuthority == api.AuthorityReported
	}) {
		s := sb.threadSnapshot(t, th.ID)
		t.Fatalf("%s turn never settled to idle+reported (busy=%s authority=%q)", agent, s.Busy, s.StateAuthority)
	}

	// (3) The pane-liveness bound: stop kills the pane, which must CLEAR the
	// authority — headless·idle with NO authority label. A reporter that died
	// with its agent can never pin busy (the codex-liveness bug class, applied
	// to the new mechanism).
	if _, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", th.ID); err != nil {
		t.Fatalf("thread stop: %v\n%s", err, stderr)
	}
	if !waitUntil(15*time.Second, func() bool {
		s := sb.threadSnapshot(t, th.ID)
		return s.Head == api.Headless && s.Busy == api.BusyIdle && s.StateAuthority == ""
	}) {
		s := sb.threadSnapshot(t, th.ID)
		t.Fatalf("post-stop state = %s·%s authority=%q, want headless·idle with authority unset", s.Head, s.Busy, s.StateAuthority)
	}
}
