package api

// Scheduled work (schema 49, _dev/SCHEDULING.md): a Schedule is one record —
// WHEN (a clock spec), WHAT (an action: a message into an existing thread, or a
// spawn of a new one), under what RULES — plus its own state. It lives in the
// store of the machine that EXECUTES it (a message schedule on its target
// thread's owner; a spawn schedule on the machine holding the cwd), is not
// mesh-replicated, and is reached cross-machine by --machine routing (writes)
// or a fan-out (list --all-machines).

// Schedule actions.
const (
	ScheduleActionMessage = "message"
	ScheduleActionSpawn   = "spawn"
)

// Schedule lifecycle states (§9.6). Exactly one state per record; only
// `active` fires. The three a user may SET are active, paused and archived;
// `spent` and `broken` are the daemon's own verdicts and are refused on a
// write — a caller cannot declare a schedule finished or broken, only the
// clock and the breaker can.
const (
	// ScheduleActive will fire at next_fire.
	ScheduleActive = "active"
	// SchedulePaused was turned off by hand and can be resumed (a resume
	// recomputes next_fire from now — a paused schedule never fires a backlog).
	SchedulePaused = "paused"
	// ScheduleSpent has nothing left on its clock: a one-shot that fired, a
	// max_fires count reached, a not_after passed, a cron with no further
	// occurrence. Terminal by arithmetic, not by judgement — there is nothing
	// to resume, which is what distinguishes it from paused and archived.
	ScheduleSpent = "spent"
	// ScheduleBroken is the failure breaker's verdict: it WANTS to fire and
	// keeps failing. Never hidden from a listing — a broken schedule is a
	// problem report, and hiding it is how a subsystem goes quiet (H75).
	ScheduleBroken = "broken"
	// ScheduleArchived was set aside deliberately — by hand, or carried there
	// by its target thread's archive (§9.4). Reversible: `unarchive` restores
	// it to active, or to spent if its window closed meanwhile.
	ScheduleArchived = "archived"
)

// ScheduleSettableStates are the states a write may ask for.
var ScheduleSettableStates = []string{ScheduleActive, SchedulePaused, ScheduleArchived}

// ScheduleAllStates is the whole vocabulary, for validating a --state filter.
var ScheduleAllStates = []string{ScheduleActive, SchedulePaused, ScheduleSpent, ScheduleBroken, ScheduleArchived}

// ScheduleLiveStates are what a listing shows by DEFAULT: the schedules that
// will fire, could be made to fire again, or are failing to. `spent` and
// `archived` are inert by construction and would otherwise bury the live set —
// one agent-driven week on Lukas's rig left 101 of 117 records unfireable.
var ScheduleLiveStates = []string{ScheduleActive, SchedulePaused, ScheduleBroken}

// ScheduleStateSettable reports whether a caller may write this state.
func ScheduleStateSettable(s string) bool {
	for _, v := range ScheduleSettableStates {
		if v == s {
			return true
		}
	}
	return false
}

// ScheduleStateKnown reports whether a state is in the vocabulary.
func ScheduleStateKnown(s string) bool {
	for _, v := range ScheduleAllStates {
		if v == s {
			return true
		}
	}
	return false
}

// ScheduleStateLive reports whether a state belongs to the default listing.
func ScheduleStateLive(s string) bool {
	for _, v := range ScheduleLiveStates {
		if v == s {
			return true
		}
	}
	return false
}

// Catch-up policies for occurrences missed while the daemon was down/asleep.
const (
	CatchupSkip = "skip" // roll forward, log the count, never fire the missed ones (default)
	CatchupOnce = "once" // fire exactly one catch-up run, then resume the cadence
)

// ScheduleRules is the guard/effect set (stored as one JSON column so it can
// grow without a migration per flag; validated on write).
type ScheduleRules struct {
	// --- message action ---
	// WhenHeadless: what to do when the target has no live pane — "turn"
	// (deliver as a headless turn; default), "revive" (resume it into a pane
	// first), "skip" (only ever deliver into a live pane).
	WhenHeadless string `json:"when_headless,omitempty"`
	// WhenBusy: "skip" (never interrupt a running turn; default — the heartbeat
	// rule) or "send" (paste anyway; a headless·busy target is a loud failure).
	WhenBusy string `json:"when_busy,omitempty"`
	// If is the closed guard vocabulary, all of which must hold: idle, busy,
	// headful, headless, attached, detached, flagged, not-flagged, archived,
	// not-archived, blocked, not-blocked. Unknown words are refused at creation.
	If []string `json:"if,omitempty"`
	// IdleForS requires the target to have been idle this many seconds (from the
	// snapshot's last_active_unix). nil = 60 s whenever `idle` is in If (the
	// stale-busy classes H58/H64 make a bare idle read unsafe); 0 = no dwell.
	IdleForS *int64 `json:"idle_for_s,omitempty"`
	// IgnoreHold / AllowArchived override the two implicit skips (a parked or
	// archived thread is left alone by default).
	IgnoreHold    bool `json:"ignore_hold,omitempty"`
	AllowArchived bool `json:"allow_archived,omitempty"`
	// RespectTypingMs overrides the pane typing guard for this schedule's pastes
	// (nil = [send] respect_typing; 0 = off). A typing pane SKIPS the run.
	RespectTypingMs *int `json:"respect_typing_ms,omitempty"`

	// --- spawn action ---
	// IfPrevious: when the previous run's thread is still going (a live pane or
	// a turn in flight): "skip" (default), "spawn-anyway", "stop-previous".
	IfPrevious string `json:"if_previous,omitempty"`
	// OnTurnEnd: what happens to the run's thread when its first turn ends —
	// "keep" (default), "stop", "archive", "stop+archive", "delete".
	OnTurnEnd string `json:"on_turn_end,omitempty"`
	// MaxRuntimeS stops a run still going after this many seconds (0 = none).
	MaxRuntimeS int64 `json:"max_runtime_s,omitempty"`
	// FlagOnEnd re-enables auto-flagging on a HEADED run's thread (off by
	// default: an unattended recurring turn would flag itself every time).
	FlagOnEnd bool `json:"flag_on_end,omitempty"`
}

