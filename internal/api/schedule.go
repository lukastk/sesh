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
	Enabled bool   `json:"enabled"`
	// DisabledReason says why the daemon disabled it (the failure breaker) or
	// "" for a user pause / an enabled schedule.
	DisabledReason string `json:"disabled_reason,omitempty"`

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
	// Enabled pauses/resumes (a resume recomputes next_fire from now — a paused
	// schedule never fires its backlog).
	Enabled *bool `json:"enabled,omitempty"`
}

// ScheduleIDRequest addresses one schedule (remove, run-now).
type ScheduleIDRequest struct {
	ID string `json:"id"`
	// Force (run-now) bypasses the guards: the action is performed regardless
	// of busy/headless/hold/typing — what the user typed is what happens.
	Force bool `json:"force,omitempty"`
}

// ScheduleResponse wraps one schedule.
type ScheduleResponse struct {
	Schema   int      `json:"schema"`
	Schedule Schedule `json:"schedule"`
}

// SchedulesResponse is GET /v1/schedules. Unreachable lists peers a fan-out
// could not reach (all-machines only).
type SchedulesResponse struct {
	Schema      int        `json:"schema"`
	Schedules   []Schedule `json:"schedules"`
	Unreachable []string   `json:"unreachable,omitempty"`
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
