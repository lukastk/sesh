package daemon

// The /v1/schedules endpoint family (_dev/SCHEDULING.md §8.2). Validation is
// loud and total at creation: an unknown guard word, a spec that never fires,
// a message target that is not this machine's thread, a spawn cwd that is not
// absolute — none of it gets stored to fail later, unattended. The zone is
// resolved here (the OWNER's local zone when the request names none) and
// recorded, so what the record fires at is what the response prints back.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
	"github.com/lukastk/sesh/internal/schedule"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesSchedules(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/schedules", d.handleScheduleCreate)
	mux.HandleFunc("GET /v1/schedules", d.handleScheduleList)
	mux.HandleFunc("GET /v1/schedules/get", d.handleScheduleGet)
	mux.HandleFunc("POST /v1/schedules/update", d.handleScheduleUpdate)
	mux.HandleFunc("POST /v1/schedules/remove", d.handleScheduleRemove)
	mux.HandleFunc("POST /v1/schedules/prune", d.handleSchedulePrune)
	mux.HandleFunc("POST /v1/schedules/run-now", d.handleScheduleRunNow)
	mux.HandleFunc("GET /v1/schedules/runs", d.handleScheduleRuns)
}

// localZoneName is the IANA name of this machine's zone: $TZ, else the
// /etc/localtime link target, else "Local" (which time.LoadLocation resolves
// to the system zone — correct on the one machine that ever runs the record,
// just less informative in a listing).
func localZoneName() string {
	if tz := os.Getenv("TZ"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			name := target[i+len("zoneinfo/"):]
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}
	return "Local"
}

// validGuard reports whether a --if word is in the closed vocabulary.
func validGuard(word string) bool {
	for _, w := range validGuardWords {
		if w == word {
			return true
		}
	}
	return false
}

// validateRules checks the rules for an action; loud on any unknown value.
func validateRules(action string, r api.ScheduleRules) error {
	switch r.WhenHeadless {
	case "", "turn", "revive", "skip":
	default:
		return fmt.Errorf("when_headless %q: want turn, revive or skip", r.WhenHeadless)
	}
	switch r.WhenBusy {
	case "", "skip", "send":
	default:
		return fmt.Errorf("when_busy %q: want skip or send", r.WhenBusy)
	}
	for _, w := range r.If {
		if !validGuard(w) {
			return fmt.Errorf("--if %q: not a guard word (want one of %s)", w, strings.Join(validGuardWords, ", "))
		}
	}
	if r.IdleForS != nil && *r.IdleForS < 0 {
		return errors.New("idle_for must not be negative")
	}
	if r.RespectTypingMs != nil && *r.RespectTypingMs < 0 {
		return errors.New("respect_typing must not be negative")
	}
	switch r.IfPrevious {
	case "", "skip", "spawn-anyway", "stop-previous":
	default:
		return fmt.Errorf("if_previous %q: want skip, spawn-anyway or stop-previous", r.IfPrevious)
	}
	switch r.OnTurnEnd {
	case "", "keep", "stop", "archive", "stop+archive", "delete":
	default:
		return fmt.Errorf("on_turn_end %q: want keep, stop, archive, stop+archive or delete", r.OnTurnEnd)
	}
	if r.MaxRuntimeS < 0 {
		return errors.New("max_runtime must not be negative")
	}
	if action == api.ScheduleActionMessage && (r.IfPrevious != "" || r.OnTurnEnd != "" || r.MaxRuntimeS != 0 || r.FlagOnEnd) {
		return errors.New("if_previous/on_turn_end/max_runtime/flag_on_end are spawn rules; a message schedule takes none of them")
	}
	if action == api.ScheduleActionSpawn && (r.WhenHeadless != "" || r.WhenBusy != "" || len(r.If) > 0 || r.IdleForS != nil || r.IgnoreHold || r.AllowArchived) {
		return errors.New("when_headless/when_busy/if/idle_for/ignore_hold/allow_archived are message rules; a spawn schedule takes none of them")
	}
	return nil
}

