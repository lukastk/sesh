package daemon

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// TestMaintainerRevGatedSweep pins the C3 scaling property
// (_dev/MESH_SCALE.md): settled threads — no runtime, which is every archived
// thread — cost ZERO per-tick work. Sweeping is O(live); a record write (rev
// triggers) or a hold expiry forces exactly one full sweep; the baseline sweep
// emits nothing and later changes emit through the maintainer's publish path.
func TestMaintainerRevGatedSweep(t *testing.T) {
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
	sock := "seshscale-" + strings.ReplaceAll(t.Name(), "/", "_")
	if _, err := exec.Command("tmux", "-L", sock, "-f", "/dev/null", "new-session", "-d", "-s", "idle", "-x", "80", "-y", "20").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck

	d := &Daemon{store: st, tmux: tmux.NewServer(sock), cfg: config.Config{Machine: "test"},
		hlInFlight: map[string]bool{}, hlReply: map[string]string{}}
	evt, cap := newCaptureEventer(d)
	d.evt = evt
	m := newMaintainer(d)

	// Four settled (headless, paneless) records — stand-ins for the archived corpus.
	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		if err := st.InsertThread(api.Thread{ID: id, Machine: "test", SessionName: id, AgentKind: "pi"}); err != nil {
			t.Fatalf("InsertThread: %v", err)
		}
	}

	// Tick 1 = the BASELINE full sweep: everything derived once, nothing emitted.
	m.tick()
	if m.fullSweeps != 1 || m.sweptThreads != 4 {
		t.Fatalf("baseline: fullSweeps=%d sweptThreads=%d, want 1/4", m.fullSweeps, m.sweptThreads)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(cap.snapshot()); n != 0 {
		t.Fatalf("baseline sweep emitted %d events — a restart re-announced existing state", n)
	}

	// Quiet ticks: rev unchanged, every thread settled — ZERO work.
	m.tick()
	m.tick()
	m.tick()
	if m.fullSweeps != 1 {
		t.Fatalf("quiet ticks re-read records: fullSweeps=%d", m.fullSweeps)
	}
	if m.sweptThreads != 4 {
		t.Fatalf("quiet ticks swept settled threads: sweptThreads=%d, want still 4 — the O(total) sweep is the bug this replaced", m.sweptThreads)
	}

	// A record write bumps the rev (schema triggers) => exactly one full sweep,
	// and the change EMITS now that the baseline is over.
	if err := st.RenameThread("s2", "renamed"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	m.tick()
	if m.fullSweeps != 2 || m.sweptThreads != 8 {
		t.Fatalf("post-write: fullSweeps=%d sweptThreads=%d, want 2/8", m.fullSweeps, m.sweptThreads)
	}
	found := false
	for _, env := range cap.waitFor(t, 1) {
		if env["SESH_EVENT"] == "thread_renamed" && env["SESH_THREAD_NAME"] == "renamed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rename never emitted through the publish path: %v", cap.snapshot())
	}
	m.tick() // and it settles again
	if m.fullSweeps != 2 || m.sweptThreads != 8 {
		t.Fatalf("post-write settle: fullSweeps=%d sweptThreads=%d", m.fullSweeps, m.sweptThreads)
	}

	// An in-flight headless turn is UNSETTLED: swept per tick while it lasts
	// (busy can end with no record write), settled again once observed idle.
	d.hlMu.Lock()
	d.hlInFlight["s3"] = true
	d.hlMu.Unlock()
	m.tick()
	if m.sweptThreads != 9 {
		t.Fatalf("busy headless tick swept %d total, want 9 (just s3)", m.sweptThreads)
	}
	if sn, _ := m.stateOf("s3"); sn.Busy != api.BusyBusy {
		t.Fatalf("s3 not busy: %+v", sn)
	}
	d.hlMu.Lock()
	delete(d.hlInFlight, "s3")
	d.hlMu.Unlock()
	m.tick() // observes the turn end (last snap busy => still unsettled this tick)
	if sn, _ := m.stateOf("s3"); sn.Busy != api.BusyIdle {
		t.Fatalf("s3 did not flip idle: %+v", sn)
	}
	swept := m.sweptThreads
	m.tick()
	if m.sweptThreads != swept {
		t.Fatalf("idle s3 still being swept (%d -> %d)", swept, m.sweptThreads)
	}

	// A hold expiry has NO record write — the deadline itself forces the sweep
	// that flips OnHold off.
	until := time.Now().Add(1200 * time.Millisecond).Unix()
	if err := st.SetThreadHold("s1", until, 0); err != nil {
		t.Fatalf("hold: %v", err)
	}
	m.tick() // full (rev bump); learns the deadline
	if sn, _ := m.stateOf("s1"); !sn.OnHold {
		t.Fatalf("s1 not on hold: %+v", sn)
	}
	full := m.fullSweeps
	m.tick()
	if m.fullSweeps != full {
		t.Fatalf("held-but-unexpired tick went full")
	}
	time.Sleep(time.Until(time.Unix(until, 0)) + 100*time.Millisecond)
	m.tick() // the deadline passed: full sweep, OnHold flips off
	if m.fullSweeps != full+1 {
		t.Fatalf("hold expiry did not force a full sweep")
	}
	if sn, _ := m.stateOf("s1"); sn.OnHold {
		t.Fatalf("s1 still on hold after expiry: %+v", sn)
	}

	// Deletion: emitted, tombstoned, gone.
	if err := st.DeleteThread("s4"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	m.tick()
	if _, ok := m.stateOf("s4"); ok {
		t.Fatalf("deleted thread still maintained")
	}
	deleted := false
	for _, env := range cap.snapshot() {
		if env["SESH_EVENT"] == "thread_deleted" && env["SESH_THREAD_ID"] == "s4" {
			deleted = true
		}
	}
	if !deleted {
		// handle() is async — give it a beat.
		time.Sleep(100 * time.Millisecond)
		for _, env := range cap.snapshot() {
			if env["SESH_EVENT"] == "thread_deleted" && env["SESH_THREAD_ID"] == "s4" {
				deleted = true
			}
		}
	}
	if !deleted {
		t.Fatalf("deletion never emitted: %v", cap.snapshot())
	}
}
