package daemon

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

// TestReportStateBackground pins issue #24's authority semantics: a turn_ended
// carrying background TASKS keeps the thread effectively busy (the turn axis
// itself goes idle, so the stale-busy bound never sees it); wake-ups alone do
// not; a mid-turn event carries the set forward; only the next turn_ended
// replaces it; a turn_ended without background records "not reported" (nil),
// never a stale set; and background on any other event is refused loudly.
func TestReportStateBackground(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	d := &Daemon{store: st}
	if err := st.InsertThread(api.Thread{ID: "tid-c", Machine: "test", SessionName: "s", AgentKind: "claude"}); err != nil {
		t.Fatal(err)
	}
	var seq int64
	report := func(event string, bg *api.BackgroundState) (int, error) {
		t.Helper()
		seq++
		return d.reportState(api.ReportStateRequest{ThreadID: "tid-c", Source: "sesh:test", Event: event, Seq: seq, Background: bg}, 1000+seq)
	}
	mustReport := func(event string, bg *api.BackgroundState) {
		t.Helper()
		if _, err := report(event, bg); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
	}
	state := func() authorityState {
		t.Helper()
		a, ok := d.reportedState("tid-c")
		if !ok {
			t.Fatal("no authority entry")
		}
		return a
	}
	shell := &api.BackgroundState{Tasks: []api.BackgroundTask{{ID: "b1", Type: "shell", Status: "running", Description: "sleep 25"}}, Wakeups: []api.BackgroundWakeup{}}

	mustReport(api.ReportTurnStarted, nil)
	mustReport(api.ReportTurnEnded, shell)
	if a := state(); a.busy || !a.effectiveBusy() || len(a.background.Tasks) != 1 {
		t.Fatalf("turn_ended with a running shell: want turn idle + effectively busy, got busy=%v effective=%v bg=%+v", a.busy, a.effectiveBusy(), a.background)
	}
	// The reporter's struct must not be aliased by the authority entry.
	shell.Tasks[0].ID = "mutated"
	if state().background.Tasks[0].ID != "b1" {
		t.Fatal("authority entry aliases the request's background slice")
	}

	// The wake-up turn starts: busy on the turn axis, the set carried forward.
	mustReport(api.ReportTurnStarted, nil)
	if a := state(); !a.busy || !a.background.HasTasks() {
		t.Fatalf("turn_started after background: want busy + carried set, got busy=%v bg=%+v", a.busy, a.background)
	}
	// A stall mid-turn also carries it.
	mustReport(api.ReportBlocked, nil)
	if !state().background.HasTasks() {
		t.Fatal("blocked dropped the background set")
	}

	// The turn ends with nothing in flight but a pending wake-up: idle.
	mustReport(api.ReportTurnEnded, &api.BackgroundState{Tasks: []api.BackgroundTask{}, Wakeups: []api.BackgroundWakeup{{ID: "c1", Schedule: "*/20 * * * *", Recurring: true}}})
	if a := state(); a.effectiveBusy() || a.background == nil || len(a.background.Wakeups) != 1 {
		t.Fatalf("wake-up only: want idle with the wake-up recorded, got effective=%v bg=%+v", a.effectiveBusy(), a.background)
	}

	// A turn_ended that does not say (an older claude) is "not reported".
	mustReport(api.ReportTurnEnded, shell)
	mustReport(api.ReportTurnEnded, nil)
	if a := state(); a.background != nil || a.effectiveBusy() {
		t.Fatalf("turn_ended without background: want nil (not reported) and idle, got %+v", a.background)
	}

	// Background is only known at a turn end.
	for _, ev := range []string{api.ReportTurnStarted, api.ReportBlocked, api.ReportUnblocked, api.ReportRelease, api.ReportTurnEndedNoAuthority} {
		status, err := report(ev, shell)
		if err == nil || status != http.StatusBadRequest || !strings.Contains(err.Error(), "only accepted with turn_ended") {
			t.Fatalf("%s with background: want 400 'only accepted with turn_ended', got %d %v", ev, status, err)
		}
	}
}