// setScheduleState stamps a lifecycle transition on a record in memory (§9.6).
// Every state write goes through it so state, reason and the instant of the
// transition can never drift apart.
func setScheduleState(sc *api.Schedule, state, reason string, at time.Time) {
	sc.State, sc.StateReason, sc.StateAtUnix = state, reason, at.Unix()
}

// effectiveState corrects a stored state that the clock has overtaken: a
// `paused` record whose window closed while it was off is `spent`, because
// resuming it cannot produce an occurrence. It is a pure function of the
// record and `now`, applied wherever a state is READ or written, so a listing
// never shows a resumable label on a record that cannot resume — the exact lie
// that left ~60 rows on Lukas's rig claiming `paused` while every write to
// them failed. Returns the state unchanged for every other case.
func (d *Daemon) effectiveState(sc api.Schedule, now time.Time) (string, string) {
	if sc.State != api.SchedulePaused {
		return sc.State, sc.StateReason
	}
	spec, loc, err := d.scheduleSpec(sc)
	if err != nil {
		// A spec that no longer parses is a broken record, not a finished one.
		return api.ScheduleBroken, "spec no longer parses: " + err.Error()
	}
	if next := spec.Next(now.In(loc)); next.IsZero() || (sc.NotAfterUnix > 0 && next.Unix() > sc.NotAfterUnix) {
		return api.ScheduleSpent, "paused, and its window passed"
	}
	return sc.State, sc.StateReason
}

// withEffectiveState returns the record as it should be REPORTED.
func (d *Daemon) withEffectiveState(sc api.Schedule, now time.Time) api.Schedule {
	sc.State, sc.StateReason = d.effectiveState(sc, now)
	return sc
}

// scheduleSpec parses a record's clock in its recorded zone, anchored the way
// the scheduler anchors it (creation, or not_before when set).
func (d *Daemon) scheduleSpec(sc api.Schedule) (schedule.Spec, *time.Location, error) {
	loc, err := time.LoadLocation(sc.TZ)
	if err != nil {
		return schedule.Spec{}, nil, fmt.Errorf("tz %q: %w", sc.TZ, err)
	}
	anchor := time.Unix(sc.CreatedAtUnix, 0)
	if sc.NotBeforeUnix > 0 {
		anchor = time.Unix(sc.NotBeforeUnix, 0)
	}
	spec, err := schedule.Parse(sc.Spec, loc, anchor.In(loc))
	return spec, loc, err
}

