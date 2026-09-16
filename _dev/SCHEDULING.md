# sesh v2 — scheduled work: timed messages and timed spawns (design)

*Status: **SCOPE / DESIGN. Nothing here is built.** Ticket 28f6e77b ("Scheduled messages and
agents feature"), 2026-09-16. This doc answers the ticket's own question — "it might be one or
two different [features], I'm not sure" — and then specifies the thing well enough to build.
Open decisions Lukas owns are marked ⚑ and collected in §15.*

---

## 0. The verdict, up front

**One mechanism, two actions, and a build order that ships them separately.**

A schedule is one record: **when** (a cron/interval spec), **what** (an action), **under what
rules** (guards + effects), plus its own state (next fire, last outcome, counters). The two
things the ticket describes are the same record with a different `action`:

| action | what it does | the ticket's words |
|---|---|---|
| `message` | deliver text into an EXISTING thread | "schedule messages to send to them" |
| `spawn` | create a NEW thread in a cwd and give it a prompt | "a cron for spawning a new thread in a given folder" |

They share the clock, the catch-up policy, the pause/expiry/max-fires machinery, the outcome
history, the CLI family, the routing, and — this is the load-bearing part — **the guard
vocabulary**, because "don't send if it's already running" and "don't spawn if the last run is
still going" are the same predicate over the same state axes. Splitting them into two features
would duplicate all of that and then drift.

But they are **not** equally justified, and the honest scoping says so:

- **The `message` action must live in sesh.** It needs the target's live state (`head`, `busy`,
  `attachment`, `last_active`), evaluated *at fire time, on the owning machine, without a
  race*, and then needs to pick between two different delivery paths (a pane paste vs a headless
  turn) and possibly revive a pane first. That is mechanism — exactly what `sesh` owns and what
  a shell wrapper cannot do correctly (§11.1).
- **The `spawn` action is mostly convenience.** `0 9 * * 1-5 sesh delegate --agent pi --cwd X "prompt"` in a crontab already does ~80 % of it today (§2.3). Scheduled spawn earns its place
  through **uniformity, lifecycle rules, fleet visibility, and the overlap guard** — not through
  impossibility. It is cheap once the engine exists, so build it second, deliberately.

Recommended naming: **`sesh schedule`**, records called **schedules**, one run of a schedule
called a **run**.

---

## 1. What was asked — restated precisely, with one vocabulary correction

From the ticket, the wanted rules were:

1. *"if you send a message and the thread is unattached, you can choose to have the message
   revive the thread first and attach it"*
2. *"a rule so the message will always be sent to an attached thread rather than an unattached
   one"*
3. *"the message only gets sent if the thread isn't already running … a heartbeat message to
   make sure the agent keeps going"*
4. spawn: *"when the thread finishes its turn, it is automatically killed and archived"*
5. spawn: *"whether the thread gets spawned as an attached or unattached thread"*

### 1.1 "attached" here means HEADFUL, not the attachment axis

This matters enough to fix before any flag is named. sesh already has three orthogonal axes
(SPEC §3), and the ticket's "attached/unattached" collides with the third one:

| axis | values | meaning |
|---|---|---|
| **head** | `headful` / `headless` | is there a live tmux pane running the agent |
| **busy** | `busy` / `idle` | is a turn executing right now |
| **attachment** | `attached` / `detached` | is a tmux CLIENT viewing that session — i.e. **is a human looking** |

"Revive it first and attach it" = **headless → headful** (`thread resume`/`headful` — a pane is
created and the conversation resumed). The daemon cannot make a thread `attached`: attachment
requires a terminal client, which only the cockpit/user has. This is the same correction made
for shell threads (`_dev/SHELL.md`, H89).

