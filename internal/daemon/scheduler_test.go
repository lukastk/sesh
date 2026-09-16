package daemon

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/blobs"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// TestScheduleGuards is the guard truth table (pure over a snapshot).
func TestScheduleGuards(t *testing.T) {
	now := time.Unix(10_000, 0)
	idle := api.ThreadSnapshot{Head: api.Headful, Busy: api.BusyIdle, Attachment: api.Detached, LastActiveUnix: 9_000}
	busy := idle
	busy.Busy = api.BusyBusy
	zero := int64(0)
	ten := int64(10)
	cases := []struct {
		name    string
		rules   api.ScheduleRules
		snap    api.ThreadSnapshot
		blocked bool
		want    string
	}{
		{"idle target, no rules", api.ScheduleRules{}, idle, false, ""},
		{"busy target skips by default", api.ScheduleRules{}, busy, false, "busy"},
		{"busy target with when_busy=send", api.ScheduleRules{WhenBusy: "send"}, busy, false, ""},
		{"--if idle on a busy target", api.ScheduleRules{If: []string{"idle"}}, busy, false, "not idle"},
		{"--if idle implies a 60s dwell", api.ScheduleRules{If: []string{"idle"}}, func() api.ThreadSnapshot { s := idle; s.LastActiveUnix = 9_970; return s }(), false, "idle for only 30s (need 1m0s)"},
		{"--if idle dwell satisfied", api.ScheduleRules{If: []string{"idle"}}, idle, false, ""},
		{"--idle-for 0 disables the dwell", api.ScheduleRules{If: []string{"idle"}, IdleForS: &zero}, func() api.ThreadSnapshot { s := idle; s.LastActiveUnix = 9_999; return s }(), false, ""},
		{"--idle-for 10 explicit", api.ScheduleRules{If: []string{"idle"}, IdleForS: &ten}, func() api.ThreadSnapshot { s := idle; s.LastActiveUnix = 9_995; return s }(), false, "idle for only 5s (need 10s)"},
		{"on hold skips", api.ScheduleRules{}, func() api.ThreadSnapshot { s := idle; s.OnHold = true; return s }(), false, "on hold"},
		{"on hold with --ignore-hold", api.ScheduleRules{IgnoreHold: true}, func() api.ThreadSnapshot { s := idle; s.OnHold = true; return s }(), false, ""},
		{"archived skips", api.ScheduleRules{}, func() api.ThreadSnapshot { s := idle; s.Archived = true; return s }(), false, "archived"},
		{"archived with --allow-archived", api.ScheduleRules{AllowArchived: true}, func() api.ThreadSnapshot { s := idle; s.Archived = true; return s }(), false, ""},
		{"--if detached on an attached target", api.ScheduleRules{If: []string{"detached"}}, func() api.ThreadSnapshot { s := idle; s.Attachment = api.Attached; return s }(), false, "not detached"},
		{"--if headful on a headless target", api.ScheduleRules{If: []string{"headful"}}, func() api.ThreadSnapshot { s := idle; s.Head = api.Headless; return s }(), false, "not headful"},
		{"--if not-flagged on a flagged target", api.ScheduleRules{If: []string{"not-flagged"}}, func() api.ThreadSnapshot { s := idle; s.Flagged = true; return s }(), false, "not not-flagged"},
		{"--if blocked", api.ScheduleRules{If: []string{"blocked"}}, idle, false, "not blocked"},
		{"--if blocked, blocked", api.ScheduleRules{If: []string{"blocked"}, WhenBusy: "send"}, busy, true, ""},
		{"several conditions, all hold", api.ScheduleRules{If: []string{"idle", "detached", "not-archived"}}, idle, false, ""},
	}
	for _, c := range cases {
		if got := scheduleGuards(c.rules, c.snap, c.blocked, now); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRenderRunName(t *testing.T) {
	at := time.Date(2026, 9, 16, 9, 5, 0, 0, time.UTC)
	sc := api.Schedule{Name: "nightly"}
	if got := renderRunName(sc, at); got != "nightly-2026-09-16-0905" {
		t.Fatalf("default template: %q", got)
	}
	sc.NameTemplate = "review {date} ({schedule})"
	if got := renderRunName(sc, at); got != "review 2026-09-16 (nightly)" {
		t.Fatalf("custom template: %q", got)
	}
}

// bareSchedulerDaemon is a daemon with a store, a real tmux server and the
// scheduler wired, but NO HTTP server: actions that need the daemon's own
// endpoints (a headless turn, a revive) fail loudly — which the breaker test
// leans on — while a pane paste (direct) succeeds.
func bareSchedulerDaemon(t *testing.T, sock string) (*Daemon, *store.Store) {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	d := &Daemon{
		store: st, blobs: blobs.New(home), tmux: tmux.NewServer(sock), cfg: config.Config{Machine: "test", Home: home},
		schedules: config.SchedulesConfig{Enabled: true, DisableAfterFailures: 3},
		pasteStop: make(chan struct{}),
	}
	d.sched = newScheduler(d)
	return d, st
}

// TestSchedulerCatchup: a schedule whose occurrences were slept through is
// rolled forward under catchup=skip (missed counted, nothing fired), and fires
// exactly ONE catch-up run under catchup=once.
func TestSchedulerCatchup(t *testing.T) {
	d, st := bareSchedulerDaemon(t, "seshsched-catchup")
	now := time.Now()
	stale := now.Add(-2 * time.Hour).Unix()
	for _, sc := range []api.Schedule{
		{ID: "skip", Name: "skip", Action: api.ScheduleActionMessage, Enabled: true, Spec: "every 10m0s", TZ: "UTC", Catchup: api.CatchupSkip, ThreadID: "gone", Text: "x", NextFireUnix: stale, CreatedAtUnix: stale - 60},
		{ID: "once", Name: "once", Action: api.ScheduleActionMessage, Enabled: true, Spec: "every 10m0s", TZ: "UTC", Catchup: api.CatchupOnce, ThreadID: "gone", Text: "x", NextFireUnix: stale, CreatedAtUnix: stale - 60},
	} {
		if err := st.InsertSchedule(sc); err != nil {
			t.Fatal(err)
		}
	}
	d.sched.tickNow(now)
	// The runs are goroutines; give them a beat.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ev, _, _, _, _ := d.sched.counters(); ev >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	skip, _ := st.GetSchedule("skip")
	// Due 2h ago, every 10m: the due occurrence + 12 more slept through = 13.
	if skip.Missed != 13 || skip.NextFireUnix <= now.Unix() {
		t.Fatalf("catchup=skip: missed=%d next=%d (now %d) — want 13 missed (the due one + 12 slept through) and a future next_fire", skip.Missed, skip.NextFireUnix, now.Unix())
	}
	once, _ := st.GetSchedule("once")
	if once.Missed != 13 || once.NextFireUnix <= now.Unix() || once.LastFireUnix != now.Unix() {
		t.Fatalf("catchup=once: %+v", once)
	}
	ev, fired, _, failed, missed := d.sched.counters()
	if ev != 1 || fired != 0 || failed != 1 || missed != 26 {
		t.Fatalf("counters evaluated=%d fired=%d failed=%d missed=%d — want exactly one catch-up run (which fails: its thread is gone)", ev, fired, failed, missed)
	}
	// The gone target disabled the schedule loudly.
	if once, _ = st.GetSchedule("once"); once.Enabled || !strings.Contains(once.DisabledReason, "no longer exists") {
		t.Fatalf("a run against a deleted thread must disable the schedule: %+v", once)
	}
}

// TestSchedulerFiresIntoPane: a due message schedule pastes into the target's
// real pane, advances next_fire so the next tick does not re-fire, and records
// the run; a viewer typing makes the run SKIP (recorded), not wait.
func TestSchedulerFiresIntoPane(t *testing.T) {
	f := newPasteFixture(t)
	d := f.d
	d.cfg = config.Config{Machine: "test", Home: t.TempDir()}
	d.blobs = blobs.New(d.cfg.Home)
	d.schedules = config.SchedulesConfig{Enabled: true, DisableAfterFailures: 3}
	d.sched = newScheduler(d)
	// The fixture's thread record is Machine "test" in the store; the daemon's
	// machine must match for the snapshot path.
	now := time.Now()
	sc := api.Schedule{ID: "s1", Name: "nudge", Action: api.ScheduleActionMessage, Enabled: true, Spec: "every 1h0m0s", TZ: "UTC", Catchup: api.CatchupSkip,
		ThreadID: f.th.ID, Text: "SCHED-NUDGE", NextFireUnix: now.Unix() - 5, CreatedAtUnix: now.Unix() - 3600}
	if err := f.st.InsertSchedule(sc); err != nil {
		t.Fatal(err)
	}
	// The nested viewer is attached but has not typed within the guard window
	// (the fixture typed nothing yet): the paste lands.
	d.sched.tickNow(now)
	if !eventuallyD(15*time.Second, func() bool { return strings.Contains(f.captured(), "SCHED-NUDGE") }) {
		t.Fatalf("scheduled message never reached the pane:\n%s", f.captured())
	}
	got, _ := f.st.GetSchedule("s1")
	if got.NextFireUnix <= now.Unix() || got.LastFireUnix != now.Unix() {
		t.Fatalf("next_fire not advanced: %+v", got)
	}
	if !eventuallyD(5*time.Second, func() bool { g, _ := f.st.GetSchedule("s1"); return g.FireCount == 1 && g.LastOutcome == "fired: pane" }) {
		g, _ := f.st.GetSchedule("s1")
		t.Fatalf("run not recorded: %+v", g)
	}
	runs, _ := f.st.ListScheduleRuns("s1", 0)
	if len(runs) != 1 || !strings.HasPrefix(runs[0].Outcome, "fired") {
		t.Fatalf("runs: %+v", runs)
	}
	// A second tick at the same instant must not fire again.
	d.sched.tickNow(now.Add(time.Second))
	time.Sleep(300 * time.Millisecond)
	if ev, _, _, _, _ := d.sched.counters(); ev != 1 {
		t.Fatalf("re-fired on the next tick: evaluated=%d", ev)
	}
	// Make it due again with a typing viewer: the run skips, recorded.
	got.NextFireUnix = now.Unix() + 2
	if err := f.st.UpdateSchedule(got); err != nil {
		t.Fatal(err)
	}
	d.send.RespectTyping = 30 * time.Second
	f.typeThrough()
	time.Sleep(200 * time.Millisecond)
	d.sched.tickNow(now.Add(3 * time.Second))
	if !eventuallyD(5*time.Second, func() bool { g, _ := f.st.GetSchedule("s1"); return g.SkipCount == 1 }) {
		g, _ := f.st.GetSchedule("s1")
		t.Fatalf("typing viewer did not skip the run: %+v", g)
	}
	if g, _ := f.st.GetSchedule("s1"); !strings.HasPrefix(g.LastOutcome, "skipped: viewer typing") {
		t.Fatalf("outcome = %q", g.LastOutcome)
	}
	if n := strings.Count(f.captured(), "SCHED-NUDGE"); n != 1 {
		t.Fatalf("the skipped run pasted anyway (%d occurrences):\n%s", n, f.captured())
	}
}

// TestSchedulerFailureBreaker: consecutive failures disable a schedule at the
// configured streak, with the reason recorded; run-now's outcome says failed.
func TestSchedulerFailureBreaker(t *testing.T) {
	d, st := bareSchedulerDaemon(t, "seshsched-breaker")
	if err := st.InsertThread(api.Thread{ID: "t1", Machine: "test", SessionName: "headless-t1", AgentKind: "pi", Cwd: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	// A headless target with when_headless=turn needs the daemon's own
	// send-headless endpoint — absent here, so every run fails loudly.
	sc := api.Schedule{ID: "s1", Name: "broken", Action: api.ScheduleActionMessage, Enabled: true, Spec: "every 1h0m0s", TZ: "UTC", Catchup: api.CatchupSkip,
		ThreadID: "t1", Text: "x", NextFireUnix: time.Now().Add(time.Hour).Unix(), CreatedAtUnix: time.Now().Unix()}
	if err := st.InsertSchedule(sc); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		outcome, detail, _ := d.sched.runOnce(sc, time.Now().Add(time.Duration(i)*time.Second), false)
		if outcome != api.OutcomeFailed || !strings.Contains(detail, "headless turn") {
			t.Fatalf("run %d: outcome %q detail %q", i, outcome, detail)
		}
		got, _ := st.GetSchedule("s1")
		if got.FailStreak != i {
			t.Fatalf("run %d: fail_streak = %d", i, got.FailStreak)
		}
		if i < 3 && !got.Enabled {
			t.Fatalf("disabled before the breaker (%d)", i)
		}
	}
	got, _ := st.GetSchedule("s1")
	if got.Enabled || !strings.Contains(got.DisabledReason, "3 consecutive failures") {
		t.Fatalf("breaker did not disable: %+v", got)
	}
	runs, _ := st.ListScheduleRuns("s1", 0)
	if len(runs) != 3 {
		t.Fatalf("runs recorded: %d", len(runs))
	}
}

// TestBuildScheduleValidation: creation is loud on every bad input.
func TestBuildScheduleValidation(t *testing.T) {
	d, st := bareSchedulerDaemon(t, "seshsched-validate")
	if err := st.InsertThread(api.Thread{ID: "t1", Machine: "test", SessionName: "s", AgentKind: "pi", Cwd: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ok := api.CreateScheduleRequest{Action: "message", Spec: "every 15m", ThreadID: "t1", Text: "hi", Rules: api.ScheduleRules{If: []string{"idle"}}}
	sc, err := d.buildSchedule(ok, now, "")
	if err != nil {
		t.Fatalf("valid request refused: %v", err)
	}
	if sc.TZ == "" || sc.NextFireUnix <= now.Unix() || sc.Catchup != "skip" || sc.Spec != "every 15m0s" || !strings.HasPrefix(sc.Name, "message-") {
		t.Fatalf("defaults: %+v", sc)
	}
	bad := []struct {
		name string
		req  api.CreateScheduleRequest
		want string
	}{
		{"unknown action", api.CreateScheduleRequest{Action: "email", Spec: "every 15m", ThreadID: "t1", Text: "x"}, "want message or spawn"},
		{"unknown guard word", api.CreateScheduleRequest{Action: "message", Spec: "every 15m", ThreadID: "t1", Text: "x", Rules: api.ScheduleRules{If: []string{"sleepy"}}}, "not a guard word"},
		{"bad when_busy", api.CreateScheduleRequest{Action: "message", Spec: "every 15m", ThreadID: "t1", Text: "x", Rules: api.ScheduleRules{WhenBusy: "queue"}}, "when_busy"},
		{"spawn rule on a message", api.CreateScheduleRequest{Action: "message", Spec: "every 15m", ThreadID: "t1", Text: "x", Rules: api.ScheduleRules{OnTurnEnd: "keep"}}, "spawn rules"},
		{"thread not here", api.CreateScheduleRequest{Action: "message", Spec: "every 15m", ThreadID: "elsewhere", Text: "x"}, "not on this machine"},
		{"never fires", api.CreateScheduleRequest{Action: "message", Spec: "cron 0 0 31 2 *", ThreadID: "t1", Text: "x"}, "never fires"},
		{"bad cron", api.CreateScheduleRequest{Action: "message", Spec: "cron 99 * * * *", ThreadID: "t1", Text: "x"}, "minute field"},
		{"too frequent", api.CreateScheduleRequest{Action: "message", Spec: "every 2s", ThreadID: "t1", Text: "x"}, "minimum"},
		{"bad zone", api.CreateScheduleRequest{Action: "message", Spec: "every 15m", TZ: "Mars/Olympus", ThreadID: "t1", Text: "x"}, "tz"},
		{"bad catchup", api.CreateScheduleRequest{Action: "message", Spec: "every 15m", Catchup: "all", ThreadID: "t1", Text: "x"}, "catchup"},
		{"not_after in the past", api.CreateScheduleRequest{Action: "message", Spec: "every 15m", NotAfterUnix: now.Unix() - 1, ThreadID: "t1", Text: "x"}, "past"},
		{"empty text", api.CreateScheduleRequest{Action: "message", Spec: "every 15m", ThreadID: "t1", Text: "  "}, "required"},
		{"unknown blob", api.CreateScheduleRequest{Action: "message", Spec: "every 15m", ThreadID: "t1", Text: "see @blob(deadbeef)"}, "blob"},
		{"spawn without prompt", api.CreateScheduleRequest{Action: "spawn", Spec: "every 15m", Agent: "pi", Cwd: "/tmp"}, "prompt is required"},
		{"spawn relative cwd", api.CreateScheduleRequest{Action: "spawn", Spec: "every 15m", Agent: "pi", Cwd: "proj", Prompt: "x"}, "absolute"},
		{"spawn bad agent", api.CreateScheduleRequest{Action: "spawn", Spec: "every 15m", Agent: "gpt", Cwd: "/tmp", Prompt: "x"}, "agent"},
		{"spawn message rule", api.CreateScheduleRequest{Action: "spawn", Spec: "every 15m", Agent: "pi", Cwd: "/tmp", Prompt: "x", Rules: api.ScheduleRules{WhenBusy: "skip"}}, "message rules"},
		{"spawn bad on_turn_end", api.CreateScheduleRequest{Action: "spawn", Spec: "every 15m", Agent: "pi", Cwd: "/tmp", Prompt: "x", Rules: api.ScheduleRules{OnTurnEnd: "kill"}}, "on_turn_end"},
		{"spawn into_session headless", api.CreateScheduleRequest{Action: "spawn", Spec: "every 15m", Agent: "pi", Cwd: "/tmp", Prompt: "x", IntoSession: "s"}, "headed"},
		{"duplicate name", api.CreateScheduleRequest{Name: "dup", Action: "message", Spec: "every 15m", ThreadID: "t1", Text: "x"}, "already exists"},
	}
	if err := st.InsertSchedule(api.Schedule{ID: "existing", Name: "dup", Action: "message", Enabled: true, Spec: "every 15m0s", TZ: "UTC", Catchup: "skip", ThreadID: "t1", Text: "x", CreatedAtUnix: 1}); err != nil {
		t.Fatal(err)
	}
	for _, c := range bad {
		_, err := d.buildSchedule(c.req, now, "")
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not name %q", c.name, err.Error(), c.want)
		}
	}
	// Editing the existing record keeps its own name without a self-collision.
	if _, err := d.buildSchedule(api.CreateScheduleRequest{Name: "dup", Action: "message", Spec: "every 15m", ThreadID: "t1", Text: "x"}, now, "existing"); err != nil {
		t.Fatalf("self-collision on edit: %v", err)
	}
	if _, err := d.resolveScheduleRef("dup"); err != nil {
		t.Fatalf("resolve by name: %v", err)
	}
	if _, err := d.resolveScheduleRef("nope"); !errors.Is(err, store.ErrScheduleNotFound) {
		t.Fatalf("unknown ref: %v", err)
	}
}