// buildSchedule validates a create request into a record with its first
// next_fire computed at `now`. selfID is the record being EDITED (excluded
// from the name-collision check), "" on create. requireFuture demands an
// occurrence after now: true on create and whenever a record is being put back
// ON the clock, false when re-validating an edit that leaves the clock alone —
// otherwise renaming, pausing or archiving a record whose instant has passed is
// impossible, which is how ~60 records on Lukas's rig became unreachable.
func (d *Daemon) buildSchedule(req api.CreateScheduleRequest, now time.Time, selfID string, requireFuture bool) (api.Schedule, error) {
	sc := api.Schedule{
		ID: uuid.NewString(), Machine: d.cfg.Machine, Name: strings.TrimSpace(req.Name), Action: req.Action,
		State: api.ScheduleActive, StateAtUnix: now.Unix(),
		Spec: strings.TrimSpace(req.Spec), TZ: req.TZ, Catchup: req.Catchup,
		NotBeforeUnix: req.NotBeforeUnix, NotAfterUnix: req.NotAfterUnix, MaxFires: req.MaxFires,
		Rules: req.Rules, CreatedAtUnix: now.Unix(),
	}
	switch sc.Action {
	case api.ScheduleActionMessage, api.ScheduleActionSpawn:
	default:
		return sc, fmt.Errorf("schedule: action %q: want message or spawn", sc.Action)
	}
	if sc.Name == "" {
		sc.Name = sc.Action + "-" + sc.ID[:8]
	}
	if strings.ContainsAny(sc.Name, "\t\n") {
		return sc, errors.New("schedule: name must be one line")
	}
	existing, err := d.store.ListSchedules()
	if err != nil {
		return sc, err
	}
	for _, e := range existing {
		if e.ID != selfID && e.Name == sc.Name {
			return sc, fmt.Errorf("schedule: a schedule named %q already exists on this machine (%s)", sc.Name, e.ID[:8])
		}
	}
	switch sc.Catchup {
	case "":
		sc.Catchup = api.CatchupSkip
	case api.CatchupSkip, api.CatchupOnce:
	default:
		return sc, fmt.Errorf("schedule: catchup %q: want skip or once", sc.Catchup)
	}
	if sc.TZ == "" {
		sc.TZ = localZoneName()
	}
	loc, err := time.LoadLocation(sc.TZ)
	if err != nil {
		return sc, fmt.Errorf("schedule: tz %q: %w", sc.TZ, err)
	}
	if sc.MaxFires < 0 || sc.NotBeforeUnix < 0 || sc.NotAfterUnix < 0 {
		return sc, errors.New("schedule: max_fires/not_before/not_after must not be negative")
	}
	if requireFuture && sc.NotAfterUnix > 0 && sc.NotAfterUnix <= now.Unix() {
		return sc, errors.New("schedule: not_after is already in the past")
	}
	if err := validateRules(sc.Action, sc.Rules); err != nil {
		return sc, fmt.Errorf("schedule: %w", err)
	}
	anchor := now
	if sc.NotBeforeUnix > 0 {
		anchor = time.Unix(sc.NotBeforeUnix, 0)
	}
	spec, err := schedule.Parse(sc.Spec, loc, anchor.In(loc))
	if err != nil {
		return sc, err
	}
	sc.Spec = spec.Raw
	from := now.In(loc)
	if sc.NotBeforeUnix > 0 && anchor.After(now) {
		from = anchor.In(loc).Add(-time.Second)
	}
	next := spec.Next(from)
	if next.IsZero() {
		if requireFuture {
			return sc, fmt.Errorf("schedule: %q never fires (no occurrence after %s)", sc.Spec, from.Format(time.RFC3339))
		}
		sc.NextFireUnix = 0
	} else if sc.NotAfterUnix > 0 && next.Unix() > sc.NotAfterUnix {
		if requireFuture {
			return sc, fmt.Errorf("schedule: the first occurrence (%s) is after not_after", next.Format(time.RFC3339))
		}
		sc.NextFireUnix = 0
	} else {
		sc.NextFireUnix = next.Unix()
	}

	switch sc.Action {
	case api.ScheduleActionMessage:
		sc.ThreadID, sc.Text = req.ThreadID, req.Text
		if sc.ThreadID == "" || strings.TrimSpace(sc.Text) == "" {
			return sc, errors.New("schedule message: thread_id and text are required")
		}
		th, err := d.store.GetThread(sc.ThreadID)
		if err != nil {
			if errors.Is(err, store.ErrThreadNotFound) {
				return sc, fmt.Errorf("schedule message: thread %s is not on this machine — a message schedule lives on its target's owner (create it with --machine <owner>)", sc.ThreadID)
			}
			return sc, err
		}
		if api.NonAgentKind(th.AgentKind) {
			return sc, fmt.Errorf("schedule message: %s is a %s node, not an agent thread", th.ID[:8], th.AgentKind)
		}
		if _, err := d.expandPrompt(sc.Text); err != nil {
			return sc, fmt.Errorf("schedule message: %w", err)
		}
	case api.ScheduleActionSpawn:
		sc.Agent, sc.Cwd, sc.Prompt, sc.Model = req.Agent, expandHomeCwd(req.Cwd), req.Prompt, req.Model
		sc.IntoSession, sc.ParentID, sc.NameTemplate = req.IntoSession, req.ParentID, req.NameTemplate
		sc.Headless = req.Headless == nil || *req.Headless
		if sc.Agent == "" {
			sc.Agent = d.defaults.Agent
		}
		if sc.Agent == "" {
			return sc, errors.New("schedule spawn: agent is required (or set [defaults] agent)")
		}
		kind, err := agents.ParseKind(sc.Agent)
		if err != nil {
			return sc, fmt.Errorf("schedule spawn: %w", err)
		}
		if !filepath.IsAbs(sc.Cwd) {
			return sc, fmt.Errorf("schedule spawn: cwd %q must be absolute (or ~-relative)", req.Cwd)
		}
		if strings.TrimSpace(sc.Prompt) == "" {
			return sc, errors.New("schedule spawn: prompt is required")
		}
		if _, err := d.expandPrompt(sc.Prompt); err != nil {
			return sc, fmt.Errorf("schedule spawn: %w", err)
		}
		mode, err := d.resolveSpawnMode(kind, req.SpawnMode)
		if err != nil {
			return sc, fmt.Errorf("schedule spawn: %w", err)
		}
		if mode == "" {
			mode = "default" // the agent's own permission defaults — recorded as such, never blank
		}
		sc.SpawnMode = mode
		if sc.ParentID != "" {
			if _, err := d.store.GetThread(sc.ParentID); err != nil {
				return sc, fmt.Errorf("schedule spawn: parent %s: %w", sc.ParentID, err)
			}
		}
		if sc.IntoSession != "" && sc.Headless {
			return sc, errors.New("schedule spawn: into_session needs a headed spawn (headless = false)")
		}
	}
	return sc, nil
}