// Schedule is the stored record plus its live state.
type Schedule struct {
	ID      string `json:"id"`
	Machine string `json:"machine"`
	Name    string `json:"name"`
	Action  string `json:"action"`
	// State is the lifecycle label (one of the Schedule* constants above) and
	// the ONLY firing gate: the scheduler loads `active` and nothing else.
	// It replaced an `enabled bool` + free-text reason, which collapsed "off
	// for now", "finished", and "broken" into one indistinguishable state that
	// no query could filter (schema 54).
	State string `json:"state"`
	// StateReason is the sentence behind a non-active state ("one-shot fired",
	// "max_fires 1 reached", "3 consecutive failures — last: …", "thread
	// archived"); "" for active and for a plain hand pause.
	StateReason string `json:"state_reason,omitempty"`
	// StateAtUnix is when the record entered its current state — the age a
	// `prune --before` reads, and what tells you whether a broken schedule
	// broke an hour ago or in August.
	StateAtUnix int64 `json:"state_at_unix,omitempty"`

	// WHEN
	Spec          string `json:"spec"` // "cron <expr>" | "every <dur>" | "at <instant>"
	TZ            string `json:"tz"`   // IANA zone, recorded at creation
	Catchup       string `json:"catchup"`
	NotBeforeUnix int64  `json:"not_before_unix,omitempty"`
	NotAfterUnix  int64  `json:"not_after_unix,omitempty"`
	MaxFires      int    `json:"max_fires,omitempty"`

	// WHAT: message
	ThreadID string `json:"thread_id,omitempty"`
	Text     string `json:"text,omitempty"`
	// WHAT: spawn
	Agent        string `json:"agent,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	Model        string `json:"model,omitempty"`
	SpawnMode    string `json:"spawn_mode,omitempty"` // the EFFECTIVE mode, recorded for disclosure
	Headless     bool   `json:"headless,omitempty"`
	IntoSession  string `json:"into_session,omitempty"`
	ParentID     string `json:"parent_id,omitempty"`
	NameTemplate string `json:"name_template,omitempty"`

	Rules ScheduleRules `json:"rules"`

	// STATE
	NextFireUnix int64  `json:"next_fire_unix,omitempty"`
	LastFireUnix int64  `json:"last_fire_unix,omitempty"`
	LastOutcome  string `json:"last_outcome,omitempty"` // fired | skipped: <why> | failed: <err>
	FireCount    int    `json:"fire_count"`
	SkipCount    int    `json:"skip_count"`
	FailStreak   int    `json:"fail_streak"`
	// Missed is the running count of occurrences slept through under catchup=skip.
	Missed        int   `json:"missed,omitempty"`
	CreatedAtUnix int64 `json:"created_at_unix"`
	// LastRunThreadID is the thread the most recent SPAWN run created ("" for
	// message schedules), so a listing can point at it.
	LastRunThreadID string `json:"last_run_thread_id,omitempty"`
}

// ScheduleRun is one fire's record: what it decided and, for a spawn, the
// thread it made.
type ScheduleRun struct {
	ScheduleID  string `json:"schedule_id"`
	FiredAtUnix int64  `json:"fired_at_unix"`
	ThreadID    string `json:"thread_id,omitempty"`
	Outcome     string `json:"outcome"` // fired | skipped: … | failed: …
	Detail      string `json:"detail,omitempty"`
	EndedAtUnix int64  `json:"ended_at_unix,omitempty"` // spawn: when the run's thread was reaped/finished
}

// CreateScheduleRequest is the body of POST /v1/schedules. The clock is given
// as Spec text; TZ empty = the owning daemon's local zone, which is resolved
// and RECORDED (the response carries it, and the CLI prints it back).
type CreateScheduleRequest struct {
	Name    string `json:"name,omitempty"`
	Action  string `json:"action"`
	Spec    string `json:"spec"`
	TZ      string `json:"tz,omitempty"`
	Catchup string `json:"catchup,omitempty"`
	// NotBeforeUnix/NotAfterUnix/MaxFires bound the schedule's life.
	NotBeforeUnix int64 `json:"not_before_unix,omitempty"`
	NotAfterUnix  int64 `json:"not_after_unix,omitempty"`
	MaxFires      int   `json:"max_fires,omitempty"`

	ThreadID string `json:"thread_id,omitempty"`
	Text     string `json:"text,omitempty"`

	Agent        string `json:"agent,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	Model        string `json:"model,omitempty"`
	SpawnMode    string `json:"spawn_mode,omitempty"` // '' = the [spawn] config
	Headless     *bool  `json:"headless,omitempty"`   // nil = headless (the decided default)
	IntoSession  string `json:"into_session,omitempty"`
	ParentID     string `json:"parent_id,omitempty"`
	NameTemplate string `json:"name_template,omitempty"`

	Rules ScheduleRules `json:"rules"`
}

