package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestScheduleStateQueries: the lifecycle state is the firing gate, so the
// queries that feed the scheduler and the TUI must select on it — not on row
// count. Active is the only state that fires or counts against a thread.
func TestScheduleStateQueries(t *testing.T) {
	st := openTestStore(t)
	seed := func(id, state, reason string, thread string, next int64) {
		t.Helper()
		sc := api.Schedule{
			ID: id, Name: id, Action: api.ScheduleActionMessage, State: state, StateReason: reason,
			StateAtUnix: 100, Spec: "every 1h0m0s", TZ: "UTC", Catchup: api.CatchupSkip,
			ThreadID: thread, Text: "x", NextFireUnix: next, CreatedAtUnix: 1,
		}
		if err := st.InsertSchedule(sc); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	seed("a1", api.ScheduleActive, "", "t1", 5000)
	seed("a2", api.ScheduleActive, "", "t1", 3000)
	seed("p1", api.SchedulePaused, "", "t1", 9000)
	seed("s1", api.ScheduleSpent, "one-shot fired", "t1", 0)
	seed("b1", api.ScheduleBroken, "3 consecutive failures", "t2", 7000)
	seed("x1", api.ScheduleArchived, "thread archived", "t2", 8000)

	active, err := st.ListSchedulesActive()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("the scheduler must load only active schedules, got %d: %+v", len(active), active)
	}
	for _, sc := range active {
		if sc.State != api.ScheduleActive {
			t.Fatalf("%s is %s", sc.ID, sc.State)
		}
	}
	if all, err := st.ListSchedules(); err != nil || len(all) != 6 {
		t.Fatalf("ListSchedules must still return every record: %d %v", len(all), err)
	}

	counts, err := st.CountSchedulesByState()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{api.ScheduleActive: 2, api.SchedulePaused: 1, api.ScheduleSpent: 1, api.ScheduleBroken: 1, api.ScheduleArchived: 1}
	for state, n := range want {
		if counts[state] != n {
			t.Fatalf("census %s: want %d, got %d (%v)", state, n, counts[state], counts)
		}
	}

	// The thread digest (the TUI's SCHED column) counts ACTIVE only: a parked,
	// finished or set-aside schedule is not pending work against a thread.
	digests, err := st.ScheduleDigests()
	if err != nil {
		t.Fatal(err)
	}
	if d := digests["t1"]; d.Count != 2 || d.NextFireUnix != 3000 {
		t.Fatalf("t1 digest: want 2 active and earliest next 3000, got %+v", d)
	}
	if d, ok := digests["t2"]; ok {
		t.Fatalf("t2 has only a broken and an archived schedule — it must not appear: %+v", d)
	}

	// SetSchedulesState moves a set in one transaction and can rewrite next_fire
	// (the cascade's resume), leaving records outside the set untouched.
	if err := st.SetSchedulesState([]string{"a1", "p1"}, api.ScheduleArchived, "thread archived", 777, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "p1"} {
		got, _ := st.GetSchedule(id)
		if got.State != api.ScheduleArchived || got.StateReason != "thread archived" || got.StateAtUnix != 777 {
			t.Fatalf("%s: %+v", id, got)
		}
	}
	if got, _ := st.GetSchedule("a2"); got.State != api.ScheduleActive {
		t.Fatalf("a2 must be untouched: %+v", got)
	}
	if err := st.SetSchedulesState([]string{"a1"}, api.ScheduleActive, "", 888, map[string]int64{"a1": 4242}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSchedule("a1"); got.State != api.ScheduleActive || got.NextFireUnix != 4242 || got.StateReason != "" {
		t.Fatalf("a1 back on the clock: %+v", got)
	}
	// An empty id set is a no-op, not an error (a thread with no schedules).
	if err := st.SetSchedulesState(nil, api.ScheduleArchived, "", 1, nil); err != nil {
		t.Fatalf("empty set: %v", err)
	}
}

// TestScheduleUnknownStateIsLoud: a state outside the vocabulary means the
// column was written by something that is not this code. Reading it must fail
// loudly rather than coercing it into a plausible default — a coerced "active"
// would start firing a record nobody put on the clock.
func TestScheduleUnknownStateIsLoud(t *testing.T) {
	st := openTestStore(t)
	if err := st.InsertSchedule(api.Schedule{
		ID: "s1", Name: "s1", Action: api.ScheduleActionMessage, State: api.ScheduleActive,
		Spec: "every 1h0m0s", TZ: "UTC", Catchup: api.CatchupSkip, ThreadID: "t1", Text: "x", CreatedAtUnix: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE schedules SET state = 'disabled' WHERE id = 's1'`); err != nil {
		t.Fatal(err)
	}
	_, err := st.GetSchedule("s1")
	if err == nil || !strings.Contains(err.Error(), "unknown state") {
		t.Fatalf("want a loud unknown-state error, got %v", err)
	}
}

// TestMigrationScheduleStateBackfill: migration 27 DERIVES the lifecycle state
// from the pre-54 (enabled, disabled_reason) pair, and the derivation is the
// whole point — the hard case is a PAUSED one-shot whose instant has already
// passed, which is spent in fact however it is labelled. On the rig this was
// written for, ~60 of 68 "paused" records were in exactly that position, every
// write to them failing, so copying the old label forward would have preserved
// the bug under a new name.
func TestMigrationScheduleStateBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sesh.db")
	// Locate the lifecycle migration by content, so appending later ones keeps
	// this test staging the schema that came just before it.
	pre := -1
	for i, m := range migrations {
		if strings.Contains(m, "ADD COLUMN state TEXT NOT NULL DEFAULT 'active'") {
			pre = i
			break
		}
	}
	if pre < 0 {
		t.Fatal("the schedule-state migration was not found")
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)", path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meta (id, version) VALUES (1, 0) ON CONFLICT(id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < pre; i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			t.Fatalf("apply pre-migration %d: %v", i+1, err)
		}
	}

	// Era-53 rows: the exact shapes found in the live store.
	past, future := int64(1000000000), int64(4000000000)
	seed := func(id string, enabled int, reason, spec string, nextFire, lastFire int64) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO schedules
			(id, name, action, enabled, disabled_reason, spec, tz, catchup, thread_id, text, next_fire, last_fire, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, id, "message", enabled, reason, spec, "UTC", "skip", "t1", "x", nextFire, lastFire, 500); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("live", 1, "", "cron 0 9 * * *", future, 0)
	seed("pause-cron", 0, "paused", "cron 0 9 * * *", future, 0)
	seed("pause-future-shot", 0, "paused", "at 2096-01-01T00:00:00Z", future, 0)
	seed("pause-past-shot", 0, "paused", "at 2001-09-09T01:46:40Z", past, 0)
	seed("fired", 0, "one-shot fired", "at 2001-09-09T01:46:40Z", 0, past)
	seed("maxed", 0, "max_fires 1 reached", "cron 0 9 * * *", future, past)
	seed("nofuture", 0, "no future occurrence", "cron 0 9 * * *", 0, past)
	seed("breaker", 0, "3 consecutive failures — last: boom", "cron 0 9 * * *", future, past)

	if _, err := db.Exec(migrations[pre]); err != nil {
		t.Fatalf("apply the lifecycle migration: %v", err)
	}

	type row struct{ state, reason string }
	got := map[string]row{}
	rows, err := db.Query(`SELECT id, state, state_reason, state_at FROM schedules`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, state, reason string
		var at int64
		if err := rows.Scan(&id, &state, &reason, &at); err != nil {
			t.Fatal(err)
		}
		// state_at is the transition instant: the last fire when there was one,
		// else creation — never 0, which --before would read as the epoch.
		if at == 0 {
			t.Fatalf("%s: state_at was not backfilled", id)
		}
		got[id] = row{state, reason}
	}
	for id, want := range map[string]row{
		"live":              {api.ScheduleActive, ""},
		"pause-cron":        {api.SchedulePaused, ""},
		"pause-future-shot": {api.SchedulePaused, ""},
		"pause-past-shot":   {api.ScheduleSpent, "paused, and its one-shot instant passed"},
		"fired":             {api.ScheduleSpent, "one-shot fired"},
		"maxed":             {api.ScheduleSpent, "max_fires 1 reached"},
		"nofuture":          {api.ScheduleSpent, "no future occurrence"},
		"breaker":           {api.ScheduleBroken, "3 consecutive failures — last: boom"},
	} {
		if got[id] != want {
			t.Errorf("%s: want %+v, got %+v", id, want, got[id])
		}
	}

	// The superseded columns stay readable for a rollback to a pre-54 binary.
	var enabled int
	if err := db.QueryRow(`SELECT enabled FROM schedules WHERE id = 'live'`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("enabled mirror: %d %v", enabled, err)
	}
}