func (d *Daemon) handleScheduleCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateScheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc, err := d.buildSchedule(req, time.Now(), "", true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := d.store.InsertSchedule(sc); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ScheduleResponse{Schema: api.SchemaVersion, Schedule: sc})
}

// resolveScheduleRef resolves a full id or a unique prefix against this
// machine's schedules; loud on unknown or ambiguous.
func (d *Daemon) resolveScheduleRef(ref string) (api.Schedule, error) {
	if ref == "" {
		return api.Schedule{}, errors.New("schedule: id is required")
	}
	if sc, err := d.store.GetSchedule(ref); err == nil {
		return sc, nil
	}
	all, err := d.store.ListSchedules()
	if err != nil {
		return api.Schedule{}, err
	}
	var hits []api.Schedule
	for _, sc := range all {
		if strings.HasPrefix(sc.ID, ref) || sc.Name == ref {
			hits = append(hits, sc)
		}
	}
	switch len(hits) {
	case 0:
		return api.Schedule{}, fmt.Errorf("%w: %s", store.ErrScheduleNotFound, ref)
	case 1:
		return hits[0], nil
	default:
		names := make([]string, len(hits))
		for i, h := range hits {
			names[i] = h.ID[:8] + " " + h.Name
		}
		return api.Schedule{}, fmt.Errorf("schedule: %q is ambiguous: %s", ref, strings.Join(names, "; "))
	}
}

