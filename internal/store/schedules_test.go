package store

import (
	"errors"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func TestScheduleCRUD(t *testing.T) {
	st := openTestStore(t)
	idle := int64(30)
	sc := api.Schedule{
		ID: "s1", Name: "heartbeat", Action: api.ScheduleActionMessage, Enabled: true,
		Spec: "every 20m0s", TZ: "Europe/London", Catchup: api.CatchupSkip, MaxFires: 30,
		ThreadID: "t1", Text: "keep going",
		Rules:        api.ScheduleRules{WhenBusy: "skip", If: []string{"idle"}, IdleForS: &idle},
		NextFireUnix: 1000, CreatedAtUnix: 900,
	}
	if err := st.InsertSchedule(sc); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSchedule("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "heartbeat" || got.Rules.WhenBusy != "skip" || got.Rules.IdleForS == nil || *got.Rules.IdleForS != 30 || len(got.Rules.If) != 1 || got.NextFireUnix != 1000 || !got.Enabled {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	rev1, _ := st.SchedulesRev()
	got.Enabled, got.DisabledReason, got.FireCount, got.LastOutcome = false, "breaker", 3, "failed: x"
	if err := st.UpdateSchedule(got); err != nil {
		t.Fatal(err)
	}
	rev2, _ := st.SchedulesRev()
	if rev2 <= rev1 {
		t.Fatalf("an update must bump the schedules rev (%d → %d)", rev1, rev2)
	}
	again, _ := st.GetSchedule("s1")
	if again.Enabled || again.DisabledReason != "breaker" || again.FireCount != 3 {
		t.Fatalf("update lost fields: %+v", again)
	}
	if err := st.UpdateSchedule(api.Schedule{ID: "nope"}); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("updating an unknown schedule must be loud, got %v", err)
	}
	byThread, _ := st.ListSchedulesByThread("t1")
	if len(byThread) != 1 {
		t.Fatalf("by thread: %d", len(byThread))
	}
	if _, err := st.GetSchedule("nope"); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("unknown get: %v", err)
	}
	if err := st.DeleteSchedule("nope"); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("unknown delete must be loud, got %v", err)
	}
	if err := st.DeleteSchedule("s1"); err != nil {
		t.Fatal(err)
	}
	all, _ := st.ListSchedules()
	if len(all) != 0 {
		t.Fatalf("after delete: %d schedules", len(all))
	}
}

func TestScheduleRunsAndCascade(t *testing.T) {
	st := openTestStore(t)
	if err := st.InsertThread(api.Thread{ID: "t1", Machine: "m", SessionName: "s", AgentKind: "pi", Cwd: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s1", "s2"} {
		if err := st.InsertSchedule(api.Schedule{ID: id, Name: id, Action: api.ScheduleActionMessage, Enabled: true, Spec: "every 1h0m0s", TZ: "UTC", Catchup: "skip", ThreadID: "t1", CreatedAtUnix: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.InsertSchedule(api.Schedule{ID: "sp", Name: "spawner", Action: api.ScheduleActionSpawn, Enabled: true, Spec: "every 1h0m0s", TZ: "UTC", Catchup: "skip", Agent: "pi", Cwd: "/tmp", CreatedAtUnix: 1}); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 5; i++ {
		if err := st.InsertScheduleRun(api.ScheduleRun{ScheduleID: "s1", FiredAtUnix: 100 + i, Outcome: "fired"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.InsertScheduleRun(api.ScheduleRun{ScheduleID: "sp", FiredAtUnix: 200, ThreadID: "spawned-1", Outcome: "fired"}); err != nil {
		t.Fatal(err)
	}
	runs, _ := st.ListScheduleRuns("s1", 2)
	if len(runs) != 2 || runs[0].FiredAtUnix != 105 {
		t.Fatalf("newest-first limit: %+v", runs)
	}
	open, _ := st.OpenScheduleRuns()
	if len(open) != 1 || open[0].ThreadID != "spawned-1" {
		t.Fatalf("open runs: %+v", open)
	}
	if err := st.UpdateScheduleRun(api.ScheduleRun{ScheduleID: "sp", FiredAtUnix: 200, ThreadID: "spawned-1", Outcome: "fired", Detail: "archived", EndedAtUnix: 300}); err != nil {
		t.Fatal(err)
	}
	if open, _ = st.OpenScheduleRuns(); len(open) != 0 {
		t.Fatalf("ended run still open: %+v", open)
	}
	if err := st.PruneScheduleRuns("s1", 3); err != nil {
		t.Fatal(err)
	}
	if runs, _ = st.ListScheduleRuns("s1", 0); len(runs) != 3 || runs[2].FiredAtUnix != 103 {
		t.Fatalf("prune kept the wrong runs: %+v", runs)
	}
	// Deleting the thread cascades its message schedules (and their runs) in
	// the same transaction; the spawn schedule is untouched.
	n, err := st.DeleteThreadCascade("t1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("cascaded %d schedules, want 2", n)
	}
	all, _ := st.ListSchedules()
	if len(all) != 1 || all[0].ID != "sp" {
		t.Fatalf("after cascade: %+v", all)
	}
	if runs, _ = st.ListScheduleRuns("s1", 0); len(runs) != 0 {
		t.Fatalf("cascaded schedule's runs survived: %+v", runs)
	}
}