So the ticket's rules 1, 2 and 5 are all about the **head** axis, and rule 3 is about the
**busy** axis. The **attachment** axis is still worth exposing as a guard — but for a different
and genuinely useful purpose (§6.5: don't paste into a pane a human is typing in), and it must
be spelled differently so the two can never be confused.

### 1.2 What the ticket did not say, that the design has to answer anyway

Missed fires while a machine was asleep; timezone/DST; what happens when the target thread is
deleted, archived, or on hold; what stops a heartbeat from running forever; how a failure is
surfaced; and where the schedule record lives when the thread is on another machine. §5, §9 and
§15.

---

## 2. What exists today (so the gap is honest)

### 2.1 Primitives a scheduler would compose — all already green

| capability | today's surface |
|---|---|
| deliver text into a live pane | `thread send` → `tmux.Server.SendText` (bracketed paste since H77/H90) |
| deliver one stateless turn to a headless thread | `thread send-headless` (in-flight registry; overlap = loud 409) |
| revive a headless thread into a pane | `thread resume` / `thread headful` (+ `confirmAgentLaunched`, H34) |
| create a thread anywhere | `thread new` (headed/headless, placement modes, `--parent`); `--msg` delivers a first prompt once the agent is ready (`sendWhenReady`, headed only — refused with `--headless`) |
| spawn → prompt → await → archive | **`sesh delegate`** (cmd/sesh/delegate.go) — the ephemeral-worker contract |
| block until a turn ends | `thread wait` / `thread send --wait` / `sesh await` |
| act on a busy→idle edge | the eventer (`observePair`) → subscriptions, auto-flag, `[[hooks]]` |
| park a thread until an instant | `thread hold --until` — **deadline state with no record write** |
| deliver into a thread by state | `sendIntoThread` (subscriptions.go): pane if headful, headless turn if idle, loud skip if headless·busy |
| reach another machine | `--machine` routing (re-exec over ssh / the peer's HTTP API) |
| run a command on an event | `[[hooks]]` (`$SHELL -c`, 15 s timeout, env-described event) |

**Nothing in the list is time-triggered.** Every one of them fires on a user action or an
observed state edge. That is the entire gap: sesh has no clock-driven input.

### 2.2 The closest existing thing is `hold`

`internal/daemon/hold.go` is the only place where a **future instant** changes behaviour. Its
mechanism is worth copying exactly, because it is the one non-obvious part: a deadline that
flips a derived value **with no record write** must be turned into a scheduled recomputation, or
nothing will ever recompute it. `nextHoldFlip(holds, threads, nowUnix)` returns the earliest
future instant at which some thread's `on_hold` flips; the maintainer stores it and folds it into
its `needFull` gate. A missing schedule there would have left a released thread reading un-held
forever — which is exactly the failure mode a cron with a bad next-fire computation has.

### 2.3 What is already possible without any new code — the honest baseline

Today, on any machine:

```cron
*/30 * * * * sesh thread send --id 1777a4ac --text "status check" >/dev/null 2>&1
0 9 * * 1-5  sesh delegate --agent pi --cwd ~/dev/…__mosaic "Review yesterday's CI failures"
```

and the fleet's existing convention for this is a **self-installing script that registers its own
cron/launchd entry** with a label, its own throttle stamp and its own log file
(`myrig/home/.mybin/atuin-vacuum` is the reference implementation: `LABEL="work.lukastk.…"`,
`THROTTLE_DAYS`, a `$CACHE_DIR/*.stamp`, a `$CACHE_DIR/*.log`, and a `is_termux` exclusion).

That baseline works and should be respected. What it cannot do:

1. **Guard on live thread state.** `sesh thread send` always sends. "Only if idle" requires
   reading the state and deciding **on the owner, atomically with the send** — a shell that does
   `sesh thread status && sesh thread send` has a real TOCTOU window (the maintainer tick is
   300 ms; a turn can start in between), and the failure mode is interrupting a running turn.
2. **Revive-then-send as one operation**, with the codex-before-first-turn N/A surfaced honestly
   rather than as an opaque shell error.
3. **Follow the thread.** A crontab is per-machine, hand-maintained, and keyed by a thread id
   that may be archived or deleted tomorrow. Nothing removes the cron line; it fails silently
   into a deleted id forever.
4. **Be visible.** Six machines × crontabs is exactly the fleet-config sprawl sesh exists to
   kill (SPEC §7). `sesh tui` cannot show that a thread has a heartbeat; `sesh schedule list
   --all-machines` does not exist.
5. **Report.** No record of "fired / skipped because busy / failed", so a heartbeat that stopped
   working is invisible until someone notices the agent went quiet.
6. **Survive the phone.** termux has cronie but the whole uid gets killed by lmkd (H108); a
   daemon-owned schedule at least fires whenever the daemon is up and says loudly what it missed.

Points 1, 2 and 5 are the ones that actually require sesh.

---

## 3. The model

### 3.1 Ownership: a schedule lives on the machine that will execute it

This follows SPEC §1's one consistent rule, and it is the most consequential decision here.

- A `message` schedule is owned by **the target thread's owning machine** — because every
  delivery path is owner-side: the pane lives in that machine's tmux, the headless turn reads
  that machine's transcripts, and the state it guards on is that machine's maintained snapshot.
- A `spawn` schedule is owned by **the machine the thread will be spawned on** (the machine
  holding the cwd).

Consequences, all good:

- Firing is a purely local operation: no cross-machine call on the hot path, no "did the fire
  get through" ambiguity.
- A schedule fires **iff the machine that hosts the work is up**. It does not depend on the
  machine you happened to create it from being awake — which would be fatal for a cron created
  from a laptop (macbook sleeps; H95/H101 are full of "machine asleep" deploy notes).
- `sesh schedule message --id <thread> --machine <m>` routes like every other verb and gets
  routing for free from `cmd/sesh/route.go`.
- Deleting the thread deletes its schedules **in the same transaction** (§9.4).

### 3.2 Not replicated in the mesh snapshot

Schedules follow the **ticket/subscription precedent**: an owner-local table, reached
cross-machine by `--machine` routing for writes and a **fan-out** for mesh-wide reads
(`internal/daemon/fanout.go` already does this for tickets and the grid). They are deliberately
NOT added to `api.ThreadSnapshot`, because every field there is re-transferred on every changed
row of every sync round (MESH_SCALE) and schedules change on human timescales.

⚑ **Exception worth considering (§15.6):** two small omitempty fields on the snapshot —
`schedules int` and `next_fire_unix int64` — would let the TUI render a "this thread has a
heartbeat" marker without a fan-out. Cheap (two ints, only on rows that have schedules) and it is
the only way the sidebar can show it. Recommendation: **yes, in phase 3**, not before.

### 3.3 The firing pipeline — five phases, in this order

```
   due?          →  guards        →  prepare       →  act          →  post
   (clock)          (skip or go)     (make the        (send /         (record outcome;
                                      runtime fit)     spawn)          arm the run reaper)
```

Every phase can end the run, and **every ending is recorded with a reason**. A skip is a
first-class, visible outcome — not a silent no-op. This is the shape that makes "only if not
running" honest: the guard is evaluated on the owner against the maintained snapshot
(≤300 ms stale) and the action is performed by the same daemon that owns the state.

---

## 4. The record

Store table (append as migration 26; the store never calls `time.Now` — the daemon passes `now`
in), plus its own `revs` row + triggers if the scheduler loop is to be rev-gated:

```sql
CREATE TABLE schedules (
  id            TEXT PRIMARY KEY,        -- uuid, prefix-resolvable like threads/tickets
  name          TEXT NOT NULL,           -- human label, unique per machine (loud on collision)
  action        TEXT NOT NULL,           -- 'message' | 'spawn'
  enabled       INTEGER NOT NULL DEFAULT 1,

  -- WHEN
  spec          TEXT NOT NULL,           -- '*/30 * * * *' | 'every 15m' | 'at 2026-09-17T09:00'
  tz            TEXT NOT NULL,           -- IANA name, resolved AND RECORDED at creation
  catchup       TEXT NOT NULL,           -- 'skip' | 'once'
  not_before    INTEGER,                 -- optional start instant
  not_after     INTEGER,                 -- optional expiry (0 = none)
  max_fires     INTEGER,                 -- 0 = unlimited

  -- WHAT (message)
  thread_id     TEXT,                    -- target thread (message action)
  text          TEXT,                    -- may carry @blob() tokens; expanded on delivery

  -- WHAT (spawn)
  agent         TEXT, cwd TEXT, prompt TEXT, model TEXT, spawn_mode TEXT,
  headless      INTEGER, into_session TEXT, parent_id TEXT, name_template TEXT,

  -- RULES (json blob: the guard/effect set of §6–§7, one column so rules can grow
  --        without a migration per flag; validated on write, loud on unknown keys)
  rules         TEXT NOT NULL,

  -- STATE
  next_fire     INTEGER NOT NULL,
  last_fire     INTEGER,
  last_outcome  TEXT,                    -- 'fired' | 'skipped:<reason>' | 'failed:<err>'
  fire_count    INTEGER NOT NULL DEFAULT 0,
  skip_count    INTEGER NOT NULL DEFAULT 0,
  fail_streak   INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL
);
```

And, for the spawn action's lifecycle rules (§7.3), a second small table so a daemon restart
mid-run still reaps:

```sql
CREATE TABLE schedule_runs (
  schedule_id TEXT NOT NULL, thread_id TEXT NOT NULL,
  started_at INTEGER NOT NULL, ended_at INTEGER, outcome TEXT,
  PRIMARY KEY (schedule_id, thread_id)
) WITHOUT ROWID;
```

`schedule_runs` doubles as the **history** surface (`sesh schedule runs --id`) and as the
**overlap guard**'s source of truth (§7.2). Seeded into an in-memory map at daemon start, the
same way `seedSubTracker` restores subscription counts.

---

## 5. When: the clock

### 5.1 Spec forms

Three, all explicit, one field:

| form | example | notes |
|---|---|---|
| cron | `--cron '*/30 * * * *'` | 5-field, standard; no seconds field, no `@reboot` |
| interval | `--every 15m` | anchored at creation (or at `--not-before`); no cron-expression thinking required |
| one-shot | `--at '2026-09-17 09:00'` | fires once and disables itself (does NOT delete — the record is the receipt) |

Parsing: a **cron parser must be written or vendored**. The repo has 8 direct dependencies and a
strong preference for small ones; `robfig/cron/v3` is the obvious vendor (MIT, ~1k lines, widely
used) but a 5-field parser with a `Next(time.Time) time.Time` is ~150 lines and unit-tests
exhaustively. ⚑ §15.4. Either way the parser must be a **pure function** `Next(spec, tz, after)
→ time.Time` so it unit-tests like `TestEffectiveHolds` does, with literal constants.

### 5.2 Timezone — recorded, never inferred at fire time

A schedule stores an IANA zone name, defaulted to the **owning machine's** zone at creation and
**printed back** at creation: `scheduled 09:00 Europe/London on mymain — next fire Wed 09:00`.
This matters because a routed create (`--machine macstudio` from mymain) would otherwise silently
mean a different wall-clock time than the one typed, and because DST shifts a `0 9 * * *` by an
hour if the zone is implicit. Cron's classic DST edges (a 02:30 job on the spring-forward day)
get the standard, documented answer: **skip the nonexistent local time, fire once on the
repeated one** — stated in the doc and pinned by a unit test.

### 5.3 Missed fires (the laptop-was-asleep problem)

Per schedule, `catchup`:

- **`skip` (default).** On daemon start, or after any gap, roll `next_fire` forward to the first
  future occurrence and **log loudly** how many were missed. `schedule list` shows
  `missed 14 while the daemon was down`. This is the right default: a macbook that wakes to 200
  queued heartbeats is the failure mode.
- **`once`.** Fire exactly one catch-up run immediately, then resume the normal cadence. For
  "the daily review must happen even if the machine was off until noon".

Never "fire all missed occurrences". There is no use case and it is an unbounded storm.

### 5.4 The loop

A fourth daemon subsystem in the established shape (`{mu, started, stop, done}` +
`start`/`stopAndWait`/`run`/`tick`), started in `Serve()` after `d.mmaint.start()` and stopped
first in `Shutdown`. **`const schedulerTick = 1 * time.Second`**, comparing wall clock against
each enabled schedule's `next_fire`.

A 1 s poll rather than a timer armed at `nextFire` is deliberate: it is trivially robust to
suspend/resume, to system clock jumps (NTP step, timezone database update), and to schedules
being edited underneath it; and with tens of schedules the per-tick cost is a map walk and an
integer compare. The `nextHoldFlip` style precomputation is available if it ever matters; it does
not at this scale, and the simpler thing is the one that cannot be subtly wrong after a laptop
lid opens.

Per-schedule in-flight guard (a set keyed by schedule id, like `hlInFlight` / meshsync's
per-peer guard): **two runs of the same schedule never overlap**, and `next_fire` is advanced at
fire time (not at completion) so a slow run cannot skew the cadence.

Why not fold it into the maintainer's 300 ms tick, which already holds the record list and the
rev gate? Because the maintainer is the daemon's hottest loop (every marked pane captured per
tick, H102) and its correctness rests on the rev-gated sweep; a scheduler needs neither the
captures nor the gate, and a fire that revives a pane takes seconds — it must not run inside
the loop that is trying to observe that pane. The scheduler *reads* the maintainer's published
snapshot (`stateOf`, O(1)); it never joins its tick.

### 5.5 Bounding a schedule's life

`--max-fires N`, `--not-after <date>`, `--pause`/`--resume`, and an automatic
**`disable-after-failures`** (default 10, configurable, loud): a schedule that has failed ten
times running is broken, and continuing to run it hides the breakage. Disabling is recorded in
`last_outcome` and surfaced by `sesh doctor` (§9.2).

---

## 6. Rules, part 1 — message schedules

The whole design goal here is to turn the ticket's list of examples into a **small orthogonal
vocabulary** rather than a bag of booleans that interact unpredictably.

### 6.1 `--when-headless {turn|revive|skip}` — the head-axis rule

This single tri-state encodes all three of the ticket's head-axis wants:

| value | behaviour when the target is headless | ticket's phrasing |
|---|---|---|
| `turn` (default) | deliver as a **headless turn** (`send-headless`) — the subscriptions engine's rule | — |
| `revive` | **resume the thread into a pane first**, then paste | "revive the thread first and attach it" |
| `skip` | do nothing; record `skipped: headless` | "always be sent to an attached [live] thread rather than an unattached one" |

When the target is already headful, all three deliver into the pane. `revive` goes through the
one existing implementation (`reviveThread`), which already reserves the thread's headless
in-flight slot for the whole revival — so a headless turn cannot start underneath it — and
inherits `confirmAgentLaunched`'s settle check (H34), so a resume that dies a second later is a
**loud failed run**, not a silent success. codex-before-its-first-turn cannot be resumed — the
existing justified N/A (422) — and must surface as `failed: codex has no session id yet`, never
as a fake fire.

### 6.2 `--when-busy {skip|send}` — the busy-axis rule, and the heartbeat

| value | behaviour when the target is `busy` |
|---|---|
| `skip` (default) | do nothing; record `skipped: busy`. **This is the heartbeat rule.** |
| `send` | deliver anyway — a pane paste lands in the agent's input box (all three TUIs queue typed input during a turn); a headless·busy target has no such affordance and returns the existing loud 409, recorded as a failed run |

Default `skip` is the conservative choice: never interrupt a running turn unless asked.

**Deliberately NOT in scope for phase 1: `--when-busy wait`** (defer the delivery until the turn
ends, with a deadline). It is a genuinely useful third value, and it is also a persisted pending
queue with ordering, expiry and restart semantics — a feature of its own. The composition already
exists for a human (`thread send --wait`), the wait endpoint's `settled` condition (idle OR
blocked — "the agent stopped on its own") is the right trigger, and the eventer's busy→idle edge
is where it would hook. Note it, do not build it.

