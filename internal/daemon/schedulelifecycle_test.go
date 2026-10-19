package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

// lifecycleFixture: a daemon with one agent thread and a helper per endpoint.
// No HTTP server is needed — the handlers are called directly, which is what
// the lifecycle lives in.
type lifecycleFixture struct {
	t  *testing.T
	d  *Daemon
	th string
}

func newLifecycleFixture(t *testing.T, sock string) *lifecycleFixture {
	t.Helper()
	d, st := bareSchedulerDaemon(t, sock)
	th := "11111111-1111-4111-8111-111111111111"
	if err := st.InsertThread(api.Thread{ID: th, Machine: "test", SessionName: "target", AgentKind: "claude", Cwd: "/tmp"}); err != nil {
		t.Fatalf("insert thread: %v", err)
	}
	return &lifecycleFixture{t: t, d: d, th: th}
}

func (f *lifecycleFixture) post(path string, body any, handler func(http.ResponseWriter, *http.Request), out any) int {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw))))
	if out != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			f.t.Fatalf("decode %s: %v (%s)", path, err, rec.Body.String())
		}
	}
	return rec.Code
}

// create makes a message schedule and returns it.
func (f *lifecycleFixture) create(name, spec string, rules api.ScheduleRules) api.Schedule {
	f.t.Helper()
	var out api.ScheduleResponse
	code := f.post("/v1/schedules", api.CreateScheduleRequest{
		Name: name, Action: api.ScheduleActionMessage, Spec: spec, TZ: "UTC",
		ThreadID: f.th, Text: "x", Rules: rules,
	}, f.d.handleScheduleCreate, &out)
	if code != http.StatusOK {
		f.t.Fatalf("create %s: HTTP %d", name, code)
	}
	return out.Schedule
}

// setState calls the update endpoint and returns the record plus the status.
func (f *lifecycleFixture) setState(id, state string) (api.Schedule, int, string) {
	f.t.Helper()
	raw, _ := json.Marshal(api.UpdateScheduleRequest{ID: id, State: &state})
	rec := httptest.NewRecorder()
	f.d.handleScheduleUpdate(rec, httptest.NewRequest(http.MethodPost, "/v1/schedules/update", strings.NewReader(string(raw))))
	var out api.ScheduleResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			f.t.Fatal(err)
		}
	}
	return out.Schedule, rec.Code, rec.Body.String()
}

