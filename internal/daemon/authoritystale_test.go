package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// TestStaleReportedBusy pins the predicate truth table: both the pane freeze
// AND the report age must exceed the bound; an uncaptured pane proves nothing.
func TestStaleReportedBusy(t *testing.T) {
	now := time.Now()
	bound := 2 * time.Minute
	cases := []struct {
		name       string
		lastChange time.Time
		reportedAt int64
		want       bool
	}{
		{"frozen pane, old report -> stale", now.Add(-3 * time.Minute), now.Add(-3 * time.Minute).Unix(), true},
		{"frozen pane, FRESH report -> not stale (turn about to render)", now.Add(-3 * time.Minute), now.Unix(), false},
		{"animating pane, old report -> not stale (real turn)", now.Add(-time.Second), now.Add(-10 * time.Minute).Unix(), false},
		{"pane never captured -> proves nothing", time.Time{}, now.Add(-10 * time.Minute).Unix(), false},
		{"exactly at the bound -> stale", now.Add(-bound), now.Add(-bound).Unix(), true},
	}
	for _, c := range cases {
		if got := staleReportedBusy(c.lastChange, c.reportedAt, now, bound); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestMaintainerDropsStaleReportedBusy drives the REAL maintainer against a
// REAL tmux pane (a static process argv0-named "claude" so AgentUnderPane
// resolves it — this is a daemon-internal test outside the matrix; the honest
// real-agent authority flows live in the conformance stateauthority cells):
// a reported turn_started on a byte-stable pane pins busy only until the
// staleness bound, then the entry is dropped, busy degrades to the heuristic
// idle VISIBLY (state_authority flips reported->heuristic) — the lost
// turn_ended class (claude's Stop hook does not fire on a user interrupt).
// A BLOCKED report on the same frozen pane is exempt and keeps pinning.
func TestMaintainerDropsStaleReportedBusy(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	// A fake agent named "claude": a symlink to sleep (a shebang script would
	// read as argv0 "sh" and miss the agent regex), invoked as `claude
	// infinity` — renders nothing, the byte-stable pane of an
	// interrupted-and-abandoned agent.
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.Symlink(sleepBin, bin); err != nil {
		t.Fatal(err)
	}

	sock := "seshstale-test-" + strings.ReplaceAll(t.Name(), "/", "_")
	raw := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := raw("-f", "/dev/null", "new-session", "-d", "-s", "frozen", "-x", "80", "-y", "20", bin+" infinity"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck

	const tid = "tid-stale-busy"
	if err := st.InsertThread(api.Thread{ID: tid, Machine: "test", SessionName: "frozen", AgentKind: "claude", Cwd: "/tmp"}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	pane, err := raw("list-panes", "-t", "frozen", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("pane id: %v", err)
	}
	if _, err := raw("set-option", "-p", "-t", pane, "@sesh-thread-id", tid); err != nil {
		t.Fatalf("mark pane: %v", err)
	}

	d := &Daemon{store: st, tmux: tmux.NewServer(sock)}
	m := newMaintainer(d)
	m.staleBound = 2 * time.Second // injectable: no real 2-minute freeze needed

	snapOf := func() api.ThreadSnapshot {
		t.Helper()
		sn, ok := m.stateOf(tid)
		if !ok {
			t.Fatalf("thread has no maintained state")
		}
		return sn
	}

	// Baseline: seed the content diff, no authority -> heuristic idle.
	m.tick()
	time.Sleep(500 * time.Millisecond)
	m.tick()
	if sn := snapOf(); sn.Busy != api.BusyIdle || sn.StateAuthority != api.AuthorityHeuristic {
		t.Fatalf("baseline: busy=%s authority=%s, want idle/heuristic", sn.Busy, sn.StateAuthority)
	}

	// A reported turn_started pins busy (authority=reported) while fresh.
	if code, err := d.reportState(api.ReportStateRequest{ThreadID: tid, Event: api.ReportTurnStarted, Source: "test", Seq: 1}, time.Now().Unix()); err != nil {
		t.Fatalf("reportState: %d %v", code, err)
	}
	m.tick()
	if sn := snapOf(); sn.Busy != api.BusyBusy || sn.StateAuthority != api.AuthorityReported {
		t.Fatalf("fresh report: busy=%s authority=%s, want busy/reported", sn.Busy, sn.StateAuthority)
	}

	// Past the bound (pane frozen the whole time): the entry is dropped and
	// busy degrades to heuristic idle — visibly.
	time.Sleep(2500 * time.Millisecond)
	m.tick()
	if sn := snapOf(); sn.Busy != api.BusyIdle || sn.StateAuthority != api.AuthorityHeuristic {
		t.Fatalf("past bound: busy=%s authority=%s, want idle/heuristic (stale authority dropped)", sn.Busy, sn.StateAuthority)
	}
	if _, still := d.reportedState(tid); still {
		t.Fatalf("authority entry still present after the stale drop")
	}

	// BLOCKED is exempt: a permission/question prompt is genuinely mid-turn
	// with a static pane, however long it sits.
	if code, err := d.reportState(api.ReportStateRequest{ThreadID: tid, Event: api.ReportBlocked, Source: "test", Seq: 2, Reason: "needs your permission"}, time.Now().Unix()); err != nil {
		t.Fatalf("reportState blocked: %d %v", code, err)
	}
	time.Sleep(2500 * time.Millisecond)
	m.tick()
	if sn := snapOf(); sn.Busy != api.BusyBusy || sn.StateAuthority != api.AuthorityReported {
		t.Fatalf("blocked past bound: busy=%s authority=%s, want busy/reported (blocked exempt)", sn.Busy, sn.StateAuthority)
	}
}