// UpdateScheduleRequest is the body of POST /v1/schedules/update: only non-nil
// fields change; a changed clock recomputes next_fire.
type UpdateScheduleRequest struct {
	ID           string         `json:"id"`
	Name         *string        `json:"name,omitempty"`
	Spec         *string        `json:"spec,omitempty"`
	TZ           *string        `json:"tz,omitempty"`
	Catchup      *string        `json:"catchup,omitempty"`
	NotAfterUnix *int64         `json:"not_after_unix,omitempty"`
	MaxFires     *int           `json:"max_fires,omitempty"`
	Text         *string        `json:"text,omitempty"`
	Prompt       *string        `json:"prompt,omitempty"`
	Rules        *ScheduleRules `json:"rules,omitempty"`
	// State is pause/resume/archive/unarchive (one of ScheduleSettableStates;
	// `spent` and `broken` are refused). Moving TO active recomputes next_fire
	// from now — a schedule off the clock never fires its backlog — and a
	// record whose window has closed meanwhile lands in `spent` instead, with
	// a reason, rather than failing the call.
	State *string `json:"state,omitempty"`
}

// ScheduleIDRequest addresses one schedule (remove, run-now).
type ScheduleIDRequest struct {
	ID string `json:"id"`
	// Force (run-now) bypasses the guards: the action is performed regardless
	// of busy/headless/hold/typing — what the user typed is what happens.
	Force bool `json:"force,omitempty"`
}

// PruneSchedulesRequest is POST /v1/schedules/prune — the ONLY path that
// deletes schedule records, and it is always a deliberate call: nothing in the
// daemon prunes on its own. Spent and archived records are hidden rather than
// collected, because a schedule carries the prompt text that was actually
// scheduled plus its run history, and sesh does not auto-delete durable
// records (the same rule that protects thread records).
type PruneSchedulesRequest struct {
	// States to delete; empty = just ScheduleSpent. ScheduleActive is refused
	// (it would silently cancel live work), so is ScheduleBroken (it would
	// erase a problem report).
	States []string `json:"states,omitempty"`
	// BeforeUnix deletes only records that entered their state before this
	// instant; 0 = no age bound.
	BeforeUnix int64 `json:"before_unix,omitempty"`
	// Thread limits the sweep to one target thread's schedules.
	Thread string `json:"thread,omitempty"`
	// DryRun reports what would go without touching anything.
	DryRun bool `json:"dry_run,omitempty"`
}

// PruneSchedulesResponse lists exactly what was (or would be) removed.
type PruneSchedulesResponse struct {
	Schema  int        `json:"schema"`
	DryRun  bool       `json:"dry_run"`
	Removed []Schedule `json:"removed"`
}

// ScheduleResponse wraps one schedule.
type ScheduleResponse struct {
	Schema   int      `json:"schema"`
	Schedule Schedule `json:"schedule"`
}

// SchedulesResponse is GET /v1/schedules. Unreachable lists peers a fan-out
// could not reach (all-machines only).
type SchedulesResponse struct {
	Schema    int        `json:"schema"`
	Schedules []Schedule `json:"schedules"`
	// Counts is the per-state census of everything the filter was applied to
	// (before it), so a default listing can say how many records it held back
	// instead of hiding them silently.
	Counts      map[string]int `json:"counts,omitempty"`
	Unreachable []string       `json:"unreachable,omitempty"`
}

// ScheduleRunsResponse is GET /v1/schedules/runs?id=.
type ScheduleRunsResponse struct {
	Schema int           `json:"schema"`
	Runs   []ScheduleRun `json:"runs"`
}

// ScheduleRunNowResponse is POST /v1/schedules/run-now: the run's outcome, and
// for a spawn the thread it created.
type ScheduleRunNowResponse struct {
	Schema   int    `json:"schema"`
	ID       string `json:"id"`
	Outcome  string `json:"outcome"`
	Detail   string `json:"detail,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
}

// ScheduleOutcome prefixes.
const (
	OutcomeFired   = "fired"
	OutcomeSkipped = "skipped"
	OutcomeFailed  = "failed"
)