func (d *Daemon) scheduleErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrScheduleNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func (d *Daemon) handleScheduleList(w http.ResponseWriter, r *http.Request) {
	all, err := d.store.ListSchedules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// `states` is the explicit filter (comma-separated); absent = the live set
	// (active, paused, broken). A finished or set-aside schedule is inert by
	// construction and would otherwise bury the handful that still matter.
	wanted := map[string]bool{}
	for _, s := range strings.Split(r.URL.Query().Get("states"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			if !api.ScheduleStateKnown(s) {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("schedule list: unknown state %q (want %s)", s, strings.Join(api.ScheduleAllStates, ", ")))
				return
			}
			wanted[s] = true
		}
	}
	if len(wanted) == 0 {
		for _, s := range api.ScheduleLiveStates {
			wanted[s] = true
		}
	}
	now := time.Now()
	thread := r.URL.Query().Get("thread")
	out := api.SchedulesResponse{Schema: api.SchemaVersion, Schedules: []api.Schedule{}, Counts: map[string]int{}}
	for _, sc := range all {
		sc = d.withEffectiveState(sc, now)
		sc.Machine = d.cfg.Machine
		if thread != "" && sc.ThreadID != thread && sc.LastRunThreadID != thread {
			continue
		}
		out.Counts[sc.State]++
		if wanted[sc.State] {
			out.Schedules = append(out.Schedules, sc)
		}
	}
	if r.URL.Query().Get("all-machines") == "1" {
		// Re-serialised from the VALIDATED set, never the raw query string: it
		// reaches a peer as an argument inside an ssh command line.
		peerRows, counts, unreachable := d.fanOutSchedules(thread, statesArg(wanted))
		for _, sc := range peerRows {
			if wanted[sc.State] {
				out.Schedules = append(out.Schedules, sc)
			}
		}
		for state, n := range counts {
			out.Counts[state] += n
		}
		out.Unreachable = unreachable
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSchedulePrune deletes records in the inert states — the only path that
// destroys a schedule, and never automatic. `active` is refused (it would
// cancel live work by surprise) and so is `broken` (it would erase a problem
// report); both have to be moved deliberately first.
func (d *Daemon) handleSchedulePrune(w http.ResponseWriter, r *http.Request) {
	var req api.PruneSchedulesRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	states := req.States
	if len(states) == 0 {
		states = []string{api.ScheduleSpent}
	}
	want := map[string]bool{}
	for _, s := range states {
		switch s {
		case api.ScheduleSpent, api.SchedulePaused, api.ScheduleArchived:
			want[s] = true
		case api.ScheduleActive:
			writeError(w, http.StatusBadRequest, "schedule prune: refusing to delete active schedules — pause or archive them first")
			return
		case api.ScheduleBroken:
			writeError(w, http.StatusBadRequest, "schedule prune: refusing to delete broken schedules — they are a problem report; archive one to set it aside")
			return
		default:
			writeError(w, http.StatusBadRequest, fmt.Sprintf("schedule prune: unknown state %q", s))
			return
		}
	}
	all, err := d.store.ListSchedules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	out := api.PruneSchedulesResponse{Schema: api.SchemaVersion, DryRun: req.DryRun, Removed: []api.Schedule{}}
	for _, sc := range all {
		sc = d.withEffectiveState(sc, now)
		sc.Machine = d.cfg.Machine
		if !want[sc.State] {
			continue
		}
		if req.Thread != "" && sc.ThreadID != req.Thread && sc.LastRunThreadID != req.Thread {
			continue
		}
		if req.BeforeUnix > 0 && sc.StateAtUnix >= req.BeforeUnix {
			continue
		}
		out.Removed = append(out.Removed, sc)
	}
	if !req.DryRun {
		for _, sc := range out.Removed {
			if err := d.store.DeleteSchedule(sc.ID); err != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("schedule prune: %s: %v", sc.ID, err))
				return
			}
			log.Printf("scheduler: %s (%s) PRUNED from %s", sc.ID, sc.Name, sc.State)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// archiveSchedulesForThread carries a thread's ACTIVE message schedules into
// `archived` when the thread is archived, and back to `active` when it is not
// (§9.4). An archived thread is a "leave this alone" signal: the per-run guard
// already skips delivery into one, but a schedule left active goes on waking
// the scheduler and writing a `skipped: archived` run row at every occurrence
// for as long as the thread stays parked, and goes on counting as pending work
// against it. Returns how many records moved.
//
// Only `active` records are carried: a paused, spent, broken or hand-archived
// schedule already means something the thread's state must not overwrite. A
// schedule whose rules say --allow-archived is deliberately meant to fire into
// an archived thread, so it is left alone too.
func (d *Daemon) archiveSchedulesForThread(threadID string, archived bool) (int, error) {
	rows, err := d.store.ListSchedulesByThread(threadID)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	var ids []string
	nextFire := map[string]int64{}
	state, reason := api.ScheduleArchived, scheduleThreadArchivedReason
	for _, sc := range rows {
		if archived {
			if sc.State != api.ScheduleActive || sc.Rules.AllowArchived {
				continue
			}
			ids = append(ids, sc.ID)
			continue
		}
		// Un-archiving restores exactly what this cascade took, identified by
		// its reason — never a record archived by hand, and never one that was
		// paused or broken when the thread was parked.
		if sc.State != api.ScheduleArchived || sc.StateReason != scheduleThreadArchivedReason {
			continue
		}
		state, reason = api.ScheduleActive, ""
		// Back onto the clock from NOW, like a resume: no backlog. A window
		// that closed while the thread was parked lands in `spent` instead,
		// which is the truth rather than a record that claims to be live.
		spec, loc, err := d.scheduleSpec(sc)
		if err != nil {
			continue // left archived; a broken spec needs a human, not a cascade
		}
		next := spec.Next(now.In(loc))
		if next.IsZero() || (sc.NotAfterUnix > 0 && next.Unix() > sc.NotAfterUnix) {
			if err := d.store.SetSchedulesState([]string{sc.ID}, api.ScheduleSpent,
				"its window passed while the thread was archived", now.Unix(), map[string]int64{sc.ID: 0}); err != nil {
				return 0, err
			}
			continue
		}
		ids = append(ids, sc.ID)
		nextFire[sc.ID] = next.Unix()
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := d.store.SetSchedulesState(ids, state, reason, now.Unix(), nextFire); err != nil {
		return 0, err
	}
	log.Printf("schedules: thread %s %s: %d message schedule(s) -> %s", threadID[:min(8, len(threadID))],
		map[bool]string{true: "archived", false: "unarchived"}[archived], len(ids), state)
	return len(ids), nil
}

// scheduleThreadArchivedReason marks the records the thread-archive cascade
// moved, so un-archiving restores those and nothing else.
const scheduleThreadArchivedReason = "thread archived"

func (d *Daemon) handleScheduleGet(w http.ResponseWriter, r *http.Request) {
	sc, err := d.resolveScheduleRef(r.URL.Query().Get("id"))
	if err != nil {
		d.scheduleErr(w, err)
		return
	}
	sc = d.withEffectiveState(sc, time.Now())
	sc.Machine = d.cfg.Machine
	writeJSON(w, http.StatusOK, api.ScheduleResponse{Schema: api.SchemaVersion, Schedule: sc})
}

func (d *Daemon) handleScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateScheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc, err := d.resolveScheduleRef(req.ID)
	if err != nil {
		d.scheduleErr(w, err)
		return
	}
	now := time.Now()
	// clockEdited: the USER changed the clock, so a spec with nothing left is a
	// typo and must be loud. clockChanged: the next fire needs recomputing,
	// which a resume also needs — but a resume of an exhausted clock is not an
	// error, it is a `spent` record, and saying so beats refusing the call.
	clockEdited, clockChanged := false, false
	if req.Name != nil {
		sc.Name = strings.TrimSpace(*req.Name)
	}
	if req.Spec != nil {
		sc.Spec, clockEdited, clockChanged = strings.TrimSpace(*req.Spec), true, true
	}
	if req.TZ != nil {
		sc.TZ, clockEdited, clockChanged = *req.TZ, true, true
	}
	if req.Catchup != nil {
		sc.Catchup = *req.Catchup
	}
	if req.NotAfterUnix != nil {
		sc.NotAfterUnix, clockEdited = *req.NotAfterUnix, true
	}
	if req.MaxFires != nil {
		sc.MaxFires = *req.MaxFires
	}
	if req.Text != nil {
		sc.Text = *req.Text
	}
	if req.Prompt != nil {
		sc.Prompt = *req.Prompt
	}
	if req.Rules != nil {
		sc.Rules = *req.Rules
	}
	// A record the clock has already overtaken is reported — and now stored —
	// as what it is, so an edit never carries a stale `paused` forward.
	sc.State, sc.StateReason = d.effectiveState(sc, now)
	if req.State != nil {
		want := *req.State
		if !api.ScheduleStateSettable(want) {
			if api.ScheduleStateKnown(want) {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("schedule: %q is the daemon's own verdict and cannot be set (settable: %s)",
					want, strings.Join(api.ScheduleSettableStates, ", ")))
				return
			}
			writeError(w, http.StatusBadRequest, fmt.Sprintf("schedule: unknown state %q (want %s)", want, strings.Join(api.ScheduleSettableStates, ", ")))
			return
		}
		if want == api.ScheduleActive && sc.State != api.ScheduleActive {
			// Back onto the clock: recompute from now, never a backlog.
			clockChanged = true
		}
		if want != sc.State {
			setScheduleState(&sc, want, "", now)
		}
	}
	// Re-validate the whole record through the create path (same rules, same
	// loudness), keeping identity and state. requireFuture only when the record
	// is going back on the clock or its clock changed: otherwise a rename or an
	// archive of a record whose instant has passed would be impossible.
	check, err := d.buildSchedule(api.CreateScheduleRequest{
		Name: sc.Name, Action: sc.Action, Spec: sc.Spec, TZ: sc.TZ, Catchup: sc.Catchup,
		NotBeforeUnix: sc.NotBeforeUnix, NotAfterUnix: sc.NotAfterUnix, MaxFires: sc.MaxFires,
		ThreadID: sc.ThreadID, Text: sc.Text, Agent: sc.Agent, Cwd: sc.Cwd, Prompt: sc.Prompt, Model: sc.Model,
		SpawnMode: sc.SpawnMode, Headless: &sc.Headless, IntoSession: sc.IntoSession, ParentID: sc.ParentID, NameTemplate: sc.NameTemplate,
		Rules: sc.Rules,
	}, now, sc.ID, clockEdited)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc.Spec, sc.TZ, sc.Catchup, sc.SpawnMode = check.Spec, check.TZ, check.Catchup, check.SpawnMode
	if clockChanged {
		sc.NextFireUnix = check.NextFireUnix
	}
	// An ACTIVE record with nothing left on its clock is spent, whatever route
	// it took here (an edited spec, a not_after brought forward).
	if sc.State == api.ScheduleActive && sc.NextFireUnix == 0 {
		setScheduleState(&sc, api.ScheduleSpent, "no future occurrence", now)
	}
	if err := d.store.UpdateSchedule(sc); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sc.Machine = d.cfg.Machine
	writeJSON(w, http.StatusOK, api.ScheduleResponse{Schema: api.SchemaVersion, Schedule: sc})
}

func (d *Daemon) handleScheduleRemove(w http.ResponseWriter, r *http.Request) {
	var req api.ScheduleIDRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc, err := d.resolveScheduleRef(req.ID)
	if err != nil {
		d.scheduleErr(w, err)
		return
	}
	if err := d.store.DeleteSchedule(sc.ID); err != nil {
		d.scheduleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "removed": sc.ID})
}