func (f *lifecycleFixture) list(states string) api.SchedulesResponse {
	f.t.Helper()
	rec := httptest.NewRecorder()
	f.d.handleScheduleList(rec, httptest.NewRequest(http.MethodGet, "/v1/schedules?states="+states, nil))
	if rec.Code != http.StatusOK {
		f.t.Fatalf("list: HTTP %d %s", rec.Code, rec.Body.String())
	}
	var out api.SchedulesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

// expireOneShot rewrites a record to the shape a fired one-shot leaves behind:
// an `at` instant in the past. Written through the store so it is exactly what
// the scheduler would have persisted.
func (f *lifecycleFixture) expireOneShot(id, state, reason string) {
	f.t.Helper()
	sc, err := f.d.store.GetSchedule(id)
	if err != nil {
		f.t.Fatal(err)
	}
	sc.Spec = "at " + time.Now().Add(-48*time.Hour).Format(time.RFC3339)
	sc.State, sc.StateReason, sc.StateAtUnix = state, reason, time.Now().Add(-48*time.Hour).Unix()
	sc.NextFireUnix = time.Now().Add(-48 * time.Hour).Unix()
	if err := f.d.store.UpdateSchedule(sc); err != nil {
		f.t.Fatal(err)
	}
}

// TestScheduleStateWritesAreRestricted: `spent` and `broken` are the daemon's
// verdicts. A caller that could declare a schedule finished or broken could
// also silence the breaker, so both are refused on a write and the error says
// which states ARE settable.
func TestScheduleStateWritesAreRestricted(t *testing.T) {
	f := newLifecycleFixture(t, "seshsched-restrict")
	sc := f.create("nightly", "cron 0 9 * * *", api.ScheduleRules{})
	for _, state := range []string{api.ScheduleSpent, api.ScheduleBroken} {
		_, code, body := f.setState(sc.ID, state)
		if code != http.StatusBadRequest || !strings.Contains(body, "cannot be set") {
			t.Fatalf("setting %s: HTTP %d %s", state, code, body)
		}
	}
	if _, code, body := f.setState(sc.ID, "disabled"); code != http.StatusBadRequest || !strings.Contains(body, "unknown state") {
		t.Fatalf("unknown state: HTTP %d %s", code, body)
	}
	// The record is untouched by any of the refusals.
	if got, _ := f.d.store.GetSchedule(sc.ID); got.State != api.ScheduleActive {
		t.Fatalf("a refused write changed the record: %+v", got)
	}
	for _, state := range []string{api.SchedulePaused, api.ScheduleArchived, api.ScheduleActive} {
		got, code, body := f.setState(sc.ID, state)
		if code != http.StatusOK || got.State != state {
			t.Fatalf("setting %s: HTTP %d %s (%+v)", state, code, body, got)
		}
	}
}

// TestExpiredScheduleIsWritable is the regression for the update bug: every
// write re-validated the clock through the create path, so a record whose
// instant had passed could not be renamed, paused or archived — it 400'd with
// "never fires" and the only remaining verb was remove. ~60 records on Lukas's
// rig were stuck in exactly that state.
func TestExpiredScheduleIsWritable(t *testing.T) {
	f := newLifecycleFixture(t, "seshsched-expired")
	sc := f.create("backstop", "cron 0 9 * * *", api.ScheduleRules{})
	f.expireOneShot(sc.ID, api.SchedulePaused, "")

	// Archiving a record with an empty clock must work: the clock is not changing.
	got, code, body := f.setState(sc.ID, api.ScheduleArchived)
	if code != http.StatusOK {
		t.Fatalf("archive an expired record: HTTP %d %s", code, body)
	}
	if got.State != api.ScheduleArchived {
		t.Fatalf("want archived, got %+v", got)
	}
	// A rename, likewise.
	name := "backstop-old"
	raw, _ := json.Marshal(api.UpdateScheduleRequest{ID: sc.ID, Name: &name})
	rec := httptest.NewRecorder()
	f.d.handleScheduleUpdate(rec, httptest.NewRequest(http.MethodPost, "/v1/schedules/update", strings.NewReader(string(raw))))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename an expired record: HTTP %d %s", rec.Code, rec.Body.String())
	}

	// But putting it BACK on the clock cannot pretend: there is no occurrence
	// left, so it lands in spent with a reason instead of a live-looking record.
	back, code, body := f.setState(sc.ID, api.ScheduleActive)
	if code != http.StatusOK {
		t.Fatalf("resume: HTTP %d %s", code, body)
	}
	if back.State != api.ScheduleSpent || back.StateReason == "" {
		t.Fatalf("resuming an exhausted clock must report spent, got %+v", back)
	}
	if back.NextFireUnix != 0 {
		t.Fatalf("a spent record must carry no next fire: %+v", back)
	}
}

// TestEffectiveStateCorrectsStalePause: a paused record whose window closes
// while it is off is spent in fact. The correction is applied wherever the
// state is read, so a listing never offers a resumable label on something that
// cannot resume.
func TestEffectiveStateCorrectsStalePause(t *testing.T) {
	f := newLifecycleFixture(t, "seshsched-stale")
	sc := f.create("one-shot", "cron 0 9 * * *", api.ScheduleRules{})
	f.expireOneShot(sc.ID, api.SchedulePaused, "")

	if rows := f.list(api.ScheduleSpent).Schedules; len(rows) != 1 || rows[0].State != api.ScheduleSpent {
		t.Fatalf("a stale pause must report spent: %+v", rows)
	}
	// And it is therefore NOT in the default (live) listing.
	live := f.list("")
	for _, row := range live.Schedules {
		if row.ID == sc.ID {
			t.Fatalf("a spent record must not appear in the default listing: %+v", row)
		}
	}
	if live.Counts[api.ScheduleSpent] != 1 {
		t.Fatalf("the census must account for what was held back: %+v", live.Counts)
	}
}