### 6.3 `--if <cond>[,<cond>…]` — the general guard

A fixed, closed vocabulary of predicates over the axes the snapshot already carries (all must
hold; unknown condition = loud error at creation, not at fire time):

`idle`, `busy`, `headful`, `headless`, `attached`, `detached`, `flagged`, `not-flagged`,
`archived`, `not-archived`, `blocked`, `not-blocked`.

Not a DSL. SPEC §6 wants explicit, machine-readable contracts; a mini-language here is a
maintenance sink and an un-testable surface. (The TUI's `[[tui.views]]` predicate language exists
and could be reused if a richer grammar is ever wanted — ⚑ §15.5.)

### 6.4 `--idle-for <dur>` — the guard that makes a heartbeat safe

`--if idle` alone is not enough, because `busy` is not always exact:

- For claude and pi with reporters live, busy is **authoritative** (`state_authority=reported`).
- For codex, and for any thread whose reporter died, busy is the **content-diff heuristic** —
  and the H-log has both failure directions on record: a stale reported-busy pinning a finished
  thread (H58) and a stale reported-idle pinning a *running* one (H64). A heartbeat that fires on
  a false idle interrupts a real turn.

`--idle-for 5m` requires the thread to have been idle for a dwell time, computed from the
snapshot's existing `last_active_unix`. It costs nothing and it is the difference between a
heartbeat that nudges a stuck agent and one that talks over a working agent. **Recommend making
it the default for `--if idle` (60 s) rather than an opt-in.** ⚑ §15.2.

