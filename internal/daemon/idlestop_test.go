package daemon

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

// TestIdleStopVerdict is the eligibility truth table of _dev/IDLE_STOP.md §5
// and the idle clock of §4.
func TestIdleStopVerdict(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	agent := api.Thread{ID: "t", AgentKind: "claude"}
	idle := api.ThreadSnapshot{Head: api.Headful, Busy: api.BusyIdle, LastActiveUnix: ago(25 * time.Hour)}
	with := func(f func(*api.ThreadSnapshot)) api.ThreadSnapshot { s := idle; f(&s); return s }
	withThread := func(f func(*api.Thread)) api.Thread { t := agent; f(&t); return t }
	day := 24 * time.Hour

	for _, tc := range []struct {
		name     string
		th       api.Thread
		snap     api.ThreadSnapshot
		have     bool
		first    time.Time
		after    time.Duration
		stop     bool
		reasonIn string
	}{
		{"idle past the limit", agent, idle, true, time.Time{}, day, true, "idle 25h0m0s (limit 24h0m0s)"},
		{"idle under the limit", agent, with(func(s *api.ThreadSnapshot) { s.LastActiveUnix = ago(time.Hour) }), true, time.Time{}, day, false, "idle 1h0m0s of 24h0m0s"},
		{"no limit anywhere", agent, idle, true, time.Time{}, 0, false, "no idle limit"},
		{"busy (a turn or background work)", agent, with(func(s *api.ThreadSnapshot) { s.Busy = api.BusyBusy }), true, time.Time{}, day, false, "busy"},
		{"headless", agent, with(func(s *api.ThreadSnapshot) { s.Head = api.Headless }), true, time.Time{}, day, false, "headless"},
		{"not observed", agent, api.ThreadSnapshot{}, false, time.Time{}, day, false, "not observed"},
		{"shell thread", withThread(func(t *api.Thread) { t.AgentKind = api.ShellAgentKind }), idle, true, time.Time{}, day, false, "not an agent thread"},
		{"virtual node", withThread(func(t *api.Thread) { t.AgentKind = api.VirtualAgentKind }), idle, true, time.Time{}, day, false, "not an agent thread"},
		{"override never", withThread(func(t *api.Thread) { t.IdleStop = "never" }), idle, true, time.Time{}, day, false, "never"},
		{"override longer keeps it", withThread(func(t *api.Thread) { t.IdleStop = "48h" }), idle, true, time.Time{}, day, false, "of 48h0m0s"},
		{"override shorter stops it", withThread(func(t *api.Thread) { t.IdleStop = "1h" }), with(func(s *api.ThreadSnapshot) { s.LastActiveUnix = ago(2 * time.Hour) }), true, time.Time{}, day, true, "limit 1h0m0s"},
		{"override without a machine limit", withThread(func(t *api.Thread) { t.IdleStop = "1h" }), idle, true, time.Time{}, 0, true, "limit 1h0m0s"},
		{"invalid override keeps it", withThread(func(t *api.Thread) { t.IdleStop = "soon" }), idle, true, time.Time{}, day, false, "invalid idle_stop"},
		{"recent attached input", agent, with(func(s *api.ThreadSnapshot) { s.AttachedActivityUnix = ago(time.Minute) }), true, time.Time{}, day, false, "idle 1m0s of"},
		{"pending wake-up", agent, with(func(s *api.ThreadSnapshot) {
			s.Background = &api.BackgroundState{Wakeups: []api.BackgroundWakeup{{ID: "c", Schedule: "0 9 * * *"}}}
		}), true, time.Time{}, day, false, "wake-up"},
		{"empty background report is no obstacle", agent, with(func(s *api.ThreadSnapshot) { s.Background = &api.BackgroundState{} }), true, time.Time{}, day, true, "limit 24h0m0s"},
		{"no activity reading", agent, with(func(s *api.ThreadSnapshot) { s.LastActiveUnix = 0 }), true, time.Time{}, day, false, "no activity reading"},
		// A pane static since the daemon first saw it publishes no LastActiveUnix;
		// first-seen is its activity, so the clock still runs.
		{"static since first seen, past the limit", agent, with(func(s *api.ThreadSnapshot) { s.LastActiveUnix = 0 }), true, now.Add(-25 * time.Hour), day, true, "limit 24h0m0s"},
		{"static since first seen, under the limit", agent, with(func(s *api.ThreadSnapshot) { s.LastActiveUnix = 0 }), true, now.Add(-time.Hour), day, false, "idle 1h0m0s of"},
		{"first seen after the last change wins", agent, idle, true, now.Add(-time.Hour), day, false, "idle 1h0m0s of"},
	} {
		stop, reason, _, _ := idleStopVerdict(tc.th, tc.snap, tc.have, tc.first, tc.after, now)
		if stop != tc.stop || !strings.Contains(reason, tc.reasonIn) {
			t.Errorf("%s: got stop=%v reason=%q, want stop=%v reason containing %q", tc.name, stop, reason, tc.stop, tc.reasonIn)
		}
	}
}

func TestIdleSeen(t *testing.T) {
	var s idleSeen
	t0 := time.Unix(100, 0)
	if got := s.observe("a", true, t0); !got.Equal(t0) {
		t.Fatalf("first observe = %v, want %v", got, t0)
	}
	if got := s.observe("a", true, t0.Add(time.Minute)); !got.Equal(t0) {
		t.Fatalf("second observe = %v, want the first %v", got, t0)
	}
	if got := s.observe("a", false, t0.Add(2*time.Minute)); !got.IsZero() {
		t.Fatalf("not live = %v, want zero", got)
	}
	if got := s.observe("a", true, t0.Add(3*time.Minute)); !got.Equal(t0.Add(3 * time.Minute)) {
		t.Fatalf("revived = %v, want a fresh clock", got)
	}
}

func TestIdleStopInterval(t *testing.T) {
	for after, want := range map[time.Duration]time.Duration{
		0: 30 * time.Second, 2 * time.Second: time.Second, 20 * time.Second: 5 * time.Second, 24 * time.Hour: 30 * time.Second,
	} {
		if got := idleStopInterval(after); got != want {
			t.Errorf("idleStopInterval(%s) = %s, want %s", after, got, want)
		}
	}
}

// TestIdleStopRecordRoundTrip pins the stored override and the stop record
// (migration 32): set, read back, clear on start.
func TestIdleStopRecordRoundTrip(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := &Daemon{store: st}
	if err := st.InsertThread(api.Thread{ID: "t1", Machine: "m", SessionName: "s", AgentKind: "claude"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetThreadIdleStop("t1", "never"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetThreadIdleStopped("t1", 1234, "idle 25h (limit 24h)"); err != nil {
		t.Fatal(err)
	}
	th, err := st.GetThread("t1")
	if err != nil {
		t.Fatal(err)
	}
	if th.IdleStop != "never" || th.IdleStoppedAtUnix != 1234 || th.IdleStoppedReason != "idle 25h (limit 24h)" {
		t.Fatalf("round trip: %+v", th)
	}
	d.clearIdleStopped(th)
	if th, _ = st.GetThread("t1"); th.IdleStoppedAtUnix != 0 || th.IdleStoppedReason != "" || th.IdleStop != "never" {
		t.Fatalf("clear on start must drop the stop record and keep the override: %+v", th)
	}
	if err := st.SetThreadIdleStop("nope", ""); err != store.ErrThreadNotFound {
		t.Fatalf("unknown thread: want ErrThreadNotFound, got %v", err)
	}
}