// TestScheduleListDefaultsToLiveStates: the default listing is active, paused
// and broken — a broken schedule is a problem report and must never be hidden.
func TestScheduleListDefaultsToLiveStates(t *testing.T) {
	f := newLifecycleFixture(t, "seshsched-listdefault")
	active := f.create("live", "cron 0 9 * * *", api.ScheduleRules{})
	paused := f.create("parked", "cron 0 10 * * *", api.ScheduleRules{})
	archived := f.create("aside", "cron 0 11 * * *", api.ScheduleRules{})
	broken := f.create("failing", "cron 0 12 * * *", api.ScheduleRules{})
	spent := f.create("done", "cron 0 13 * * *", api.ScheduleRules{})
	if _, code, _ := f.setState(paused.ID, api.SchedulePaused); code != http.StatusOK {
		t.Fatal(code)
	}
	if _, code, _ := f.setState(archived.ID, api.ScheduleArchived); code != http.StatusOK {
		t.Fatal(code)
	}
	// The two daemon verdicts are written the way the scheduler writes them.
	for id, state := range map[string]string{broken.ID: api.ScheduleBroken, spent.ID: api.ScheduleSpent} {
		sc, _ := f.d.store.GetSchedule(id)
		setScheduleState(&sc, state, "because", time.Now())
		if err := f.d.store.UpdateSchedule(sc); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]string{}
	for _, row := range f.list("").Schedules {
		seen[row.ID] = row.State
	}
	if len(seen) != 3 || seen[active.ID] != api.ScheduleActive || seen[paused.ID] != api.SchedulePaused || seen[broken.ID] != api.ScheduleBroken {
		t.Fatalf("default listing must be the live set, got %+v", seen)
	}
	if n := len(f.list(strings.Join(api.ScheduleAllStates, ",")).Schedules); n != 5 {
		t.Fatalf("--all must show every record, got %d", n)
	}
	// An unknown filter word is loud, not silently empty.
	rec := httptest.NewRecorder()
	f.d.handleScheduleList(rec, httptest.NewRequest(http.MethodGet, "/v1/schedules?states=nonsense", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown state filter: HTTP %d %s", rec.Code, rec.Body.String())
	}
}

// TestSchedulePruneRefusesLiveWork: prune is the only path that deletes a
// record, and it will not touch work that is live (`active`) or a problem
// report (`broken`). --dry-run removes nothing.
func TestSchedulePruneRefusesLiveWork(t *testing.T) {
	f := newLifecycleFixture(t, "seshsched-prune")
	keep := f.create("live", "cron 0 9 * * *", api.ScheduleRules{})
	gone := f.create("done", "cron 0 10 * * *", api.ScheduleRules{})
	sc, _ := f.d.store.GetSchedule(gone.ID)
	setScheduleState(&sc, api.ScheduleSpent, "one-shot fired", time.Now().Add(-72*time.Hour))
	if err := f.d.store.UpdateSchedule(sc); err != nil {
		t.Fatal(err)
	}

	for _, state := range []string{api.ScheduleActive, api.ScheduleBroken} {
		var out api.PruneSchedulesResponse
		if code := f.post("/v1/schedules/prune", api.PruneSchedulesRequest{States: []string{state}}, f.d.handleSchedulePrune, &out); code != http.StatusBadRequest {
			t.Fatalf("prune %s must be refused, got HTTP %d", state, code)
		}
	}

	var dry api.PruneSchedulesResponse
	if code := f.post("/v1/schedules/prune", api.PruneSchedulesRequest{DryRun: true}, f.d.handleSchedulePrune, &dry); code != http.StatusOK {
		t.Fatalf("dry run: HTTP %d", code)
	}
	if len(dry.Removed) != 1 || dry.Removed[0].ID != gone.ID || !dry.DryRun {
		t.Fatalf("dry run must name the spent record: %+v", dry)
	}
	if _, err := f.d.store.GetSchedule(gone.ID); err != nil {
		t.Fatalf("a dry run must delete nothing: %v", err)
	}

	// An age bound excludes a record that entered its state after the instant.
	var bounded api.PruneSchedulesResponse
	if code := f.post("/v1/schedules/prune", api.PruneSchedulesRequest{
		BeforeUnix: time.Now().Add(-96 * time.Hour).Unix(),
	}, f.d.handleSchedulePrune, &bounded); code != http.StatusOK {
		t.Fatalf("bounded prune: HTTP %d", code)
	}
	if len(bounded.Removed) != 0 {
		t.Fatalf("--before must exclude the newer record: %+v", bounded)
	}

	var out api.PruneSchedulesResponse
	if code := f.post("/v1/schedules/prune", api.PruneSchedulesRequest{}, f.d.handleSchedulePrune, &out); code != http.StatusOK {
		t.Fatalf("prune: HTTP %d", code)
	}
	if len(out.Removed) != 1 || out.Removed[0].ID != gone.ID {
		t.Fatalf("want the spent record removed: %+v", out)
	}
	if _, err := f.d.store.GetSchedule(gone.ID); err == nil {
		t.Fatal("the spent record is still there")
	}
	if _, err := f.d.store.GetSchedule(keep.ID); err != nil {
		t.Fatalf("the active record must survive: %v", err)
	}
}

// TestThreadArchiveCascadesSchedules: archiving a thread archives the ACTIVE
// message schedules aimed at it, and un-archiving puts exactly those back on
// the clock. Without the cascade a parked thread's schedules went on waking the
// scheduler to write `skipped: archived` at every occurrence, and went on
// counting as pending work against it.
func TestThreadArchiveCascadesSchedules(t *testing.T) {
	f := newLifecycleFixture(t, "seshsched-cascade")
	plain := f.create("plain", "cron 0 9 * * *", api.ScheduleRules{})
	// --allow-archived says "fire into this thread even when it is parked", so
	// the cascade must leave it alone: the archive is not new information to it.
	allowed := f.create("allowed", "cron 0 10 * * *", api.ScheduleRules{AllowArchived: true})
	parked := f.create("parked", "cron 0 11 * * *", api.ScheduleRules{})
	if _, code, _ := f.setState(parked.ID, api.SchedulePaused); code != http.StatusOK {
		t.Fatal(code)
	}
	byHand := f.create("byhand", "cron 0 12 * * *", api.ScheduleRules{})
	if _, code, _ := f.setState(byHand.ID, api.ScheduleArchived); code != http.StatusOK {
		t.Fatal(code)
	}

	var arch api.ArchiveThreadResponse
	if code := f.post("/v1/threads/archive", api.ArchiveThreadRequest{ID: f.th, Archived: true}, f.d.handleThreadArchive, &arch); code != http.StatusOK {
		t.Fatalf("archive thread: HTTP %d", code)
	}
	if arch.SchedulesMoved != 1 {
		t.Fatalf("only the plain active schedule should move, got %d", arch.SchedulesMoved)
	}
	assertState := func(id, state, reason string) {
		t.Helper()
		got, err := f.d.store.GetSchedule(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != state || (reason != "" && got.StateReason != reason) {
			t.Fatalf("%s: want %s/%q, got %s/%q", got.Name, state, reason, got.State, got.StateReason)
		}
	}
	assertState(plain.ID, api.ScheduleArchived, scheduleThreadArchivedReason)
	assertState(allowed.ID, api.ScheduleActive, "")
	assertState(parked.ID, api.SchedulePaused, "")
	assertState(byHand.ID, api.ScheduleArchived, "")

	// The scheduler's own view: an archived schedule is simply not loaded.
	active, err := f.d.store.ListSchedulesActive()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != allowed.ID {
		t.Fatalf("only the allow-archived schedule may still fire: %+v", active)
	}

	var un api.ArchiveThreadResponse
	if code := f.post("/v1/threads/archive", api.ArchiveThreadRequest{ID: f.th, Archived: false}, f.d.handleThreadArchive, &un); code != http.StatusOK {
		t.Fatalf("unarchive thread: HTTP %d", code)
	}
	if un.SchedulesMoved != 1 {
		t.Fatalf("un-archiving must restore exactly what the cascade took, got %d", un.SchedulesMoved)
	}
	assertState(plain.ID, api.ScheduleActive, "")
	// Un-archiving the thread must NOT revive a record archived by hand, nor
	// un-pause one that was already parked when the thread went away.
	assertState(byHand.ID, api.ScheduleArchived, "")
	assertState(parked.ID, api.SchedulePaused, "")
	back, _ := f.d.store.GetSchedule(plain.ID)
	if back.NextFireUnix <= time.Now().Unix() {
		t.Fatalf("a restored schedule fires in the future, not its backlog: %+v", back)
	}
}

// TestThreadArchiveCascadeSpendsClosedWindow: a one-shot whose instant passes
// while its thread is parked cannot come back — un-archiving reports it spent
// rather than putting a record that can never fire back on the clock.
func TestThreadArchiveCascadeSpendsClosedWindow(t *testing.T) {
	f := newLifecycleFixture(t, "seshsched-cascade-spent")
	shot := f.create("shot", fmt.Sprintf("at %s", time.Now().Add(90*time.Minute).Format(time.RFC3339)), api.ScheduleRules{})
	var arch api.ArchiveThreadResponse
	if code := f.post("/v1/threads/archive", api.ArchiveThreadRequest{ID: f.th, Archived: true}, f.d.handleThreadArchive, &arch); code != http.StatusOK {
		t.Fatalf("archive: HTTP %d", code)
	}
	if arch.SchedulesMoved != 1 {
		t.Fatalf("moved %d", arch.SchedulesMoved)
	}
	// Its instant passes while the thread is parked.
	f.expireOneShot(shot.ID, api.ScheduleArchived, scheduleThreadArchivedReason)

	var un api.ArchiveThreadResponse
	if code := f.post("/v1/threads/archive", api.ArchiveThreadRequest{ID: f.th, Archived: false}, f.d.handleThreadArchive, &un); code != http.StatusOK {
		t.Fatalf("unarchive: HTTP %d", code)
	}
	if un.SchedulesMoved != 0 {
		t.Fatalf("a closed window is not restored: %d", un.SchedulesMoved)
	}
	got, _ := f.d.store.GetSchedule(shot.ID)
	if got.State != api.ScheduleSpent || !strings.Contains(got.StateReason, "archived") {
		t.Fatalf("want spent with a reason naming the archive, got %s/%q", got.State, got.StateReason)
	}
}