Optional companion: `--require-authority` (only fire when `state_authority == reported`), for the
paranoid case. Off by default — it would make every codex thread unschedulable.

### 6.5 The two implicit skips, and the typing collision

**On hold → skip (default), `--ignore-hold` to override.** A hold is the user's own explicit
"leave this alone" signal, it auto-expires, and H105 established that it means *parked active
work*. A scheduled message reviving a parked thread at 3 am is precisely the surprise this
project avoids.

**Archived → skip (default), `--allow-archived` to override.** Same reasoning; an archived
thread reviving itself is surprising. (Note the exception that already exists: an
archived-but-headful thread is live and visible in the active view — H40 — so `--allow-archived`
is meaningful.)

**The typing collision — a real hazard, not a hypothetical.** `SendText` pastes into the pane and
then sends Enter. If a human is mid-way through typing a line in that pane, the paste is appended
to their half-written text and Enter submits **the concatenation**. Subscriptions have carried
this hazard since they were built; a scheduled message makes it recurring and unattended.

The signal to avoid it already exists: H48 added `attached_activity_unix` to the snapshot (the
newest tmux `client_activity` = last *input* from a client on that session). So:

> **`--respect-typing <dur>` (default 60 s, ON) for pane deliveries:** if the thread is attached
> and someone typed within the window, skip this run (recorded as `skipped: viewer typing`).