// handleScheduleRunNow fires a schedule immediately (--force bypasses the
// guards) and answers with the outcome — the way to test a schedule without
// waiting for its clock. It does not touch next_fire.
func (d *Daemon) handleScheduleRunNow(w http.ResponseWriter, r *http.Request) {
	var req api.ScheduleIDRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc, err := d.resolveScheduleRef(req.ID)
	if err != nil {
		d.scheduleErr(w, err)
		return
	}
	if d.sched == nil {
		writeError(w, http.StatusServiceUnavailable, "schedule run-now: no scheduler in this daemon")
		return
	}
	d.sched.mu.Lock()
	busy := d.sched.inFlight[sc.ID]
	if !busy {
		d.sched.inFlight[sc.ID] = true
	}
	d.sched.mu.Unlock()
	if busy {
		writeError(w, http.StatusConflict, "schedule run-now: a run of "+sc.Name+" is already in progress")
		return
	}
	defer func() {
		d.sched.mu.Lock()
		delete(d.sched.inFlight, sc.ID)
		d.sched.mu.Unlock()
	}()
	outcome, detail, threadID := d.sched.runOnce(sc, time.Now(), req.Force)
	writeJSON(w, http.StatusOK, api.ScheduleRunNowResponse{Schema: api.SchemaVersion, ID: sc.ID, Outcome: outcome, Detail: detail, ThreadID: threadID})
}

func (d *Daemon) handleScheduleRuns(w http.ResponseWriter, r *http.Request) {
	sc, err := d.resolveScheduleRef(r.URL.Query().Get("id"))
	if err != nil {
		d.scheduleErr(w, err)
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "schedule runs: limit must be a non-negative integer")
			return
		}
		limit = n
	}
	runs, err := d.store.ListScheduleRuns(sc.ID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ScheduleRunsResponse{Schema: api.SchemaVersion, Runs: runs})
}

// fanOutSchedules asks every reachable peer for its schedules (the tickets
// fan-out pattern: http via the TCP API, else a real ssh hop; a peer the
// liveness cache knows is down is reported unreachable without a dial).
// statesArg renders a validated state set as a stable comma-separated list.
func statesArg(wanted map[string]bool) string {
	var out []string
	for _, s := range api.ScheduleAllStates { // vocabulary order, not map order
		if wanted[s] {
			out = append(out, s)
		}
	}
	return strings.Join(out, ",")
}

func (d *Daemon) fanOutSchedules(thread, states string) ([]api.Schedule, map[string]int, []string) {
	d.noteMeshDemand()
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return nil, nil, nil
	}
	down := d.knownOfflinePeers()
	var merged []api.Schedule
	var unreachable []string
	counts := map[string]int{}
	for _, p := range reg.List() {
		if down[p.Machine] {
			unreachable = append(unreachable, p.Machine)
			continue
		}
		rows, peerCounts, err := fetchPeerSchedules(p, thread, states)
		if err != nil {
			unreachable = append(unreachable, p.Machine)
			continue
		}
		merged = append(merged, rows...)
		for state, n := range peerCounts {
			counts[state] += n
		}
	}
	return merged, counts, unreachable
}

func fetchPeerSchedules(p peers.Peer, thread, states string) ([]api.Schedule, map[string]int, error) {
	if p.Transport() == "http" {
		c, err := peerRemoteClient(p)
		if err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
		defer cancel()
		resp, err := c.SchedulesList(ctx, thread, false, states)
		if err != nil {
			return nil, nil, err
		}
		return resp.Schedules, resp.Counts, nil
	}
	args := []string{"env", "SESH_HOME=" + shQuote(p.Home), "SESH_MACHINE=" + shQuote(p.Machine), shQuote(p.Binary), "schedule", "list", "--json"}
	if thread != "" {
		args = append(args, "--thread", shQuote(thread))
	}
	if states != "" {
		args = append(args, "--state", shQuote(states))
	}
	sshArgs := append(peers.SSHMultiplexArgs(), p.SSHArgs()...)
	sshArgs = append(sshArgs, p.SSH, strings.Join(args, " "))
	cmd := exec.Command("ssh", sshArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var out api.SchedulesResponse
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, nil, err
	}
	return out.Schedules, out.Counts, nil
}