⚑ §15.3 — defaulting it ON is a behaviour choice, but silently corrupting a half-typed prompt is
the worse failure.

### 6.6 What a heartbeat actually looks like

```bash
sesh schedule message --id 1777a4ac \
  --every 20m \
  --text "Continue with the plan. If you are blocked, say so and stop." \
  --if idle --idle-for 10m \
  --when-headless skip \
  --max-fires 30 --not-after 2026-09-20
```

Reads exactly as intended: *every 20 minutes, if this thread has been quiet for 10 minutes and
still has a live pane, nudge it — at most 30 times, and never after Saturday.*

The rails are not decoration. An unbounded heartbeat is an agent that runs forever and spends
tokens forever; `--max-fires` / `--not-after` make the bound explicit at creation. On top of that
the engine keeps a **circuit breaker** modelled on `subscribe.Tracker` (currently 6 deliveries
per minute per edge): a per-schedule cap that trips loudly rather than looping.

---

## 7. Rules, part 2 — spawn schedules

### 7.1 The spawn form

Everything `thread new` already takes, recorded on the schedule: `--agent`, `--cwd`, `--prompt`
(or `--prompt-file`, or a `@blob()` token — expansion is already automatic on delivery),
`--model`, `--headless` (the ticket's "attached or unattached" — ⚑ default below), placement
(`--into-session`), `--parent`, and the spawn mode (`--yolo`/`--sandbox`, as `delegate` has).

Two additions that only matter because the spawn is recurring:

- **`--name-template`** — default `<schedule-name>-<YYYY-MM-DD-HHMM>`. Without it you get thirty
  threads called "nightly-review" and the TUI is unusable.
- **`--parent <thread>`** — pointing it at a **virtual thread** (H37) groups every run of the
  schedule under one collapsible node in the TUI. This is the composition that makes recurring
  spawns tolerable in a 2,000-thread mesh, and it costs nothing: virtual parents already exist.

⚑ **Default head for a scheduled spawn: `--headless`.** Cheap, no pane churn, matches `delegate`,
and a headed unattended spawn creates panes on a machine nobody is looking at. Headed remains
available and is the right choice when the run is meant to be *found* later in the cockpit.

**The two heads take two different, existing prompt paths — and one of them fails silently
today.** A headless run is `thread new --headless` + `send-headless` (the `delegate` shape; the
turn's completion and its `ERROR:` reply are observable). A headed run is `thread new --msg`,
whose delivery is `sendWhenReady`: a goroutine that polls up to 90 s for a rendered, live agent
pane and then pastes — and on timeout **logs a line and tells the caller nothing**. That is
acceptable for a human who is about to look at the pane; it is not acceptable for an unattended
run. The scheduler must own the readiness wait for headed spawns (the same poll, synchronous,
its outcome recorded as `failed: agent never became ready` with the pane's last output) rather
than inherit the fire-and-forget path.

### 7.2 The overlap guard — the spawn analogue of "don't send if busy"

`--if-previous {skip|spawn-anyway|stop-previous}` (default **`skip`**): if the previous run's
thread is still alive (record present, not archived, and headful-or-busy), the new run is
skipped and recorded. Without this, a 5-minute cron over a 10-minute job grows threads without
bound — the single most likely way this feature hurts someone.

`--max-runtime <dur>` bounds the other side: a run still going after the limit is stopped and
recorded as `failed: exceeded max-runtime`, so a wedged agent does not hold the singleton slot
forever.

### 7.3 The lifecycle rule — "killed and archived when it finishes its turn"

`--on-turn-end {keep|stop|archive|stop+archive|delete}`, default **`stop+archive`** (exactly
`delegate`'s ephemeral contract: the record and transcript are retained and auditable, the thread
leaves the active view).

Mechanism: the run is recorded in `schedule_runs` at spawn; the **eventer's busy→idle edge**
(`observePair`, where `deliverSubscriptions` already hooks in) fires the reaper for the run's
**first** turn end. Persisting the run row is what makes this survive a daemon restart mid-run —
an in-memory-only map would leak a live thread on every restart.

Restart mid-run has two different truths, and the seed-time reconciliation must know both:
a **headless** run's turn is a goroutine of the daemon process (`agents.HeadlessTurn` under
`$SHELL -c`), so it **dies with the daemon** — on start, an open run row whose thread is headless
is recorded `failed: daemon restarted mid-turn` and reaped per policy; a **headed** run's pane is
tmux's, outlives the daemon, and its turn end will still reach the eventer — the row simply stays
open until it does.

`delete` is offered but warns: it drops the record (and the run history's thread pointer), so a
failure has nothing to inspect afterwards.

**The trap that must be handled with it: auto-flagging.** Since H60 the daemon flags a thread on
*every* turn end, attended or not. A nightly scheduled spawn would therefore flag itself every
night and pollute the flagged set and the cockpit's `,`/`.` ring — the very ring H98 built to
hold "the threads I am juggling". So a spawn schedule **sets `flag_disabled` on the threads it
creates by default** (the existing `⌁` auto-flag-off state), with `--flag-on-end` to opt back in
for jobs whose whole point is to get your attention.

### 7.4 Relationship to `sesh delegate`

A spawn schedule with `--headless --on-turn-end stop+archive` *is* `sesh delegate` on a timer.
Two honest options:

1. **Implement it as such** — the scheduler, at fire time, performs the delegate composition
   (spawn → send → reap on the eventer edge) using the same primitives. No new spawn path; the
   only genuinely new code is the guard + the reaper wiring.
2. **Generalize `delegate`'s contract into the daemon** and have both the CLI verb and the
   scheduler use it.

Recommend (1) for phase 2 — it adds no abstraction — and note (2) as a refactor if a third caller
ever appears. What must NOT happen is a second, subtly different spawn path: that is the
`--machine X` failure class this project exists to prevent.

### 7.5 A third action worth knowing about (not phase 1 or 2)

`sesh schedule ticket --ticket <id> --cron …`: create a thread bound to a ticket and send the
ticket's prompt — i.e. "deploy this ticket every Monday". The ticket layer already has
`send-prompt` and binding; the action is a thin variant of `spawn`. Listed so the `action` column
is designed with room for it, not built.

---

## 8. Surfaces

### 8.1 CLI (`sesh schedule`) — one family, explicit subcommands

Separate subcommands per action rather than one mega-flag-set: half the flags would be invalid
for the other action, and `help_test.go`'s meta-tests require a `flagDoc` for every flag in a
command's usage line (so the usage lines stay honest only if they are separate).

```
sesh schedule message --id <thread> --text <s> (--cron <s>|--every <d>|--at <t>) [rules]
sesh schedule spawn   --agent <a> --cwd <d> --prompt <s> (--cron …) [spawn opts] [rules]
sesh schedule list   [--all-machines] [--thread <id>] [--json]
sesh schedule show   --id <sched>            # full record + next fire + last outcome
sesh schedule runs   --id <sched> [--limit N]
sesh schedule pause  --id | resume --id | remove --id
sesh schedule edit   --id <sched> [--cron …|--text …|--if …]
sesh schedule run-now --id <sched> [--force]   # fire immediately; --force bypasses guards
```

`run-now` is not a convenience — it is the only way to test a schedule without waiting for a
cron edge, and it is what makes the matrix cells fast.

Every subcommand needs: a `help.go` registry entry, a `flagDocs` entry per flag, a
`subcommandSets` entry in `help_test.go` (hand-maintained mirror — H89's gotcha), and the
`skills/sesh-cli/SKILL.md` section. That is a hard rule in AGENTS.md, not a nicety.

### 8.2 API

`api.SchemaVersion` 48 → 49, additive: a new endpoint family
(`POST /v1/schedules`, `POST /v1/schedules/remove`, `POST /v1/schedules/update`,
`GET /v1/schedules`, `GET /v1/schedules/runs`, `POST /v1/schedules/run-now`) plus client
methods. A pre-49 daemon 404s the routes loudly — mixed-mesh safe in both directions, and the
changelog paragraph must say so explicitly, as every other entry does.

Store: migration 26 (append-only). No change to `threads`, so no rehearsal risk beyond the usual
pre/post backup convention.

### 8.3 TUI and sesh-ui

Phase 3, and deliberately minimal:

- **No new gutter glyph.** The gutter is ten cells and its vocabulary is tightly managed (one
  stroke class per kind, the confusable-family guard — H97/H104). A schedule marker is not
  worth taxing every row in every view.
- **An opt-in column** (`sched`, showing the next fire relative — `in 12m`) in the
  `[tui] columns` set, exactly like `notify`/`tickets`.
- **The `I` details popup** lists the selected thread's schedules (it already lists tickets).
- Optionally a palette command `schedules` opening a viewer over the machine's schedules, in the
  shape of the `K` tickets view — only if it earns its place after living with the CLI.
- **sesh-ui** needs nothing at first (its API surface is unchanged); it can adopt the endpoints
  later. Worth a follow-up ticket, not part of this scope.

### 8.4 myrig

Nothing required. Once schedules exist, the natural policy-layer additions are a `prefix+m` menu
entry and a `sesh-notify`-style hook on a new `schedule_failed` event. Rendering and ergonomics
stay there; the mechanism stays here.

---

## 9. Failure, observability, safety

### 9.1 Every run has a recorded outcome

`fired` / `skipped:<reason>` / `failed:<error>`, with counters and the last N runs queryable.
A skip is **not** a failure and must not read as one — "skipped: busy" on a heartbeat is the
system working. But a schedule that has skipped 200 times in a row is *also* information (the
guard may never be satisfiable), and `schedule list` should show the streak.

### 9.2 Loud where it matters

- **Daemon start:** log every schedule whose occurrences were missed while down, with the count.
- **`sesh doctor`:** a check that reports schedules disabled by the failure breaker and schedules
  whose `next_fire` is more than one interval in the past (which means the loop is wedged). This
  is the H75 lesson — a subsystem that goes quiet must say so, because "no output" reads as
  health.
- **Hook events:** add `schedule_fired` and `schedule_failed` to `ValidHookEvents` so the
  existing notify machinery can toast a failure. Note the deploy coupling: the hook vocabulary
  and the binary must ship together, and a `[[hooks]]` entry naming an unknown event **refuses
  the daemon at start** — so config and binary deploy in one step (the H52 ordering).

### 9.3 The kill switch

`[schedules] enabled = false` in `config.toml` stops a machine firing anything, for a box where
unattended agent activity is not wanted (a test machine; the phone). Read at daemon start like
every other section.

### 9.4 Cascades and orphans

Deleting a thread deletes its `message` schedules **in the same transaction** as the record, and
says so in the delete output (`also removed 2 schedules`). Note the existing wart not to copy:
`DeleteThread` does **not** clean up `subscriptions` rows today, so orphan edges linger; a
schedule pointing at a deleted thread would fire into nothing forever, which is worse.

Archiving does **not** delete schedules (archive is reversible) — the archived-skip guard covers
it.

### 9.5 The security surface

Schedules run **only sesh actions** — no arbitrary shell (§10). But "spawn an agent with a
prompt, unattended, under `[spawn] mode = yolo`, every night" is a materially different risk
posture from doing it by hand, and the fleet's default is yolo. Mitigation is disclosure, not
restriction: record the effective spawn mode on the schedule, print it at creation
(`mode: yolo (from [spawn])`), and show it in `schedule show`. ⚑ §15.7.

---

## 10. What this deliberately is NOT

- **Not a general cron.** No arbitrary command execution. A schedule's action is a sesh verb
  against a thread. `atuin-vacuum` stays in crontab, and the doc should say so plainly, because
  "sesh has a scheduler now" invites exactly that migration. The daemon's HTTP API already
  carries RCE-equivalent power behind one bearer token (H73); adding a remote-managed
  arbitrary-command cron to it is a different product with a different threat model.
- **Not a job queue.** No retries with backoff, no dependencies between schedules, no fan-out to
  many threads from one schedule (⚑ §15.8 — a tag-targeted broadcast is a plausible future, and
  a dangerous one).
- **Not a replacement for subscriptions or hooks.** Those are edge-triggered; this is
  clock-triggered. They compose (a spawn schedule can subscribe its worker to a supervisor).
- **Not "agent keep-alive intelligence".** A heartbeat is a dumb timer with guards. If the real
  goal is "notice a stuck agent", the honest signal is the stall detection that already flags
  threads — a schedule is the blunt instrument, deliberately.

---

## 11. Alternatives considered

### 11.1 Cron + the existing CLI (the status quo)

Covered in §2.3. Verdict: **sufficient for spawn-shaped jobs, insufficient for anything
state-guarded.** The TOCTOU gap in `status && send` is the technical core of the argument; the
invisibility and per-machine sprawl are the ergonomic core. If only one half of this feature is
ever built, build the message half.

### 11.2 Put it in myrig (a shell loop / a rendered crontab per machine)

Rejected. Race-free state evaluation at the owner, persistence, cascade-on-delete and mesh
visibility are mechanism (SPEC §1), and a rendered-crontab-per-machine is the fleet sprawl that
`sesh master` and `sesh tui` were built to delete. A myrig wrapper over `sesh schedule` is
welcome; a myrig *implementation* is not.

### 11.3 A new hook event (`tick`) plus `[[hooks]]` policy

"Fire a hook every 5 minutes and let the shell decide" is superficially attractive: no new
records, no new CLI. Rejected because a hook command is opaque policy — it cannot be listed,
paused, edited, cascaded on thread delete, or reported on, and every user of it would re-implement
the guard logic in shell against a stale `--json` read. It also puts the TOCTOU race back.

### 11.4 Extend tickets with a schedule field

Tickets are single-writer on a canonical node and are *units of work*, not triggers; a schedule
targeting a thread must live with the thread's owner. But the overlap is real and productive in
the other direction: `schedule ticket` (§7.5) as a third action.

### 11.5 systemd timers / launchd per schedule

Rejected outright: six machines, three init systems (systemd, launchd, termux-without-either),
per-schedule unit files to render and reconcile, and no state guards. This is the shape myrig
already carries for daemons and it is exactly the wrong granularity for a user-created record.

---

## 12. Traps this codebase has already paid for, that apply here

1. **Stale busy in both directions** (H58, H64). Guard with dwell (§6.4), never trust a bare
   `idle` for an unattended action.
2. **A deadline with no write recomputes nothing** (H103's `nextHoldFlip`). The scheduler *is*
   that mechanism; its next-fire computation must be a pure, exhaustively-tested function.
3. **The pane paste concatenates with typed input** (§6.5) — `SendText` + Enter is not
   idempotent against a human.
4. **Auto-flag pollution** (H60, H98): unattended recurring turns flag themselves. §7.3.
5. **`thread info` is a diagnostic verb, not a field read** (H98 follow-up 2: 739 ms local /
   3.3 s routed). The guard evaluation must read the maintainer's published snapshot, never
   `thread info`.
6. **Do not add fields to the mesh snapshot casually** (MESH_SCALE): every field is re-transferred
   per changed row, per round, per peer.
7. **Config is read at daemon start only.** A `[schedules]` section needs a supervised restart;
   the schedules themselves therefore belong in SQLite with a CRUD API, not in config.toml.
8. **`pgrep -f` self-matches; kill by explicit pid** (H22/H74/H101) — relevant to `--max-runtime`
   enforcement, which must stop a thread through the `thread stop` verb, never by pattern-killing.
9. **The phone.** termux has no supervisor and the whole uid gets killed (H108). Schedules on
   termux fire only while the daemon happens to be up; `catchup: skip` plus the loud missed-fire
   log is the honest behaviour, and §9.3's kill switch is there for it.
10. **Mixed-fleet deploys.** Additive schema + a 404-ing route on old daemons; the changelog entry
    must state the safety in both directions, as every entry since 39 does.

---

## 13. Test plan (matrix rows)

Honest cells mean: a **real** clock (short intervals, not a mocked time), a **real** agent in a
real pane, a **real** ssh hop for remote, and the **observable external effect** asserted — the
agent's transcript actually received the text, the thread actually got archived.

| feature id | axes | what it proves |
|---|---|---|
| `schedule.crud` | agnostic × L,R | add/list/show/pause/resume/remove/edit over the real CLI, routed for remote; next-fire recomputed on edit; unknown guard word is loud at creation |
| `schedule.message` | c,co,pi × L,R | a real 5-second-interval schedule delivers into a real thread; `--when-headless revive` really revives (conversation continuity asserted); `skip` really skips |
| `schedule.guards` | agnostic × L,R | with a **genuinely busy** real thread, `--when-busy skip` does not deliver and records `skipped: busy`; `--idle-for` holds off until the dwell elapses; an on-hold thread is skipped; `--ignore-hold` overrides |
| `schedule.spawn` | c,co,pi × L,R | a real spawn at a real interval, real prompt delivered, `--on-turn-end stop+archive` really archives after the first turn, `--if-previous skip` really suppresses the second run while the first is alive |
| `schedule.catchup` | agnostic × L | stop the daemon, let an occurrence pass, restart: `skip` rolls forward and logs the miss; `once` fires exactly one catch-up |
| `schedule.lifecycle` | agnostic × L | deleting the thread removes its schedules; the disable-after-failures breaker trips and is visible |

≈ 24 cells. Plus unit tests that do not need the matrix: the cron parser (exhaustive, incl. DST
edges and leap years), `nextFire` as a pure function, the guard truth table, the rules
JSON validator, and the catch-up roll-forward arithmetic.

**Anti-gaming, per the house rule:** neuter the guard evaluation → `schedule.guards` must go red
with the delivery landing in a busy thread; neuter the reaper → `schedule.spawn` must go red with
the thread still active. Reverse each edit and re-run green before claiming anything.

Per H93's lesson, `schedule.message` and `schedule.spawn` carry the **agent axis** even though
the scheduling logic is agent-agnostic: revive, headless turns and turn-end detection all differ
per agent, and that is exactly where an agent-agnostic registration hid a real defect before.
`thread.delegate` is already registered with the full agent axis (`features.go`); the
`schedule.spawn` cells are that proof shape on a timer and should reuse its helpers.

---

## 14. Build order and effort

| phase | scope | effort |
|---|---|---|
| **1. Engine + `message`** | migration 26, api 49 + endpoints + client, the scheduler loop, the cron parser, guards (§6), CLI `message/list/show/pause/resume/remove/run-now`, help + flagdocs + SKILL, `schedule.crud` + `schedule.message` + `schedule.guards` + `schedule.catchup` cells | **medium-large** — comparable to H103 (hold release) or the subscriptions engine; one focused session plus a deploy |
| **2. `spawn`** | `schedule_runs`, the eventer reaper, overlap + max-runtime + name templates + flag-disable, CLI `spawn`, `schedule.spawn` + `schedule.lifecycle` cells | **medium** — reuses everything |
| **3. Visibility** | the two snapshot fields, the `sched` column, the details popup, `doctor` checks, `schedule_fired`/`schedule_failed` hook events | **small-medium** |

Deploy shape: phases 1 and 2 are store + daemon changes → rebuild **and** supervised restart on
all six, with the usual pre/post `VACUUM INTO` backups. Phase 3's snapshot fields are additive and
mixed-mesh safe; the TUI half is binary-only.

---

## 15. Open decisions for Lukas ⚑

1. **Scope now:** build both actions, or phase 1 (`message`) only and revisit `spawn` after
   living with it? (§0 — my read is that `message` is the one that must exist; `spawn` is cheap
   but genuinely substitutable by cron + `delegate` today.)
2. **`--idle-for` default.** Default 60 s whenever `--if idle` is used (recommended), or opt-in
   only? (§6.4)
3. **`--respect-typing` default ON at 60 s** for pane deliveries? (§6.5 — prevents submitting a
   half-typed line, at the cost of occasionally skipping a run while you are at the keyboard.)
4. **Cron parser:** vendor `robfig/cron/v3` (a 9th direct dependency) or hand-roll ~150 lines?
   (§5.1)
5. **Guard vocabulary:** the closed keyword list (recommended), or reuse the `[[tui.views]]`
   predicate language for richer expressions? (§6.3)
6. **Snapshot fields** `schedules`/`next_fire_unix` in phase 3 — accept the (small) replication
   cost so the TUI/sidebar can show a schedule marker? (§3.2)
7. **Spawn mode disclosure** — record + print the effective `[spawn]` mode per schedule
   (recommended), or additionally require `--yolo` to be typed explicitly on a scheduled spawn?
   (§9.5)
8. **Broadcast** — should a schedule ever target *many* threads (by tag)? Not designed here;
   powerful and easy to regret. (§10)
