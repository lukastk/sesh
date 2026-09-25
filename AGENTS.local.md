# AGENTS.local.md — sesh v2 working notes

**Older entries are archived in `_dev/AGENTS.local.archive.md`** (NOT imported into agent
context): H1-H33, the June 2026 build-out, moved 2026-08-22; H34-H90, the July-August 2026
entries, moved 2026-09-17. This file holds H91 onwards, plus the "Trap digest" and
"Reference" sections at the bottom. Nothing was lost - the moved entries are in the archive
in full and in git history.

## H112 — `sesh whoami`: the identity GATE, because `sesh info` is a DIAGNOSTIC and the two want opposite defaults; plus mysystem's `attach-thread` stops reading `$SESH_THREAD_ID` raw (2026-09-25, sesh <this commit> + mysystem <this commit>; NO schema/API/daemon change; sesh BINARY-ONLY, no daemon restart; mysystem needs a rebuild)
Bug report relayed by Lukas from a claude BACKGROUND JOB in `mosaic-v3/courses/finnish` — an agent
that is deliberately **not** a sesh thread. Its inherited `$SESH_THREAD_ID=c194478c` resolved to
`adi-requests`, a live headful claude thread in `~/dev/20260622_oo996d__ADI-website`; its process
ancestry reaches `systemd` directly (detached + reparented, carrying the launching pane's env). Its
point, which is the right one: **the resolvable-but-wrong case is worse than the unresolvable one.**
It had signed a message with its Claude Code JOB id, which failed loudly and was caught within the
hour; had it followed the standard advice and signed with `$SESH_THREAD_ID`, the message would have
read as coming from `adi-requests` and nothing would have questioned it.

**FIRST FINDING: H92 ALREADY REFUSES THIS EXACT CASE — verified live before touching code**, with
the reporter's real ids against the real mymain daemon: `sesh info` exits **1** naming both cwds
(`~/dev/20260622_oo996d__ADI-website` vs `~/dev/20260917_4cr9iq__mosaic-v3/courses/finnish`). The
cwd corroboration works. So the report's ask #3 (document it) was already done in
`skills/sesh-cli/SKILL.md`, and ask #2 (unset rather than leak) is **not sesh's to do** — sesh
injects the var into the pane; claude's machine-global `claude daemon run` froze it and spawns the
job. Say that plainly rather than build a half-fix.

**THE REAL RESIDUAL, and it is where the risk now lives.** env set + cwd **compatible** + no pane:
`source=env`, `verified=false`, **exit 0**, and the warning on **stderr only**. Measured live:
`sesh info --json | jq -r .thread.id` happily returns an unverified id. The documented guard —
`select(.source == "pane")` — is a ritual, and **the evidence is that rituals do not stick**: the
self-compact skill had to be patched for exactly this in H92, H95 is an agent discarding stderr in a
loop (`>/dev/null 2>&1`), and three mysystem skills still got it wrong today. An optional safe
reading is one nobody performs.

**THE FIX IS A SECOND VERB, NOT A TIGHTENED FIRST ONE** (conferred; Lukas took both recommendations).
`info` is a DIAGNOSTIC and `whoami` is a GATE, and they want **opposite defaults** — refusing to
describe a thread is exactly wrong when you are diagnosing, which is why `info` must keep exiting 0.
So `sesh whoami` runs the same resolution and answers with the **EXIT CODE**: stdout is the bare
uuid iff the identity is VERIFIED (the pane marker — the one source a process elsewhere cannot
inherit), else stdout is EMPTY and stderr says why. `TID=$(sesh whoami) || exit 1` is therefore safe
**by construction**, which the jq form never was. Three distinct refusals, each with its own text:
contradicted env id / uncontradicted-but-unverified env id (**absence of contradiction is not
evidence**) / no identity at all — the last being *an answer, not a failure*, and the one the
reporter is in: it tells the caller to say what it actually is and never borrow an id.
DESIGN POINTS: **no thread selector** (naming one makes the answer trivially "explicit"); **not
routable** — excluded from `routableSubcommand` so the flag survives into whoami's own flagset,
which refuses it with the REASON (a peer would read its OWN pane and env and answer confidently
about another machine — the failure mode itself, not a limitation); `--allow-unverified` (the
existing pseudo-global) downgrades it to info's tolerance. `noIdentityError` extracted beside
`unverifiedError` with **byte-identical text** (pinned by a test) so ~20 verbs that merely surface
it are unaffected — whoami needs the type because it has no `--id` and **a remedy the caller cannot
type is only half a loud error** (H95), asserted by a test that no whoami refusal may contain `--id`.

**MYSYSTEM WAS THE HALF ACTUALLY MISATTRIBUTING TODAY**, exactly as the reporter predicted.
`mysystem/src/commands/_thread-ref.ts` read `$SESH_THREAD_ID` raw and checked only the uuid SHAPE —
which cannot distinguish the real agent from a job carrying a stranger's id, both being well-formed
uuids naming real threads. So `ms attach-thread` on a note would have recorded `adi-requests` and
reported success. Now routed through `sesh whoami` (argv array via `execFileSync`, never a shell
string — the path rule), preferring `$SESH_BIN` over PATH, surfacing sesh's stderr **verbatim**
(it already names the thread you nearly became), and **deliberately NO fallback** to the raw var:
a fallback would restore the exact silent misattribution on the machines where verification is
hardest. The `whoami` seam is injectable so the contract is testable without a daemon.
**THE TESTS THAT EXISTED ASSERTED THE DEFECT** ("falls back to $SESH_THREAD_ID when no thread is
given" → `expect(success).toBe(true)`); rewritten to assert the refusal, for attach AND detach —
detaching the wrong thread is quieter than attaching one and therefore easier to miss.
**TEST TRAP worth keeping: vitest runs INSIDE my sesh pane**, so `sesh whoami` in a test resolves the
RUNNER's own live thread and the suite would have "passed" by attaching my real id. The harness now
strips `SESH_THREAD_ID`, `TMUX` **and** `TMUX_PANE` per test and restores them after.
Docs: `skills/myvault`, `skills/convo-review` (both the attach paragraph and the checklist line) and
`AGENTS.md`'s command list all said "uses `$SESH_THREAD_ID`" with no warning — corrected.

**GREEN.** sesh: `go vet ./...`; gofmt clean on every touched file (the 6 pre-existing
`internal/conformance/*_test.go` drift files are untouched — H48); every non-conformance package
plain, `cmd/sesh` + `internal/matrix` also `-race`; new cell `thread.whoami/-/local`; the inference
BLAST RADIUS — `thread.info` local+remote, `thread.parent`, `ticket.list-current`,
`thread.subscribe`, `daemon.hooks` — and, per H92's lesson that `-run TestMatrix/…` **excludes every
non-matrix test in the package**, `TestEmptyIDFlagIsLoud` / `TestTailCLIForms` / `TestEmptyThreadName`
explicitly. mysystem: the FULL suite, 249 tests / 22 files.
**THE FULL 280-CELL MATRIX WAS NOT RUN — do not read this as all-green.**
ANTI-GAMING, each reversed and md5-verified byte-identical (H44): letting an uncontradicted env id
pass the gate reddens the cell verbatim ("whoami accepted an identity resting on an inherited
$SESH_THREAD_ID … absence of contradiction is not evidence"); dropping `whoami` from
`routableSubcommand` reddens it at the routing refusal ("got: unknown machine \"somepeer\"");
restoring mysystem's raw-env read reddens the seam test AND both attach/detach regressions.
**THE CELL IS BUILT SO IT CANNOT PASS AGAINST A WHOAMI THAT IS A THIN ALIAS OF `info`**: each
refusal is asserted alongside the corresponding `info` call SUCCEEDING on identical inputs, with the
`info` leg a loud PRECONDITION failure if it ever stops — without that pairing the cell proves
nothing. Registered LOCAL-only by design (the `thread.placement` precedent): whoami reports who the
CALLING process is, so the question has no remote form; the `--machine` refusal is asserted rather
than the axis quietly dropped.

**LIVE-PROVEN read-only against the real mymain daemon**, all four cases: the reporter's exact
scenario refused naming `adi-requests`; the RESIDUAL shown as the money shot — `info` exit **0**,
`whoami` exit **1** on byte-identical inputs; no-identity refused; and from THIS agent's own real
pane `sesh whoami` → `43b1b376-…` exit 0, `TID=$(…)` captured it, `--json` reporting
`source=pane verified=true`.

DEPLOY: **sesh is BINARY-ONLY — no schema/API/wire change and nothing daemon-side** (the resolver is
CLI-side), so a mixed fleet is trivially safe: a machine on the old binary simply has no `whoami`.
mysystem needs a `npm run build` wherever its CLI is installed.

## H111 — SCHEDULED WORK BUILT: `sesh schedule` (cron/interval/one-shot messages into a thread + thread spawns, state-aware guards, catch-up, a reaper) AND the `respect-typing` guard on EVERY paste into a live pane (2026-09-16, sesh feat/scheduling → merged; store migration 25→26, api 48→49; DAEMON rebuild + RESTART ALL SIX; ticket 28f6e77b done)
Lukas's ticket: cron-style messages to a thread with rules (revive-if-unattached, only-if-not-
running = a heartbeat), cron-style thread spawns with rules (kill+archive on turn end, attached vs
not), "might be one or two features, I'm not sure — scope it thoroughly." Design record:
`_dev/SCHEDULING.md`; BACKLOG #8; a row in AGENTS.md's `_dev/` table.

**THE VERDICT.** ONE mechanism, two actions (`message`, `spawn`) sharing the clock, catch-up,
bounds, outcome history, CLI family and — load-bearingly — the guard vocabulary over the existing
state axes. `message` is the half only sesh can do (state guards evaluated at fire time on the
OWNER, atomically with the send; `sesh thread status && sesh thread send` from cron has a real
TOCTOU window and the failure is interrupting a running turn). `spawn` is mostly convenience —
`0 9 * * 1-5 sesh delegate …` in a crontab covers ~80 % today — so it lands second.
**VOCABULARY CORRECTION that shaped every flag name:** the ticket's "attached/unattached" means
the HEAD axis (headful/headless — the daemon can revive a pane, it cannot make a human look), not
sesh's attachment axis; the same correction H89 made for shell threads. The real attachment axis
IS used, for a different guard (below).

**MECHANISM, in one line each:** a schedule is a row in the EXECUTING machine's own `sesh.db`
(target thread's owner / the spawn machine; owner-local like tickets/subscriptions, NOT in the
mesh snapshot, fan-out for `list --all-machines`, cascade-deleted with the thread — NB
`DeleteThread` does not cascade `subscriptions` today, a wart not to copy); a fourth
`{start,stopAndWait,run,tick}` loop polling at 1 s against `next_fire` (not the maintainer's tick
— that loop is the hottest and a revive takes seconds); a pure hand-rolled 5-field cron
`Next(spec, tz, after)` with the tz RECORDED at creation (a routed create means the OWNER's
zone); catch-up `skip` (roll forward + loud miss count) or `once`, never "fire all missed";
guards `--when-headless {turn|revive|skip}`, `--when-busy {skip|send}`, `--if <closed list>`,
`--idle-for` (60 s implied by `--if idle` — the H58/H64 stale-busy classes make a bare idle read
unsafe), hold and archived skipped by default; spawn: `--if-previous skip` keyed on RUNTIME only
(headful or busy — with `keep` as the default a finished run is an un-archived headless·idle
record, so "record exists" would suppress every later run), `--on-turn-end` via the eventer's
busy→idle edge + a persisted `schedule_runs` row (a headless run's turn is a daemon goroutine and
DIES with the daemon — seed-time reconciliation marks it failed; a headed run's pane outlives
it), `--parent <virtual thread>` to group runs, `flag_disabled` on headed runs (H60 flags every
unattended turn end — a nightly job would flag itself nightly; headless turn ends never flag,
H52). `--msg`/`sendWhenReady`'s 90 s readiness timeout is a LOG LINE only — a scheduled headed
spawn must own that wait and record `failed`. No arbitrary shell, ever (the API is already
RCE-equivalent behind one token, H73). ~24 matrix cells across six rows; `message`/`spawn` carry
the agent axis per H93's lesson.

**THE LIVE FINDING (phase 0, ships FIRST, no schema):** `SendText` pastes then sends Enter, so a
delivery into a pane where a human is mid-line submits the CONCATENATION. Lukas confirmed it bites
him today — child threads reporting into a supervisor he is typing in. H48's
`attached_activity_unix` (newest tmux `client_activity` = last client INPUT) is the signal. Fix
belongs on the delivery primitive, not the scheduler: one daemon helper above `SendText` for the
THREE thread-level paste sites (`handleThreadSend`, `handleTicketSendPrompt`, `sendIntoThread`;
not `sendWhenReady`, not the raw `tmux send-text`), semantics WAIT-not-drop until 60 s of quiet
(live `list-clients`, ~2 s poll), bounded 10 min, at the bound fail LOUDLY + auto-flag the target
with the reason (never paste anyway — that is the collision again, silently). Built-in 60 s
default, `[send] respect_typing`, per-call `--respect-typing <dur>` (`0` = off); the CLI loops
like `waitLoop` because of the 15 s client timeout. Known edge stated, not hidden: PgUp counts as
input.

**DECISIONS (all nine settled with Lukas the same day, AskUserQuestion + follow-up):** both
actions, message first; `--idle-for` 60 s implied; respect-typing ON and generalised; hand-roll
the cron parser; spawn defaults headless + `--on-turn-end keep` (his call, reversing my
`stop+archive`); closed guard list; the two TUI snapshot fields in phase 3; spawn-mode disclosure
only; no broadcast. Nothing open. Build order §14: 0 respect-typing → 1 engine+message → 2 spawn
→ 3 TUI/doctor/hook events.

**METHOD NOTE:** two Explore agents mapped the spawn/send/revive paths and the daemon's
time-driven machinery in parallel while I read the design corpus; the map is what let the doc
name real seams (`sendIntoThread`, `nextHoldFlip`, `reviveThread`'s in-flight reservation, the
five `SendText` call sites) instead of inventing them.

### H111 — the build, the same day (Lukas: "proceed with the building. Build the whole thing.")
Two commits on top of the scope: the typing guard (phase 0) and the scheduler (phases 1–3).
`_dev/SCHEDULING.md` §16 lists every place the build departed from the design; the durable
ones:
- **`thread send`'s typing guard is DAEMON-OWNED, not a CLI loop.** A child agent's `sesh thread
  send` runs inside a Bash tool with its own timeout (Claude Code: 2 min), so a CLI blocking
  up to 10 min would be killed mid-wait with the message lost and nothing recorded. So the
  daemon queues a held delivery (per-thread FIFO, one drainer, 2 s polls of live
  `list-clients`), returns `deferred <id>` at once, pastes when quiet, and at the deadline
  FAILS + auto-flags the target with `undelivered message from <sender>: …` — never pastes
  anyway. `--wait` callers get the loop (`on_typing=wait`, ≤8 s per call under the 15 s client
  timeout); `--on-typing skip` refuses (what the scheduler uses). Held deliveries are
  in-memory; shutdown logs each loss. Three thread-level paste sites go through it
  (`handleThreadSend`, `handleTicketSendPrompt`, `sendIntoThread`); `sendWhenReady` and the raw
  `tmux send-text` do not.
- **The scheduler reads HEAD live and a headless target's BUSY from the turn registry**, not
  the maintainer's row — a thread stopped a moment ago still reads headful there and a just-
  completed headless turn still reads busy (300 ms), and both produced wrong deliveries in the
  first cell runs. Record fields (archived/flagged) come from the record; `on_hold` stays the
  maintainer's (it walks the whole record set — the guards cell waits on the SNAPSHOT for it,
  via a decoded `sesh mesh --json`, which is PRETTY-PRINTED so a `"on_hold":true` substring
  never matches — H55 had recorded this and I re-hit it).
- **Missed = due more than 90 s ago** (`scheduleMissGrace`), not "the daemon restarted": correct
  after suspend/resume with no notion of having slept. catchup skip counts and rolls forward
  (13 = the due one + 12 slept through in the unit test); once fires exactly one run.
- **The reaper accepts a busy→idle edge as a turn end only after a REAL turn** (reported
  authority, or ≥3 s busy): a heuristic pane flickers busy→idle between the paste and the
  agent's first render. Spawn runs write their run row BEFORE the turn (restart-safe);
  `record()` stamps it rather than inserting a second. `run-now --force` on a `--when-headless
  skip` schedule delivers a headless turn (force means deliver). The effective spawn mode is
  recorded as `default` when the agent's own. A schedule write bumps the `threads` rev too so
  the maintainer restamps the digest (`schedules`/`schedule_next_unix`) the TUI's `sched`
  column reads — and `meshRow` had to copy them (the sched-column claim caught the gap, H91's
  one-conversion lesson again).
- **Test fixture traps:** the paste fixture's pane runs `cat` under argv0 `pi` (a symlink) so
  the runtime resolver sees an agent, with stdout to /dev/null so a paste echoes ONCE (cat's
  own output doubled every line and looked like a double paste); `send-keys -t =viewer_x`
  fails where `-t viewer_x:` works; the viewer's own keystroke echoes latch the busy heuristic,
  so a "still idle while held" assertion is impossible — the pane's text is the only honest
  signal. The lifecycle cell's deterministic failure is codex's revive-before-first-turn N/A
  (a fresh codex headless thread CAN take a first headless turn, so that path is not a
  failure).
- **The AGENTS.local.md numbering collided again** (H80/H87 class): a concurrent session landed
  H110 (claude trust pre-seeding, 138be35) and deployed it to all six while this ran; rebased
  onto it (one conflict, this file), renumbered mine to H111.

**GREEN:** every non-conformance package plain + `-race`, `go vet`; the FULL TUI claims suite
(236 s, incl. the new `sched-column`); 20 new cells serially (`thread.send-respect-typing` ×2,
`schedule.crud/guards` ×2 each, `schedule.message/spawn` ×6 each, `schedule.catchup/lifecycle`
×1 each); phase-0 blast radius (send.headful ×6, ticket.send-prompt ×6, subscribe ×2, send-wait
×6, spawn-mode ×3, delegate ×6 — the two claude delegate cells failed once in the batch on a
transient claude refusal and pass alone); post-rebase: schedule.message/claude, spawn/claude,
respect-typing, thread.claude-trust ×2. **FULL 277-CELL MATRIX (62 min): 276 pass, 1 fail, 0
skip, 0 missing, 0 not-run, 2 n/a — the one red, `thread.model/claude/local` ("override model
leaked before it was requested"), reproduces BYTE-IDENTICALLY on a clean origin/main worktree:
pre-existing (claude's transcript now names the override alias unprompted, the H79 fixture
class), not this branch's, not fixed here.** ANTI-GAMING, each reversed byte-identically (md5):
paneQuiet forced true → respect-typing cell red "was not deferred: sent"; the busy guard
neutered → guards cell red "busy target: fired: pane"; the reaper neutered → spawn cell red
"run never ended with the keep policy"; missed detection neutered → catch-up unit red
"missed=0". Migration 26 REHEARSED against a `VACUUM INTO` copy of mymain's live store: 25→26,
2,057/407/80 byte-identical, tables + triggers present.

**DEPLOY RESULT (2026-09-16, merge 7100afc): LIVE ON ALL SIX.** Merged `--no-ff` to main and
pushed, then per machine: `VACUUM INTO` pre backup with counts → clean checkout pulled to
7100afc (every one was clean) → native build (`/opt/homebrew/bin/go` on the Macs) → `.new` +
`mv` → `supervisorctl restart sesh-daemon` → `daemon status` api 49 / store 26 → post backup.
Pre→post counts BYTE-IDENTICAL everywhere: **mymain 2,057/407/80; macbook 125/44/0; macstudio
26/1/0; pocket4 39/5/0; ideapad 7/2/0; termux 0/0/0** (`~/.sesh/backups/sesh-{pre,post}-v26-
h111-20260916.db`; termux has no sqlite3 → plain file copies, its store is empty, and its old
daemon 31238 was killed by its own reported pid after the build, the zshenv guard relaunching
31831 on the next login with the new inode). Every binary `vcs.modified=false`. Mesh after: all
five API peers reachable, synced ≤1 s; doctor exit 0 (`✓ schedules 0 enabled`). LIVE-SMOKED on
the supervised mymain daemon: a disposable headless pi thread + `schedule message --every 10s
--when-headless turn --max-fires 1` fired on its real clock (`fired: headless turn`), the real
turn replied `SMOKEBEAT`, max_fires disabled it, `schedule list --machine ideapad` routed, and
`thread delete` reported `also removed 1 message schedule(s)`. NB mymain's recorded zone is
`Etc/UTC` (the box runs UTC) — a schedule typed there in wall-clock terms is UTC unless `--tz`.
myrig needs NOTHING: `[send]` and `[schedules]` have built-in defaults (60 s / 10 m; enabled,
breaker 10); document them in `config.toml.jinja` only if you want them visible.

### H111 follow-up — the two loose ends fixed (2026-09-17, sesh a1c2a66; NO schema/API/daemon change; BINARY-ONLY, no restart; NOT YET DEPLOYED)
Both were recorded as known-bad in H111's summary rather than quietly dropped, so they were the
next thing asked for.

**1. `thread.model/claude/local` was a WRONG TEST, not a flake — and it was red on main before
the scheduling branch.** It asserted "the transcript contains `haiku` and NOT `sonnet`". But a
claude transcript embeds the **Agent tool's own schema** (`"enum":["sonnet","opus","haiku",
"fable"]`) and the model-id line from Lukas's global CLAUDE.md, so "sonnet" appears in a HAIKU
turn's transcript. PROBED before touching anything: a real `claude --print --model haiku` turn
wrote 2 lines matching `sonnet` while every assistant message carried
`"model":"claude-haiku-4-5-20251001"`. The substring never proved anything; it went red the day
the ambient context grew a model list. FIX: `Sandbox.transcriptModels(id, agent)` reads the
model id off ASSISTANT messages only — claude `{"type":"assistant","message":{"model":…}}`, pi
`{"type":"message","message":{"role":"assistant","model":…}}` (both shapes verified against real
on-disk transcripts) — and the cell asserts the LAST assistant message per turn: turn 1 names the
pinned model and NO message names the override yet (the disjointness check, now over model IDS),
turn 2 names the override and adds a message. Strictly STRONGER than the text match: it names the
model that actually served the turn. ANTI-GAMING: dropping `modelArgs` from the claude headless
branch turns it red naming the real model (`pinned model "haiku" did not run: the turn's
assistant message names "claude-opus-5"`) — a message the old form could not have produced;
reversed byte-identically (md5). All three model cells green.

**2. The `I` details popup now SCROLLS** (H88 recorded the defect and excluded it from
`TestPopupFramesFitPaneHeight`). It rendered its whole field list unconditionally, so on a short
pane the frame outgrew the height and bubbletea dropped the TOP lines — the title and the id, the
very fields you open it for. FIX = helpView's treatment: `Model.detailsFields()` is now ONE source
for the renderer and the scroll budget (a second hand-written count is the H41 drift class),
plus `detailsChrome`/`detailsVisibleRows`/`detailsMaxOffset`, a `detailsOffset` reset on open AND
close, always-present ▲/▼ indicators, a clamped offset, the VALUE clipped to the remaining width
by exact rune arithmetic (values carry no ANSI, so no stripping is needed), ↑/↓ + j/k + ^j/^k
half-page, and the WHEEL (a scrollable list that ignores the wheel is half-built). The guard's
exclusion comment is GONE, replaced by three details cases (plain, meta + a long cwd, and a
past-the-end offset that must clamp). ANTI-GAMING: rendering the full list again reproduces the
original defect verbatim (`details at height 8 rendered a 26-line frame`); making ↓ inert turns
the claim red (`scrolling never reached the last fields`); both reversed byte-identically.
LIVE-PROVEN read-only in an isolated 12-row tmux against the REAL mymain daemon: `thread details ·
mysetup`, the id on screen, `▼ 15 more`, and ten ↓ presses walking to `notify` with the title held.

**TEST STATE, honestly.** Green: every non-conformance package plain AND `-race`, `go vet`, the
FULL TUI claims suite (240 s), `thread.model` ×3, and **183 matrix cells reconfirmed in groups** —
tmux+master (31), schedule+shell+ticket (…), daemon/api/mesh/route/blob/fs/plugins, and thread
group 1 (78) — **0 failures**. NOT reconfirmed end-to-end: the remaining real-agent `thread.*`
groups (send/state/resume/fork/subscribe/await/delegate…), which these changes do not touch. The
last FULL 277-cell run on this branch was 276/277 with the single red being exactly the model cell
fixed here. **Do not read this as a fresh all-green grid.**

**TWO PROCESS MISTAKES WORTH NOT REPEATING.**
- **My own cleanup killed my own tests.** I started a background sweep of ~2,700 stale
  `/tmp/tmux-1000/sesh-test-*` socket files, then launched a matrix chunk BEFORE the sweep's
  completion notification arrived — the sweep was still deleting sockets the running cells had
  just created, and 27 tmux/master cells failed in 0.01 s each. They pass on a quiet box. The
  H101 lesson in a new costume: a sweep matching a generic pattern must FINISH before anything
  that creates matching names starts. (The sockets themselves were stale FILES, no live servers —
  `pgrep -x tmux` found 5 processes, none a test server — so nothing was leaking memory, but the
  file count had grown across many sessions' runs.)
- **`rm -f $D/chunk*.log` with no matching file ABORTS the whole zsh line** (NOMATCH), so an
  `&& for …` chain silently ran nothing and only the trailing `echo` fired — the log said
  "ALLDONE" with zero tests run. The H30/H49 family again. Quote the glob or use `setopt
  NULL_GLOB`; and never gate a long run on a bare glob.

**CONCURRENT WORK ON THE BOX (not mine, and it explains the kills).** The harness killed three
heavy real-agent runs citing "low memory". Twice `free` showed ~12 GB available (H109's "the
harness's own heuristic"), but the third coincided with genuine pressure: another session was
running **`cd ~/mysetup/myrig && ./install.sh mymain` over ssh from macbook**, which restarted
supervisord and EVERY supervised program — sesh-daemon among them (pid 3491022 → 2861940,
uptime 1 d → seconds). That is what restarted the daemon mid-session, not my tests; the daemon
came back healthy (api 49, store 26, all five peers reachable) on `657ae69`, which contains H111's
merge. Each kill left 1–3 sandbox daemons, all killed by EXPLICIT pid after verifying their
`SESH_HOME` was under `/tmp/sesh-sb-*` (never `pkill -f` — H22/H74), with the live daemon verified
untouched each time. LESSON: check for a running `install.sh`/deploy on the box before launching
a real-agent matrix batch; two of them on one machine is what made the box thrash.

## H110 — EVERY NEW CLAUDE BOX OPENED ON THE "Quick safety check" TRUST DIALOG: Claude Code 2.1.27x stopped inheriting trust across a GIT ROOT; fix = pre-seed `projects[cwd].hasTrustDialogAccepted` in `~/.claude.json` at every headed launch, the claude twin of EnsureCodexTrust (2026-09-16, sesh 138be35; NO schema/API/CLI change; DAEMON rebuild + RESTART; **DEPLOYED ALL SIX**; ticket 4b069b88 done)
Lukas: "it seems to happen every time I open up a new Claude Code session in a new folder …
often I want to … spawn a handful of threads and then send a message directly to them. This
prompt kind of messes that up."

**ROOT CAUSE IS UPSTREAM, AND IT IS A ONE-LINE DIFF IN CLAUDE'S OWN LOGIC.** Read from the
bundled JS of both installed versions (`strings` on `~/.local/share/claude/versions/*`):
2.1.228's trust check walked the cwd's ancestors all the way to `/`, so `/home/lukastk`
being trusted (it is — `projects["/home/lukastk"].hasTrustDialogAccepted: true`) covered
every directory under it. 2.1.273's walk is BOUNDED BY THE ENCLOSING GIT ROOT (`_0` →
`RI(r)` = git root, `m0` stops there), so a fresh box — its own repo — inherits nothing and
prompts. `--dangerously-skip-permissions` does not touch it (the check is `wpe()`:
`CLAUDE_CODE_SANDBOXED` env, in-session accept, non-interactive, exact project entry, then
the bounded walk — nothing else). No env var or settings key exists for it; claude's own
error text names the remedy: `set projects[<dir>].hasTrustDialogAccepted: true in
~/.claude.json`. PROVEN in isolation before touching code (own `CLAUDE_CONFIG_DIR` with the
real config's `projects` map emptied + credentials symlinked, exact sesh argv): a non-git
dir under the trusted home → NO dialog; a `git init`'d sibling → the dialog. Then the fix,
against the REAL config: a pre-seeded fresh repo came up at the input prompt while the
unseeded control got the dialog. Every probe was torn down and the two probe entries
removed from `~/.claude.json` (701 projects, 82 top-level keys, verified after).
`hasTrustDialogAccepted` is true on only 56 of the 701 recorded projects — the other 645 ran
fine on inherited trust for months, which is why this looked new.

**TWO DIALOGS THAT ARE NOT THIS ONE, so nobody chases them:** (a) "Allow external CLAUDE.md
file imports?" — it showed up in the isolated probe because the probe's config dir had no
per-project approval; with the REAL config it does not fire for a new box (the user-level
`~/.claude/CLAUDE.md` `@~/.pi/agent/AGENTS.md` import is not what triggers it — only 3 of
701 projects ever saw it, all with a PROJECT CLAUDE.md importing outside the tree). Not
pre-approved by sesh, deliberately: that is a security choice, not a workflow nuisance.
(b) the "Bypass Permissions mode" disclaimer — global, already accepted on the fleet
(`skipDangerousModePermissionPrompt: true` in myrig's settings.json).

FIX. `internal/agents/claude/trust.go`: `GlobalConfigPath()` (`$CLAUDE_CONFIG_DIR/.claude.json`,
else `$HOME/.claude.json` — a SIBLING of `~/.claude`, not inside it) + `EnsureTrust(path, cwd)`.
`prepCodexEnv` became `prepAgentEnv` (claude → EnsureTrust; codex → the old body), still at
the same three call sites: `thread new`, `--into-pane`, and revive (`headful`/`resume`).
Headless turns never see the dialog (non-interactive is trusted by claude's own rule). The
file is claude's LIVE STATE — 1.4 MB, rewritten by every running session — so the edit is
minimal by construction: top level and `projects` are decoded as `json.RawMessage` maps and
only the target entry is opened (no float64 round-trip of ids/timestamps, no reordering of
untouched records); an unparseable document is REFUSED loudly, never overwritten (a torn or
corrupt file must not become an empty one); the write is temp-file + rename at 0600, the
same atomic shape claude uses (its own crash leftovers `.claude.json.tmp.<pid>.<hex>` are
still in the home dir from Aug 6); already-trusted is a pure read. The cwd's realpath is
seeded too when it differs: claude keys a repo by its CANONICAL root and compares the
as-given cwd against it, so both spellings are the same directory to it. A missing cwd is a
loud error at prep (the spawn would fail later anyway).
Residual, stated: read-modify-write races with a concurrent claude session are a lost-update
class claude's own multi-process design already has (every session RMWs this file); sesh
adds one RMW per NEW directory and none afterwards.

TESTS. Units: config-path truth table; keys (clean, symlink → both, relative/missing refused);
create-with-mode; EVERYTHING-ELSE-PRESERVED (big ints, nested unknowns, other projects'
records byte-identical via `json.Compact` — NOT a decode, which would itself mangle the ints
the test guards); already-trusted leaves bytes AND mtime untouched; false→true, missing/null
`projects`; five corrupt shapes refused with the bytes unchanged and no temp file leaked.
New matrix row **`thread.claude-trust`** (claude × local + remote, real ssh hop): a real
claude in a real `git init` box against an ISOLATED `CLAUDE_CONFIG_DIR` that trusts NOTHING
(`setupClaudeConfigDir`: credentials symlinked — the setupCodexHome precedent — the real
config minus `projects`, and the bypass disclaimer pre-accepted so the only dialog left is
the one under test). Asserts the entry on disk, the pane NOT showing the dialog, and a
`thread send` fired straight after readiness being ANSWERED (a computed sentinel, since the
prompt itself is echoed in the pane); then stop → delete the entry behind claude's back
(a pre-fix record) → `headful` → all three again. **`waitThreadReady` CANNOT catch this
bug**: the dialog is a rendered, byte-stable pane, i.e. exactly "TUI up, idle" — which is
why every existing claude cell stayed green (their `/tmp` cwds are non-git and inherit
`/tmp`'s trust). ANTI-GAMING (reverse-edited, md5-verified restore — H44): neutering the
seeding reddens the cell at the on-disk check; neutering that check too reddens it at the
pane with the report VERBATIM ("parked on its workspace-trust dialog ("Quick safety
check")"). GREEN: `go vet ./...`; every non-conformance package plain, the touched ones
`-race`; cells thread.claude-trust ×2, thread.new.headed ×4 (claude ×2, codex, pi),
thread.resume claude ×2 + codex, thread.codex-session-capture, thread.spawn-mode/claude.
`thread.resume/codex/local` failed ONCE at 111 s then passed 3/3 at ~22 s — the real-codex
turn-timeout flake class (H62/H93), not this change (the codex prep body is untouched).
The full matrix was NOT run.

DEPLOY: daemon-side (the seeding runs in the owner's spawn path) ⇒ rebuild AND supervised
restart; no schema/API/wire change, so a mixed fleet is safe. Nothing retroactive is needed:
a pre-fix thread in a box gets seeded on its next revive (the cell's second half).
**DEPLOY RESULT (2026-09-16): ALL SIX at 138be35, every binary `vcs.modified=false`** —
mymain (local), ideapad/pocket4/macbook/macstudio (one piped `zsh -ls` script each: clean
checkout verified BEFORE pulling — H49/H63 — then build → `.new`+`mv` → `supervisorctl
restart sesh-daemon`), termux (plain `go build`, old daemon killed by EXPLICIT pid 12793,
the zshenv guard relaunched pid 31238, `/proc/<pid>/exe` re-read to confirm the new inode).
NB termux's `ssh-target` no longer leaks an ssh-agent — Lukas landed the one-agent-per-
device fix in termux.sh (H108 follow-up 3's open item is closed). API 48 everywhere, mesh
all reachable. LIVE-SMOKED on the supervised mymain daemon against the REAL `~/.claude.json`:
a fresh `git init` dir under `~/.cache`, `thread new --agent claude` → the entry landed
(`projects` 701→702, nothing else of sesh's), NO dialog in the pane, and a `thread send
--wait` fired immediately after was answered ("SMOKE-42"). Thread stopped+deleted, dir
removed, the entry removed again (701 projects, 82 top-level keys, verified). Concurrent
session landed H109 (the status-options daemon) mid-flight — rebased, renumbered to H110. (2026-09-15; record only, NO code change). **THE HEADLINE BELOW WAS WRONG — READ FOLLOW-UP 3 FIRST:** the "193 MB, ~2 %, victim not cause" reading missed ~2,000 leaked ssh-agents (~1.9 GB) that are invisible from inside Termux. Original title: the whole Termux uid is 193 MB, ~2 % of the ~10 GiB other apps hold in zRAM — so the recurring whole-Termux deaths are lmkd victims, not a sesh cost, and nothing inside the uid can self-heal them (termux sshd DOWN at the time of writing)

## H109 — THE STATUS ROW WITHOUT A SHELL PER REDRAW: the daemon stamps `@sesh-name` & co. as PANE user options and the work conf renders a pure format; measured 0 status-shell spawns per 20 s on mymain (was 12) (2026-09-16, sesh cfc4fa1 + myrig 149483a; NO API/wire/schema/CLI change; DAEMON rebuild + supervised RESTART + work-conf re-source; **DEPLOYED ALL SIX** — termux ~1 h after the others, once the phone was back on the tailnet)
The foldable thread's second open item after the ssh-agent fix (myrig 332a403): "tmux.work.conf's
status line still forks a login zsh about once a second while the cockpit is attached … pushing the
status into a tmux user option from sesh would remove the spawns entirely. That second one is
squarely your territory." BACKLOG #7, now built. Design record: `_dev/STATUS_OPTIONS.md`.

**DESIGN.** The row shows RECORD fields only (name, agent, tags, archived, flagged, flag-disabled),
so the owning daemon's maintainer stamps them on every `@sesh-thread-id`-marked pane as six PANE
user options (`@sesh-name`, `@sesh-agent`, `@sesh-tags`, `@sesh-archived`, `@sesh-flagged`,
`@sesh-flag-disabled`; booleans `1`/empty) and the conf renders `#{?@sesh-name,sesh: #{@sesh-name}
[#{=8:@sesh-thread-id}] · #{@sesh-agent}…,}` — lookups only, the id from the existing marker. PANE
scope and nothing coarser: tmux inherits user options during format expansion (the H89 trap), so a
session-scoped option would render a neighbour's thread on an unmarked pane. Mechanism
(`internal/daemon/statusoptions.go`, one call per tick after the sweep): desired = f(record index,
pane index) — both already in hand; diff against `paneStatus` (last written); ONLY changed panes go
to tmux, in ONE invocation (`tmux.ApplyPaneOptions`, argv cut under 8 KiB — the whole list is one
16 KiB imsg, H90), followed by `refresh-client -S -t <client>` for every attached client so a
rename/flag repaints at once (names from the SAME `list-clients` call that feeds the attachment
axis — `AttachedClients`, no extra enumeration); a still-live pane that lost its marker is cleared,
a gone pane forgotten (the set of live pane ids now falls out of `RuntimeIndex`'s walk); a
zero-thread machine pays nothing (one clearing walk while something is cached, then never). Per
tick when nothing changed: a map walk and a struct compare per marked pane, no tmux call.
Rendering policy stays in myrig — sesh publishes data. The fallback shape (`#(sesh tmux status …)`,
one exec per redraw) was not built.

**MEASURED tmux FACTS the design leans on (3.6b here, 3.7c on the phone):** `#{?@opt,…}` reads an
empty option as false (so an unmarked pane renders nothing); the `=8:` length modifier works on user
options; a comma inside a VALUE is safe (the conditional splits before substitution); a literal comma
inside a branch is `#,`; a `#[…]` in a name styles the line exactly as it did through the shell job
(not escaped); `set-option -p -u` on an absent option is rc=0; a lone `;` value must travel as `\;`.
TWO ABORT SEMANTICS of a command list, both handled: a vanished pane stops the list AT its
sub-command with **`no such pane: %N`** — NOT capture-pane's `can't find pane` wording — so the
panes before it landed, it is dropped and the rest retried (the CapturePanes pattern); a detached
client fails only the trailing `refresh-client` (`can't find client`) — every write precedes the
refreshes, so they all count as applied and the next beat repaints the rest.

**TESTS.** Units: `internal/tmux` (real server — write, empty value, `;`, a vanished pane in the
MIDDLE of a batch with the panes after it still landing, a detached client in the refresh list,
chunking under the budget, `AttachedClients` naming a real nested client); `internal/daemon` (real
maintainer + store + pane — stamped after one tick, `statusWrites` unchanged across unchanged ticks,
rename-to-`;`/tags/flag/archive/flag-disable each within a tick, cleared on unstamp, restored on
re-stamp, cleared by the zero-thread early-out after the last delete and never touched again).
Matrix row **`tmux.status-options`** (agent-agnostic × local/remote): a REAL pi thread; options
appear; the contract format (pinned as `statusRowFormat`, asserted to contain no `#(`) renders the
exact row via `display-message -p -F`; rename/tag/flag/archive through the real verbs (real ssh hop
remote) change the rendered row within a tick; unstamping empties it. ANTI-GAMING: sync neutered →
local cell RED "the daemon never stamped @sesh-name on the thread's pane"; restored BYTE-IDENTICAL
(md5 de4e38a0…) → 2/2 green. GREEN: vet, gofmt (touched files), tmux/daemon/matrix plain + `-race`
(sequential), every non-conformance package plain, blast radius = every `tmux.*` cell (24),
thread.runtime-state/pi ×2, thread.flagged/pi ×2, shell.lifecycle ×2, daemon.doctor, daemon.hooks
×2, thread.state-authority/pi ×2 — all pass. **The full 257-cell matrix was NOT run** — do not read
this as all-green.

**myrig 149483a.** `tmux.work.conf` status-format[0]'s thread row is now the format (nested inside
the termux dictation-banner conditional; verified on a blank isolated server for unmarked / marked /
flag-disabled / banner-on: identical text, glyphs, colours to the shell version); a comment forbids
a `#()` job ever coming back. `_mt_refresh_status_on` (H98's keypress repaint, incl. its background
ssh hop per prefix+f) DELETED — the daemon repaints on change. `sesh-current-status` STAYS: the
personal server's `~/.tmux.base.conf` still calls it (its panes are not daemon-managed; it still
forks per redraw — CPU only, no per-shell agent start outside termux; out of scope, noted in the
function's header). termux.sh's leak comment reworded to the past tense.

**DEPLOY (2026-09-16).** Order per machine: new binary → `supervisorctl restart sesh-daemon` →
myrig pull + render → `tmux -L sesh source-file ~/.sesh/myrig/tmux.work.conf` (an OLD daemon stamps
nothing, so re-sourcing first would blank the row). **LIVE ON 5/6 at cfc4fa1** (`vcs.modified=false`
everywhere): mymain (native; own checkout was clean), ideapad + pocket4 (native, python3 render),
macstudio + macbook (`/opt/homebrew/bin/go`, `uv run --with jinja2` render); every checkout clean
before pulling; mesh all five reachable after the restarts. Verified on each machine with a marked
pane that `@sesh-name` is set and `#{E:status-format[0]}` renders the row — mymain
`sesh: adi-requests [c194478c] · claude ⚑ FLAGGED`, pocket4 `myarch-tablet-mode … ⚑`, macstudio
`pendrive-llm`, macbook `chanu-wedding … ⚑` (ideapad had no marked pane). **THE NUMBER: mymain's
work server, 4 attached clients, 0.1 s sampling for 20 s — 0 `zsh -lc … sesh-current-status`
spawns, where the identical sample counted 12 before the deploy.** `%1620`'s row rendered within
a tick of the restart. **termux, ~1 h later:** at the first pass the phone was OFF the tailnet
(`tailscale status`: offline; ssh timed out, not refused — nothing to do with Termux), so a
once-a-minute watch waited for ping+ssh and the recipe ran at 08:44 UTC: `cd ~/mysetup/sesh && git
pull && go build -o ~/.local/bin/sesh.new ./cmd/sesh && mv -f … sesh` (plain build, CGO=1/android —
H22; built at a61063c = cfc4fa1 plus docs commits, `vcs.modified=false`), the old daemon (pid 7270,
its exe already reading `(deleted)` after the mv) killed by ITS OWN reported pid, `cd ~/mysetup/myrig
&& git pull && python3 scripts/install-home.py "$MYRIG_TARGETS"` (log to $HOME — /tmp unwritable,
H38), then a FRESH login relaunched it via the zshenv guard (that login's command line must not
contain the guard's `pgrep -f` pattern text): pid 12793, exe the new binary, the four SESH_* vars,
schema 25, cadence idle, `tmux -L sesh source-file ~/.sesh/myrig/tmux.work.conf` done, the scratch
pane's row renders EMPTY (correct: termux owns no threads — the benefit there is purely the missing
fork), `~/.ssh/agent/` holds 0 socket files (the agent fix holding), all five peers synced.

**SWEPT en route (H75 leak class, NOT mine, no suite running):** a 17-day-old conformance tmux
server `sesh-test-local-1788012287139033872` still hosting a sandbox pi (7 MB) from some earlier
killed suite run — killed by socket name — plus five dead `sesh-test-local-*` socket files. NB the
harness killed my hour-long termux-retry loop citing "low memory" while `free` showed 17 GB
available on mymain: that is the harness's own heuristic, not box pressure — keep waits short.

**SMALL TRAPS:** zsh globs an unquoted `status-format[0]` (`no matches found` — quote the option
name in `tmux set`); `display-message -p '#{E:status-format[0]}'` renders a status FORMAT for
verification (capture-pane never captures status lines); a shell redirect into a non-existent log
DIRECTORY fails BEFORE the command runs (rc=1 from the shell, `install-home` never executed —
verify the rendered artifact, not the rc alone); `grep -c '#('` on the conf counts your own
"never put `#()` back" comment.

## H108 — PHONE MEMORY (2026-09-15; record only, NO code change). **THE HEADLINE BELOW WAS WRONG — READ FOLLOW-UP 3 FIRST:** the "193 MB, ~2 %, victim not cause" reading missed ~2,000 leaked ssh-agents (~1.9 GB) that are invisible from inside Termux. Original title: the whole Termux uid is 193 MB, ~2 % of the ~10 GiB other apps hold in zRAM — so the recurring whole-Termux deaths are lmkd victims, not a sesh cost, and nothing inside the uid can self-heal them (termux sshd DOWN at the time of writing)
Cross-thread finding, relayed at Lukas's request ("read it and factor it into the termux work"). Source:
`~/dev/20260829_p58ayx__foldable-phone-research/phone-memory-findings-2026-09-15.md`, measured over ssh
on the phone that afternoon. Summing VmRSS+VmSwap over every Termux-uid process (37: the daemon, the
full cockpit, sshd sessions, shells): **112 MB resident + 81 MB swapped = 193 MB**. Largest single
process `sesh daemon run` at **15.9 MB RSS** (H102 read 45 MB two weeks earlier — the difference is what
zRAM has since swapped out, not a regression); each `sesh master window` ~3.4 MB. System-wide: 11.3 GiB
total, MemAvailable 681 MiB, SwapTotal = exactly MemTotal/2 (the zRAM fingerprint — `/proc/swaps` is
permission-denied from Termux, the ratio is the evidence) at **99 % full**: ~10.2 GiB of anonymous
memory held by OTHER apps (4.63 GiB resident + 5.58 GiB compressed), 845 MiB of page tables = a very
large cached-process population. Termux is ~2 % of that.

**WHAT IT SETTLES FOR THIS BOX.** H99 drew the boundary "NOT sesh's fault: the phone is memory-starved"
from swap at 96–98 %; this quantifies it. (1) The CPU side is done (0.7 % idle, H99/H102) and there is
no memory lever left in sesh worth pulling FOR THE PHONE: even Stage D's serve-from-rows (BACKLOG #6)
trims at most a 15.9 MB RSS on a device short by gigabytes. Phone memory is therefore NOT a trigger for
Stage D — its triggers stay the TUI poll cost and mesh size (noted in BACKLOG #6 and MESH_SCALE.md §8).
(2) The intermittent Termux deaths recorded across this work — the H99 A/B daemon death, the
post-deploy sshd+daemon death in H102, termux unreachable for H106's deploy on 09-14, and again TODAY
(`android-main` answers a tailscale ping in 70 ms, direct path, but 8022 and 7878 both REFUSE — the uid
died sometime after the other thread's ssh measurements the same afternoon) — are consistent with lmkd
killing the Termux app under pressure created elsewhere, Android reaping its tracked children with it.
TWO DEATH SIGNATURES are now on record and they discriminate: H83's phantom-cap cull took the COCKPIT
cohort and SPARED the setsid-detached daemon/sshd/crond; the H102 and today's deaths took sshd too, i.e.
the whole uid — the lmkd shape, which H84's cap bump cannot prevent and a wake lock does not touch.
(3) NOTHING INSIDE THE UID CAN SELF-HEAL A UID KILL. The relaunch paths are the login guard
(`myrig home/.myrig/zshenv/^termux^termux.sh`: starts sshd, crond and the daemon on every zsh startup)
and Termux:Boot (`home/^termux^.termux/boot/start-sshd`: wake lock, termux-api-start, sshd — it does
NOT start the daemon); after a uid kill only opening a Termux session or a reboot reaches either, since
sshd is among the dead. So the guard stays the right mechanism. A cron guard (cronie is provisioned and
crond started by that zshenv) would cover only a DAEMON-ONLY death — one is on record, the H99 A/B, when
the OLD binary sat at 15 % of a core (a plausible background-CPU phantom kill; at 0.7 % it is not) —
unverified either way, NOT built, Lukas's call. (4) Because sshd is down I could NOT read today's daemon
etime, `oom_score_adj` or `~/.myrig/logs/sesh-daemon.log`; every number above is the other thread's,
cited, not re-measured.

**CORRECTIONS THAT FLOW BACK TO THE OTHER THREAD.** (a) Its "the phantom cap's effective state is still
unconfirmed": H84 raised `max_phantom_processes` to 2^31-1 via adb `device_config` (the allowlist-blocked
`settings_enable_monitor_phantom_procs` toggle is NOT what the fix relies on), and H99 re-read that value
after a reboot on 2026-08-29 at 17h46m uptime — settled on the Android 16 build. What IS open: the file
reports the phone on **Android 17** now, and an OTA can reset device_config overrides. Behaviourally the
cap is not biting (37 processes alive today, 66 on 08-29, both above 32 with no cull), but the honest
check is `adb shell dumpsys activity settings | grep phantom` once wireless debugging is on, and H84's
three adb lines are the re-apply. (b) Its own correction is right and worth keeping here: deviceidle
whitelist and `SYSTEM_EXEMPT_FROM_POWER_RESTRICTIONS` exempt from POWER management only and do not move
`oom_score_adj` — no protection from lmkd. Termux read adj 0/50 in H83/H84 (foreground/perceptible
class), as good as a non-foreground app gets; at 99 % zRAM lmkd reaches that class. (c) What would
actually help the phone is per-app attribution (`dumpsys meminfo` by process / by OOM adjustment) —
blocked on wireless debugging being OFF (`service.adb.tls.port` empty) and Shizuku not started. That is
the other thread's track; sesh has no part in it.

### H108 follow-up — re-measured once Termux came back (2026-09-15, 12:23–12:35 UTC; still no code change)
Lukas: "sshd is now up." Read-only probe over ssh (a script piped to `zsh -s`; nothing written).
- **Recovery was exactly the documented path.** A Termux:Widget `zsh -ilc mmt-start` at 12:23:31 UTC
  ran the zshenv guard, which relaunched sshd (pid 23428), crond and the daemon (23451) within the same
  second; the widget then built the full six-machine cockpit. NOT a reboot: `/proc/<pid>/stat`
  starttime puts the daemon and sshd at 94,240 s ≈ 26.2 h after boot (boot ≈ 2026-09-14 10:13 UTC).
  NB a tmux server's comm is `tmux: server` — the space shifts every `stat` field by one, so read
  starttime from a process whose comm has no space.
- **Daemon healthy:** exe → `~/.local/bin/sesh` (not `(deleted)`), `vcs.revision=24e7e86` /
  `vcs.modified=false` (code-current with main — the two later commits are docs only), the four SESH_*
  vars, schema 25 / api 48, cadence idle, all five peers synced 13 s ago, 0 local threads. RSS 34.8 MB
  at 70 s (VmSwap 0) and **20.9 MB at 11 min** — already being swapped out; RSS on this phone measures
  pressure, not footprint. `oom_score_adj` 0 / `oom_score` 668 for the daemon, sshd, crond, the master
  server and every master window — the foreground class H83 saw; expect 50+ once Termux leaves the
  foreground.
- **Android 17 confirmed:** `google/tokay/tokay:17/CP2A.260805.005`, sdk 37, security patch 2026-08-05.
  `service.adb.tls.port` read empty — which on Android 17 means UNREADABLE from the Termux uid, not
  "off" (corrected in follow-up 2), so the device_config value was still out of reach here.
  **Behaviourally the phantom cap is NOT biting on Android 17:** 33 processes under the uid
  at +1 min, **36 at +11 min with the master server, daemon and sshd all still alive and 6 master windows
  up** — H84's method (pre-fix the same cockpit died by ~7 min; the trim is lazy and arms on crossing
  32). One ssh probe per sample, so minimal warming (H84's caveat). Strong evidence, not proof; the
  `dumpsys activity settings | grep phantom` line remains the check once adb is back.
- **The pressure had been relieved WITHOUT a reboot** — MemAvailable 5.2 GiB (681 MiB this morning),
  swap 48 % used (99 %), PageTables 210 MiB (845), AnonPages 1.78 GiB (4.63) — a large kill sweep
  (lmkd's, or apps swiped away) freed ~4.5 GiB and Termux went with it. **And it refills fast: between
  the +1 and +11 min samples SwapFree fell 3.05 GB → 1.45 GB and MemAvailable 5.2 → 4.16 GiB** — ~2.4
  MB/s into zRAM, so this morning's 99 % state is a steady state the phone returns to within roughly an
  hour of a purge. That refill rate is the number the foldable thread's per-app attribution should
  explain.
- **Death history is not recoverable:** the guard launches with `>`, so `~/.myrig/logs/sesh-daemon.log`
  is truncated per relaunch (590 bytes now: the expected no-API leaf warning). If death frequency ever
  matters, the myrig one-liner is `>>` plus a dated launch line — not done, Lukas's call.
- `sqlite3` is not installed on termux; store counts there go through `sesh` itself.

### H108 follow-up 2 — the other thread got adb and ran `dumpsys meminfo` (2026-09-15 ~16:15 UTC; relayed by Lukas; no code change)
Same findings file, new "Update" section. What it settles and corrects here:
- **PHANTOM KILLER: CLOSED.** `settings get global settings_enable_monitor_phantom_procs` → **`false`**,
  and Settings.Global overrides the `true` sysprop. That `false` is H84's own belt-and-braces
  `settings put`, so at least that H84 write SURVIVED the Android 17 OTA (the `max_phantom_processes`
  device_config value was not re-read; with the monitor off it is moot, and the +11 min survival above
  agrees). H83's "NOT DETERMINED" and my "open after the OTA" are both closed.
- **Uptime 108,078 s (30.0 h)** at their read ≈ 16:15 UTC — cross-checks the starttime arithmetic
  above (26.2 h at 12:23 UTC ⇒ boot ≈ 2026-09-14 10:13 UTC) exactly. Swap had refilled to 99 % within
  30 h of boot, matching the ~2.4 MB/s refill measured above.
- **No single hog:** 93 CACHED app processes hold 4.8 GB (YouTube 687 MB, Maps 547, Gmail 200, …),
  which Android counts as "free" because lmkd reclaims from that pool; the non-reclaimable
  Visible/Perceptible tiers hold ~2 GB (GMS 353, launcher 266, Gboard 218, …). Obsidian had already
  been evicted. Termux by PSS: `com.termux` 104 MB (in the FOREGROUND bucket — Lukas had it open), the
  sesh daemon **27 MB**, each master window ~4 MB, Termux:API 12 MB.
- **zRAM correction:** 1.34 GB physical holds 5.50 GB swapped = **4.1× compression**, not the 2.3× the
  morning section estimated; the rest of the gap is GPU private memory (543 MB) and "Lost RAM"
  (907 MB). No number in H108 above depended on the 2.3×.
- **`service.adb.tls.port` is NOT READABLE from the Termux uid on Android 17** (empty ≠ off), and
  wireless debugging only runs on Wi-Fi (the phone had been on mobile data). The other thread found
  the port by scanning localhost from inside Termux.
- **WHY sshd, crond and the daemon die WITH the app, now that the monitor is confirmed off:** with
  phantom tracking disabled, AMS does not even know the children exist, so the uid-wide death is not
  a phantom kill. The mechanism that fits all three observations (H83's cull SPARED the setsid-detached
  trio while the app lived; H102's and today's deaths took them while the app died; the monitor is
  off) is ordinary app-death cleanup: when lmkd kills `com.termux`'s main process, AMS kills the app's
  PROCESS GROUP (its cgroup), and `setsid` does not leave the cgroup. Not verified on-device (no
  logcat), stated as the fit. Consequence: the sesh leaf lives exactly as long as the Termux app
  process does, and the app's oom bucket — foreground while open, perceptible via the wake-lock
  foreground service when backgrounded — is the only knob. It is a phone knob, not a sesh one, and
  under this morning's pressure lmkd reaches the perceptible tier anyway.

### H108 follow-up 3 — CORRECTION: Termux WAS a cause — ~2,000 leaked ssh-agents (~1.9 GB at oom_score_adj 0), invisible from inside Termux; root cause = the work server's status-line login shell meeting termux.sh's per-shell agent start (2026-09-15 evening; relayed by Lukas; NO code change — termux.sh deliberately NOT edited, Lukas is deciding the permanent fix)
The foldable thread's "Update 2"/"Update 3" in the same file. Everything in H108 that rested on
Termux-side process counts was blind to this, and so were H83/H84/H99's counts. Re-verified here where
I could without touching the phone's state; the rest is cited.
- **What was found (adb `dumpsys meminfo`, Native bucket grouped by name):** 1,591 → 1,973
  `ssh-agent` processes over a few minutes (+52/min), all uid 10440 (Termux), PPid 1,
  `oom_score_adj 0`, cpuset `/foreground`. Killing 2,097 at once freed **MemAvailable +1,875 MB,
  SwapFree +2,393 MB, PageTables −382 MB** — ~0.9 MB of real RAM per agent, much of it kernel. At
  adj 0 and unmanaged by AMS they were among the LAST things lmkd would touch: it evicted every
  cached app, then services, then visible apps — Obsidian included — before them.
- **Why every Termux-side measurement missed them:** OpenSSH marks `ssh-agent` non-dumpable to
  protect keys, and Android's `/proc` (hidepid) then hides a non-dumpable process from other
  processes EVEN OF THE SAME UID: `pgrep -x ssh-agent` → 0, `/proc/<pid>` "No such file", adb shell
  gets EPERM on their environ. So the 193 MB, the "37 processes", the +11 min "36 processes", and
  plausibly H83's 12/39/45 and H84's censuses all excluded them. Digested as a trap below.
- **Root cause chain — the other thread verified it, and I re-read both myrig files: it is exactly
  as stated.** (1) `home/.myrig/zshenv/^termux^termux.sh:79-82` runs `eval "$(ssh-agent -s)"` in
  EVERY zsh lacking `SSH_AUTH_SOCK`, non-interactive included, and the agent outlives the shell.
  (2) The same file launches the sesh daemon at lines 70-77, BEFORE that block, so the daemon has no
  `SSH_AUTH_SOCK`; the daemon is the sole work-server creator (H85, by design), so the work tmux
  server's GLOBAL env has none either (`show-environment -g SSH_AUTH_SOCK` → unknown variable), and
  tmux runs `#()` status jobs with the global env. (3) myrig's `tmux.work.conf:29` status row is
  `#(zsh -lc 'sesh-current-status #{pane_id} #{socket_path}')` — a login zsh that sources termux.sh,
  sees no agent, starts one, exits. (4) Every non-interactive `ssh-target termux '…'` leaks one too:
  my SIX ssh probes today each did (`~/.ssh/agent/` socket files 246 → 248 across the last two,
  exactly as predicted). History per the other thread: the status job since 146bb6e (2026-06-11), the
  daemon-in-zshenv since a94af90 (same day), the agent block older — the leak has run whenever the
  cockpit was attached, since June. That spans H83/H84/H99: **part of the "swap pinned at 96–100 %"
  those entries blamed on other apps was this.** Causation proven by the other thread's stopgap: a
  fixed agent set as the work server's global `SSH_AUTH_SOCK` stopped the growth (socket count +2 in
  60 s, both from its own measuring ssh calls).
- **THE CADENCE IS REAL, FLEET-WIDE, AND CORRECTS H98 follow-up**, which recorded the status row as
  "re-run only on the 15 s status-interval beat". Measured today, 0.1 s sampling of `zsh -lc …
  sesh-current-status` children, `status-interval` at its default 15 on both servers: **phone (tmux
  3.7c, 1 client): 22 distinct spawns in 20 s; mymain (tmux 3.6b, 4 clients): 12 in 20 s** (a 0.5 s
  sampler on mymain saw only 2 — the shells live ~200 ms there; sample fast). About one login zsh per
  second per attached client, each sourcing all of myrig's shell.sh (~0.9 s wall on the phone — H99's
  1.8 % CPU figure assumed a 15 s cadence and is an underestimate). WHY tmux re-runs a `#()` far below
  `status-interval` I did not establish; it is measured, not explained.
- **Fleet check for the same class (read-only):** myrig starts an agent in exactly two places —
  termux.sh:81 (this leak) and `scripts/post/all.sh:20` at install time, guarded by `ssh-add -l`
  rc 2 (no reachable agent) but never stopped afterwards. Live counts: mymain 1 (`ssh-agent -D`, the
  40-day systemd one), macstudio 1 (launchd, 23 d), macbook 1 (launchd), pocket4 ≤ 1, **ideapad ~9,
  oldest 16 d, all `ssh-agent -s`** — the install-time one accumulating about once per unattended
  reinstall (its CI runner reinstalls), ~1 MB each: a myrig footnote, not a problem. The per-shell
  class is termux-only.
- **Phone state at my last probe (~18:40 UTC):** stopgap live (`SSH_AUTH_SOCK=~/.ssh/agent-fixed.sock`
  in the work server's global env; daemon pid 12114, restarted again ~76 min earlier by the other
  thread's `am kill-all`, stopgap re-applied after it); ~240 pre-stopgap agents still alive (listing
  them needs adb, killing them needs the Termux uid); `ssh-target termux` still leaks one per call.
  **NOT done here, deliberately: no edit to termux.sh, no agents killed, stopgap untouched** — Lukas is
  choosing between the file's proposed fix (one agent per phone at a fixed socket, fork-free liveness
  check, placed BEFORE the daemon launch) and dropping the agent on the phone entirely (the key has no
  passphrase; costs `ForwardAgent`).
- **What in H108 survives, what falls.** Survives: the daemon itself is small (27 MB PSS / 15.9 MB
  RSS), the H99/H102 CPU results, "nothing inside the uid can self-heal a uid kill", the
  phantom-killer closure, the boot-time arithmetic, and BACKLOG #6's memory-is-not-a-trigger (the
  leak is not the view's RAM). Falls: "193 MB / ~2 % / victim, not cause" — Termux held ~1.9 GB,
  self-inflicted, and the uid-wide deaths were partly its own pressure. Title amended above;
  MESH_SCALE.md §8 rewritten; H99's "NOT sesh's fault" sentence carries a pointer.
- **The sesh-side item this leaves:** the leak's fix is myrig's, but a login shell forked per status
  redraw is a mechanism cost sesh can remove for good — a daemon-maintained pane option
  (`#{@sesh_status}`, zero forks) or at least `#(sesh tmux status …)` (one exec, no shell). Designed
  as BACKLOG #7, not built. Nothing else to do here until Lukas decides the termux.sh side.

## H107 — the uuid popup's COPY (`y`, then `c`) worked on macOS only: termux is GOOS=android, wl-copy's forked child held the exec PIPE (TUI freeze), popups have no display env (2026-09-14, sesh 060ee4c; NO schema/API change; BINARY-ONLY, DEPLOYED ALL SIX)
Ticket b7da691e. (Lukas corrected my first read: the key is `y` for the popup, `c` inside it copies.)
THREE INDEPENDENT DEFECTS, each reproduced on the real box before touching code:
1. **termux: GOOS=android, not linux** (plain `go build`, H22). `clipboardCmd` switched on
   "darwin"/"linux" only, so termux hit `default` → "clipboard not supported on android", and the
   `termux-clipboard-set` candidate filed under linux was unreachable. **Any `runtime.GOOS == "linux"`
   gate silently excludes termux** — grep for it when a feature "works on Linux but not the phone".
2. **pocket4 sidebar (Wayland env present): the TUI FROZE.** Measured with a probe of the exact exec
   shape: rc=124 at a 10s bound. `wl-copy` forks a child that keeps serving the selection and
   inherits stdio; `CombinedOutput` reads through a pipe, and **Go's Wait blocks until every writer
   of that pipe closes** — i.e. until something else takes the clipboard. It ran inline in Update.
   This is H43's xclip-over-ssh trap, in Go. Fix: hand the tool NO pipe — stdin an `*os.File` pipe
   written+closed before start, stdout nil (/dev/null), stderr a temp FILE read afterwards. Plus a
   10s CommandContext timeout (termux-clipboard-set without the Termux:API app blocks forever) and
   the copy now runs as a tea.Cmd (termux-clipboard-set takes ~0.8s).
3. **pocket4 work-server popup: no WAYLAND_DISPLAY/DISPLAY** (the boot-started tmux server's global
   env is empty; the SIDEBAR on the master server does have it) → wl-copy "Failed to connect to a
   Wayland server". Fix: when the process names no display, run the tool with the session env from
   the systemd user manager — the SAME canonical source the daemon's spawnEnv used (H74). Lifted into
   `internal/sessionenv` (Graphical() now returns an error instead of logging; spawnEnv logs it) so
   the TUI and daemon share one implementation. spawnEnv behaviour unchanged.
Side bug fixed: the copy error went to `lastErr`, which renders "(daemon unreachable: …)" and is
cleared by the next fetch — now `actionErr`.
TESTS: `internal/tui/clipboard_test.go` — per-GOOS tool truth table, clipboardEnv, a REAL exec of a
stub that leaves `sleep 30 &` holding its stdio (must return <5s AND deliver the text), stderr
surfaced, timeout, model wiring. The `uuid-popup-copy` claim stubbed only `wl-copy` — which is WHY
it was red on macbook (H80/H88/H91/H97 recorded it as an environment red; it was a test defect) —
now installs the stub under all three tool names, and forks a lingering child. ANTI-GAMING: stderr
→ `&strings.Builder{}` (a pipe) turns the unit AND the claim red with the freeze message; reversed
md5-identical. GREEN: vet; every non-conformance package plain and -race; FULL TUI claims (237s);
thread.new.headed/pi/local (spawnEnv through the new package). Full matrix NOT run.
LIVE-PROVEN on pocket4 (isolated tmux, real `sesh tui` vs the live daemon, keys `y` `c` only):
sidebar-like env AND a display-stripped env both put the popup's uuid into the real Wayland clipboard
(`wl-paste` read-back; the second run started from a reset clipboard). pocket4's clipboard text was
saved and restored (its rich HTML flavour was not). termux: the TUI reported "UUID copied" (tool exit
0), but **read-back is NOT observed** — `termux-clipboard-get` over ssh returns empty because Android
blocks clipboard reads from a background app, adb has no `cmd clipboard`, and the phone was awake in
Obsidian so I did not steal focus. **Lukas then CONFIRMED it works on both termux and pocket4 in real use
(2026-09-14).** That smoke OVERWROTE the phone's clipboard (unreadable, so unsaveable).
**MY MISTAKE, recorded so it isn't repeated:** after an anti-gaming run I ran `pkill -f "^sleep 30$"`
— the H101 trap. The test's cleanup had already killed its own child, so anything it matched belonged
to someone else's polling loop. Kill test children by recorded pid only (the tests now do).
NOT CHANGED, noted: the copy targets the clipboard of the machine the TUI RUNS ON, so a popup in a
cockpit window for a remote machine copies to that machine (SKILL now says so). OSC 52 through the
tmux chain would reach the viewer's terminal instead — a design change, not built. mymain's X
display is not inferred (H43's single-socket heuristic lives in myrig, not here).
DEPLOY: binary-only, no restart, all six at 060ee4c `vcs.modified=false` — which also delivered H106's
pending binary to macbook and termux. A running sidebar keeps its binary until `prefix+r` (H70).

## H106 — SIDEBAR ARROW JANK on pocket4: H98's cockpit tracker applied resolves that RACED the sidebar's own follow navs; fix = own navs are observations + epoch-discard + no resolve mid-follow (2026-09-14, sesh f89b563; NO schema/API/daemon change; BINARY-ONLY, deployed 4/6 — macbook + termux unreachable, pending)
Lukas: "when I use the keyboard up and down keys on my sidebar and cockpit ... it seems to sort of
jank a bit and pre-select or revert to selecting some of those sessions that I was previously on.
It seems to jump back and forth. This is happening on my pocket4."

**H98's "the sidebar's own follows are self-cancelling" WAS WRONG, and the reason is the whole
bug.** It holds only while the cursor is still ON the thread being navved to. Arrowing outruns the
navs: ↓ fires a follow to b; a second ↓ to c is SWALLOWED (followInFlight — H57's coalesce); b's
nav lands and rings the nav bell; the bell's resolve reports b; b ≠ the stale baseline a (follows
never updated lastMasterThread) ⇒ cursor yanked back to b, then forward to c when c's resolve
lands. Same shape from the 3s backstop, and from ANY resolve issued before a follow and answered
after it. H98 itself wrote "removes any need for an epoch/sequence guard against a stale in-flight
resolve" — the guard WAS needed.
WHY pocket4: its cockpit sits on the **mymain** window (5 windows, active=mymain), so most rows are
REMOTE: a follow is a `sesh tmux nav` SUBPROCESS (rings the bell) and a resolve is a mesh round
trip. MEASURED on pocket4: `master-current` local 7ms, `--machine mymain` ~190–205ms — a race window
an ordinary key-repeat lands in every time. The local fast path (client.TmuxNav) rings no bell, so
it only races the backstop, which is why a local-window cockpit barely shows it.

FIX (internal/tui/mastertrack.go + model.go): `recordOwnNav(id)` on a successful followDoneMsg AND a
sidebar navDoneMsg sets lastMasterThread (the sidebar KNOWS what it just put on the cockpit) and
bumps `masterTrackEpoch`; masterCursorMsg carries the epoch it was issued in; applyMasterCursor
discards while followInFlight or on an older epoch. The track tick spends NO resolve while a follow
is in flight and leaves the bell UNCONSUMED, so a genuine cockpit-side move that rang meanwhile is
still resolved on the first tick after. The change-not-disagreement rule is untouched.
**Each of the three pieces is load-bearing on its own, proven by neutering each alone:** recording
closes the coalesced shape; the epoch closes "issued before a follow, answered after" (a case the
recording itself CREATES — once follows update the baseline, a stale pre-follow reply differs from
it); the in-flight guard closes "a resolve lands while the second follow is still running". My first
neuter run showed the in-flight guard was unpinned by any test — added the mid-follow case before
trusting it. Check coverage per guard, not per fix.

TESTS. Units: DoesNotRevertAFollowInProgress, DiscardsAResolveOvertakenByAFollow (+ a fresh resolve
of an external move still tracks), OwnNavIsAnObservation (follow + Enter; headless-row rule still
holds), TickWaitsOutAFollow. New claim `sidebar-arrow-no-revert` (registered AND declared — H25):
real daemon, three real pi threads, a real nested tmux client in a real master-client marker, the
follows' real TmuxNav switches, real MarkerClientCurrent resolves; only the MESSAGE ORDER is chosen
(which is exactly what bubbletea's concurrent cmds produce). ANTI-GAMING (reverse-edited,
md5-verified restore — H44): pre-fix logic restored ⇒ the claim fails verbatim "cursor = arrow-b —
the tracker dragged the selection back". NB the claim's `resolveNow` compares a msg against
`tui.MasterTrackTick()` to skip the rescheduled tick (the msg type is unexported; interface equality
on an empty struct works).
GREEN: `go vet ./...`; internal/tui plain + -race; cmd/sesh; the FULL TUI claims suite (213s);
sidebar-tracks-cockpit, sidebar-nav-stays, the new claim. The full matrix was NOT run.
NOT LIVE-OBSERVED in Lukas's running sidebar (it was not relaunched with SESH_TUI_LOG — H70/H71
say reach for that first; I didn't, because it restarts his live sidebar). The evidence is the
exact phenomenology + the measured race window + the real-component claim reproducing it. **If he
still sees jank after `prefix+r`, the next step IS the debug log** (`tmux -L sesh-master
set-environment -g SESH_TUI_LOG /tmp/sesh-sidebar.log`, prefix+r, grep `MASTER TRACK` against
`KEY`/`FOLLOW` lines).
DEPLOY: binary-only, no daemon restart. f89b563 on mymain, pocket4, ideapad, macstudio (all
`vcs.modified=false`). **macbook (ssh :22 timeout) and termux (android-main:8022 timeout) PENDING**,
harmless: `cd ~/mysetup/sesh && git pull && go build -o ~/.local/bin/sesh.new ./cmd/sesh && mv -f
~/.local/bin/sesh.new ~/.local/bin/sesh` (macbook: /opt/homebrew/bin/go; termux: plain go build,
H22). **A running sidebar keeps its binary until `prefix+r` (H70)** — pocket4's was pid 28776 from
13:33 today.

## H105 — "setup-the-DOC holds archived threads" — IT NEVER DID: 1,249 archived threads were held purely by INHERITANCE; fix = archived detaches from the max, like a release (2026-09-02, sesh 30e5add; NO schema/API/CLI change; DAEMON rebuild + RESTART)
Lukas: "I want it to only hold or unhold non-archived threads. Currently I think it also holds
archive threads, which is a bit confusing because they are already outside of the active view.
If I happen to unarchive such a thread, it won't appear in my active view."

**THE PREMISE WAS WRONG AND THE INTENT WAS RIGHT — measure before implementing.** Doing what
was literally asked would have been a NO-OP: `_mt_doc_plan` builds from `sesh thread grid
--json --all-machines` with **no `--archived`**, so archived threads are not in the plan at
all (verified: 61 plan rows, ZERO archived). The decisive query took one line:
`archived_held_with_OWN_hold: 0, archived_held_INHERITED_only: 1249`. Not one archived thread
carried a hold of its own; every one was inheriting from a live ancestor a parking round had
parked. So the fix belongs in the DERIVATION, not the DOC command.

**WHERE THE MASS WAS, since the numbers looked implausible to him** ("when I go around on my
active view, I don't see that many threads") — and his own guess was right: 2,116 records, only
**66 non-archived**; held = 1,272 = 23 live + 1,249 archived; the `on hold` view is
(non-archived AND on hold) = **23 rows**, which is all he ever sees. **1,224 of the 1,249 hang
under ONE held root** (`ituc-run-supervisor`: 278 children + 971 grandchildren, all archived).
One held supervisor was silently parking its entire archived worker fleet.

**THE RULE.** A hold parks ACTIVE work temporarily and expires on its own; archiving is the
permanent kind and already hides the thread from every view. So `effectiveHolds` now treats
Archived exactly like a release: the thread contributes only its OWN deadline and the walk from
a descendant STOPS there. Its own hold still applies and still reaches its descendants —
explicit is explicit; what stops at an archived node is only what flows from ABOVE it.
Un-archiving returns it to inheriting, so this is about what is parked WHILE archived, not a
permanent exemption. `holdDominator` mirrors it exactly — otherwise a `--clear` refusal would
name an ancestor that is no longer the reason, which is the plausible-but-wrong class.

**BLAST RADIUS MEASURED BEFORE WRITING THE CODE:** zero non-archived threads change (no live
thread is held only via a chain passing through an archived node); 1,249 archived ones stop
reading as held. That number is also why H104's new ⧖ sigil would otherwise have drawn on 1,249
archived rows in the `all` view.

TESTS: the archived truth table with a NON-ARCHIVED BASELINE asserted first (without it every
"is free" check passes vacuously) — does not inherit, does not transmit, a live sibling still
inherits, its own hold still applies AND transmits, un-archiving restores inheritance; plus
holdDominator's matching cases. `thread.hold/local` extended against a real daemon: a
three-generation tree parked by its root, archive the middle → it AND its child detach while the
root stays parked, un-archive → inheritance returns. ANTI-GAMING (reverse-edited, md5-verified
restore — H44): letting archived inherit again reddens the unit ("must not inherit … got 5000")
AND the cell ("archived=true grandchild=false").
GREEN: `go vet ./...`; every non-conformance package plain AND `-race`; thread.hold ×2,
mesh.snapshot, daemon.mesh-read; claims view-hold, hold-sigil, view-active-archived-live,
action-archive. The full matrix was NOT run.

DEPLOY: daemon-side derivation (maintainer + grid), so **rebuild AND supervised restart** — no
schema/API/CLI change, so a mixed fleet is safe (each owner derives its own threads).

## H104 — HOLD SIGIL: ⧗ own / ⧖ inherited, in the gutter cell that was already dead space (2026-09-02, sesh 0f3bc89; NO schema/API/CLI change; BINARY-ONLY, no daemon restart)
Lukas: "It would be good to assemble a sigil uh for threads that are on hold. Could you
workshop that?" Conferred with rendered gutter previews (the H49/H97 method — they render in
HIS font, which is the only test that matters); he picked all three recommendations.

**THE PAIR IS THE DESIGN, not decoration.** A single "held" marker would be nearly useless:
`active` HIDES on-hold threads, so the sigil is only ever seen in `all`, `on hold`, and custom
views — and in `on hold` EVERY row is held, so a plain marker sits on all of them saying
nothing. ⧗ own / ⧖ inherited is actionable instead: effective hold is max(own, ancestors'),
so an own hold is CLEARED while an inherited one cannot be — it needs `--release`. That is
precisely the H103 bug's shape, and nothing on screen said which row was which.

**IT COSTS NO WIDTH, and that is why it went in the MARK cell.** `pinMark` drew ↕ for the one
row being moved in move mode and a blank on every other row, always — the leftmost gutter cell
was dead space in steady state. The alternative (an 11th cell) would tax every row in every
view AND the 38-col sidebar's name column for a marker the default view never shows. Move mode
still wins the cell while active; `pinMark` → `markGlyph` so the name says what it carries.

**MEASURE THE GLYPH BEFORE FALLING FOR IT** (H97's rule, re-applied): every candidate went
through go-runewidth first. `⌛` is TWO cells (out). `‖`/`∥` — the most legible "paused" — are
EastAsianWidth AMBIGUOUS, so a terminal rendering ambiguous as double would misalign the
gutter; that is the same class as the existing ●▶·↓≡, so not a regression, but ⧖⧗☾⏸ are
unconditionally 1 cell. `⏸` reports 1 cell but lives in the emoji block, where terminals often
apply emoji presentation — flagged in the preview so he could judge it by eye. The hourglass
also has the only natural filled/hollow TWIN, which is what made the pair possible at all.

**THE FAMILY GUARD GAINED A THIRD PAIR.** New "hourglass" family (⧖⧗⌛⏳) with
`hold/inherited|hourglass` exempted by name — a pair counts as ONE occupant, so a THIRD glyph
reaching into that shape later still trips. **The guard caught my own typo immediately**: I
wrote the family string with ⧗ twice, and it reported "2 glyphs from the hourglass family
([hold/own ⧗ hold/own ⧗])" — a duplicate rune reads as a collision with itself.

TESTS. Units: the truth table (own / inherited / an own hold LATER than the ancestor's still
reads own / released → blank / lapsed → blank), markGlyph's precedence, and that the sigil
reaches the rendered row in BOTH branches — the selected (reverse-video) branch builds its
gutter separately and is where a new cell is easiest to drop on the floor. New claim
`hold-sigil` (registered AND declared — the H25 gotcha) drives a REAL daemon with a real
parent/child: baseline no sigil, hold the parent → ⧗ parent / ⧖ child, the child's own
dominating hold flips it to ⧗, `--release` clears the cell while the parent stays parked. It
matches a glyph to a SPECIFIC row by gutter position (rune index 2) — a bare `Contains` would
pass on any row carrying it. **TRAP: the child is NESTED, and a collapsed parent renders
`▸ parent` with the child not on screen at all**, so the first run failed with nothing to
assert; `WithExpand(true)` is the honest fix — making them siblings would have deleted the
inheritance the claim exists to prove. ANTI-GAMING (reverse-edited, md5-verified restore, never
git-checkout — H44): collapsing the pair to one marker reddens the claim AND the unit; dropping
the sigil from the selected branch reddens the render test.
GREEN: `go vet ./...`; every non-conformance package plain AND `-race`; the FULL TUI claims
suite serially — **72 pass, 0 fail**. The full matrix was NOT run.
LIVE-PROVEN read-only against the real mesh (isolated tmux, never pressed Enter — that would
revive a real thread): `⧖◌·  ⊘⌁ mymain m11-identity`, a thread whose hold is entirely its
ancestors' (own=0), rendering the inherited half beside ⊘ archived and ⌁ flag-disabled without
collision — and on the SELECTED row, so the reverse-video branch is proven on real data too.
**MEASUREMENT TRAP: `awk 'substr($0,3,1)'` counts BYTES, not runes** — ⧗ is 3 bytes in UTF-8,
so my first count reported 0 sigils on a screen that plainly had them. Count runes in python.

DEPLOY: **binary-only, NO daemon restart, no schema/API/CLI/key change** — a pure TUI-client
render, so a mixed fleet is trivially safe (a machine on the old binary just draws no sigil).
**A running SIDEBAR keeps the binary it launched with (H70), so the sigil does not exist inside
Lukas's sidebar until `prefix+r`.**

## H104 — CWD-SCOPED TUI POPUPS: exact directory vs directory tree, with the launch boundary orthogonal to every view and `/` filter (2026-09-02, sesh 13ca684 + myrig dbefad8/25228da; NO schema/API/daemon change; binary-only + render/conf deploy; **DEPLOYED ALL SIX**)
Ticket fab27712. Lukas wanted two base-level commands: one grid for threads whose CWD exactly matches
the pressing pane's directory, one including descendants; both must open the NORMAL sesh TUI in a popup,
start on `all`, retain every configured view, and stay scoped while filtering/cycling views.

**MECHANISM.** New explicit `sesh tui --cwd <dir>` / `--cwd-tree <dir>` flags establish a launch-time
row-universe boundary BEFORE built-in/custom view predicates and before the interactive fuzzy filter.
`--view <name>` selects only the first view (built-ins or configured names, unknown/ambiguous is loud); it
does not replace the view set. The two scopes are mutually exclusive, and scope+`--cursor` is refused
because a pane preselect outside the boundary would be contradictory. `goto-uuid` likewise cannot escape
the launch scope and names it loudly rather than arming a delayed preselect.

Home paths are owner-portable: the invoking path normalizes to `~/…`, then compares each row's owner-
stamped `CwdRel`, so `/home/lukastk/mysetup/sesh` and `/Users/lukas/mysetup/sesh` represent the same
cross-machine scope. Paths outside home stay absolute. Descendant matching uses `filepath.Rel`, never a
string prefix (`/work/app2` is not under `/work/app`) and never symlink resolution, matching stored CWD
identity.

**MYRIG UX.** `mt-tui-cwd` and `mt-tui-cwd-tree` resolve the ORIGINATING base pane via
`SESH_MT_PANE` (THE WHICH-CLIENT LAW), open a 95%×90% `display-popup`, and invoke the fast `zsh -fc`
TUI path at `--view all`. Both are in base `prefix+m`. That menu is already a popup, so its mt menu
functions export `SESH_MT_POPUP=1`; these commands then reuse the existing popup instead of trying to
nest one. Direct shell calls create their own popup. Lukas accepted the suggested free bindings and
asked for cards: base `prefix+g` opens exact scope, base `prefix+G` opens the directory tree.

**TESTS.** Units cover exact/tree, owner-relative Linux↔macOS matching, absolute paths, prefix boundaries,
view orthogonality, initial built-in/custom views, CLI normalization/conflicts, and goto refusal. New real-
daemon TUI claim `cwd-launch-scope` creates exact/child/prefix-sibling rows, archives the exact row to
prove initial `all`, switches into a configured view without escaping, and drives `/` within the tree.
ANTI-GAMING: disabling the scope application turns the claim red verbatim with all three rows where only
the exact row was wanted; the edit was reversed and `model.go` restored byte-identically (MD5
`b8abf958cc181f0ea7c9ab300bfcdc7e`). Green: every non-conformance package plain + race, `go vet ./...`,
help meta-tests, and the FULL TUI claims suite serially in 190.061s. The FULL 255-cell matrix was NOT run.
An isolated real CLI/TUI smoke (own HOME/SESH_HOME/daemon/tmux sockets; daemon killed by explicit pid)
captured exact=`scope-exact` only and tree=`scope-exact`+`scope-child`, both `[all]`, while excluding the
prefix sibling. A rendered-zsh harness proved popup reuse, direct 95%×90% popup argv, path-as-separate-argv,
and `SESH_MT_POPUP` propagation.

**DEPLOY + CARDS.** sesh binary-only (fresh TUI clients read the new flags; daemon unchanged), myrig
render for `shell.sh`, and a live `source-file` for the new work-server bindings. **ALL SIX** installed
at binary revision 13ca684 (`vcs.modified=false`) + myrig 25228da; every fresh login resolves both
functions and every running work server reports both g/G bindings. Macbook woke during the deploy and
received its previously pending binary/render/bindings before sleeping again; Termux's first GitHub pull
failed `Network is unreachable`, then a later pull succeeded normally. Two cards were added to
`mysrs/misc/mycockpit.md` and synced: misc programme created=2, errors=0; ids `mysrs-37098d87` and
`mysrs-698cf186`. No daemon was restarted anywhere.

## H103 — YOU COULD NOT UN-HOLD A CHILD WHILE ITS PARENT WAS PARKED: the max() rule had no "not held" state; fix = a dated RELEASE (third state, detaches the subtree, auto-expires) + the un-hold verbs stop reporting success while the thread stays held (2026-08-30, sesh 424cc82 + myrig 1a8e94d; api 47→48, store migration 24→25; DAEMON rebuild + RESTART; **DEPLOYED ALL SIX** with pre/post DB backups)
Lukas: "It seems like you currently can't unhold a child thread if its parent is on hold. That is
an issue. How can we solve this?"

**THE MECHANISM, and it was working as specified — the spec was incomplete.** Since H26 a thread's
effective hold is `max(own, every same-machine ancestor's own)`, and `own` has only TWO states: an
instant, or 0. So there is no way to express "this one is not held": clearing a child's own hold
leaves the max unchanged. MEASURED in an isolated sandbox before touching anything:

    hold --clear on an inherited-held child  → prints "hold cleared", EXIT 0 → still on_hold
    TUI `h` on that child                    → own==0, so the toggle took the SET branch
                                               → it PARKED it until tomorrow
    then release the parent                  → the child stays parked, on its own new hold

So the un-hold key did the opposite of its name, and — the compounding part — left behind an own
hold that OUTLIVED the parent's, converting a transient inherited hold into a durable own one. The
CLI half is worse than useless: it is a silent wrong success, the class this project exists to
prevent.

**IT WAS LIVE, NOT THEORETICAL.** On the real mesh at diagnosis: **45 threads held, 29 of them
(64%) held ONLY by inheritance** — 15 under `trellis`, 10 under `tbi-agent-investigation`, 4 under
`dagster-netrun`. One is exactly the reported shape: `315f89fc scuttlebug-dagster-build-2` is
DIRECTLY in the DOC and today's plan says `keep`, but `dagster-netrun` parks it, so
`mmt-unhold-DOC` could not free it.
**MEASUREMENT TRAP that produced a false all-clear first: `sesh thread grid --json` emits rows at
the TOP LEVEL, while `thread list`/mesh wrap them in `.thread`.** My first sweep used `.thread.on_hold`
against grid output, got null for every row, and reported "0 held threads mesh-wide". Check the shape
before believing a zero.

**THE DESIGN (conferred; Lukas asked for the recommendation and chose the subtree rule).** A thread
is now in exactly ONE of three states — held until T, RELEASED until T, or neither (inherit) —
stored as two mutually exclusive columns written by ONE statement, so no caller can produce a
held-and-released record. Rules:
- A live release makes the thread take NO ancestor into its max, and the inheritance walk from any
  DESCENDANT stops at it, so **releasing a thread frees its own subtree with it** (Lukas's choice:
  freeing a supervisor while its workers stay parked is never what you meant).
- A released ancestor's OWN hold still parks its subtree. A release cuts only what flows from ABOVE.
- **A release is DATED, like a hold.** This is the load-bearing choice over the obvious alternative
  (a sticky "ignore my ancestors" bit): every other piece of hold expires on the day boundary and
  the slate is rebuilt daily, so a permanent exemption would be the one piece of state nobody ever
  remembers — silently absent from every future parking round. A release lapses and the thread is
  parked again like everything else.
- Rejected for the record: FANNING OUT holds onto descendants (loses "release the parent frees the
  subtree", and a thread created under a parked parent silently escapes) and loud-refusal-only (does
  not do what was asked).

**THE SECOND HALF OF THE FIX IS THE LOUDNESS, and it is independent of the feature.** `--clear` and
`--release` now check the outcome BEFORE printing anything reassuring, and FAIL (exit 1) naming the
ancestor and the remedy when the thread is still held. Only the OWNER can know this — inheritance is
resolved over that machine's whole record set — so the answer rides back with the write:
`HoldThreadResponse` is a superset of `ThreadResponse` (same `schema`/`thread` tags, so a pre-48
client decoding the old type is unaffected) carrying the post-write effective deadline plus
`held_by_id`/`held_by_name` from the new `holdDominator`. The TUI toggle now keys on the
owner-derived `OnHold` flag rather than the own deadline, and picks clear-vs-release from where the
hold comes from.

**`nextHoldFlip` REPLACES `nextHoldDeadline`, and forgetting this would have been a silent stale
view:** the maintainer schedules a full sweep at the earliest instant OnHold flips with no record
write. That used to be hold expiry only. A RELEASE expiry flips it the other way (an ancestor's hold
snaps back on) with no write either, so a missed schedule would leave a released thread reading
un-held indefinitely, with nothing to correct it. `effectiveHolds` therefore takes `now` and is no
longer a pure function of the record set.

**MYRIG — the DOC commands are where this actually bit.** `mmt-setup-the-DOC` now issues
`hold --release --until tomorrow` for EVERY kept thread (was `--clear`, and only for threads already
held). Widening the set is itself a bug fix: `held_now` comes from the plan SNAPSHOT taken before
anything runs, so a kept thread whose ancestor was ABOUT to be held read held_now=0, nothing released
it, and the ancestor's hold parked it anyway — the live `315f89fc` case. Releasing unconditionally
also makes the two batches ORDER-INDEPENDENT (both are absolute-instant statements), which matters
because `_mt_apply_holds` fires 12 at a time. Checked rather than assumed: keep propagates DOWN the
tree, so a held thread never has a kept ancestor and releasing every kept thread cannot strand a hold.
`mmt-unhold-DOC` likewise releases; `mmt-unhold-all-threads` now also selects threads carrying only a
release marker, so it is a genuine reset. And `_mt_apply_holds` now CAPTURES stderr
(`2>&1 >/dev/null`) and prints the reason with the failure — it discarded it before, which would have
turned the new precise refusal into a bare "failed" (the H95 lesson, one layer down).

TESTS. Units: the release truth table (frees the subtree, an unrelated sibling stays held, a LAPSED
release is inert, a released thread's own hold still applies), `nextHoldFlip` including the release
direction, `holdDominator`, store exclusivity both ways, and the TUI toggle driven through a REAL
fake `sesh` on disk that logs argv — the observable is which command is issued, not an internal flag.
`thread.hold` local+remote EXTENDED against a real daemon over a real ssh hop: a three-generation
tree parked by the root alone (BASELINE asserted first, or every "is free" check passes vacuously),
`--clear` refused loudly naming the ancestor, `--release` freeing the thread AND its grandchild while
the root stays parked, a hold replacing the release, and a 2-second release LAPSING with no further
write (the nextHoldFlip proof). ANTI-GAMING (reverse-edited, never git-checkout — H44; `-count=1` —
H75): the old own-hold toggle rule turns the unit RED reproducing the report verbatim ("the un-hold
key issued a HOLD"); neutering the release branch in `effectiveHolds` turns the derivation units RED;
neutering the CLI outcome check turns the CELL red at "must fail loudly, not report success". All
three reversed and re-verified byte-identical by md5, then re-run green.
GREEN: `go vet ./...`; every non-conformance package plain AND `-race`; cells thread.hold ×2,
mesh.snapshot, mesh.snapshot.http, daemon.mesh-read, route.parity; the FULL TUI claims suite serially
— **70 pass, 0 fail**. The full matrix was NOT run — do not read this as all-green.
LIVE-SMOKED end to end in an isolated sandbox (own SESH_HOME/short sockets, inherited SESH_* stripped,
daemon killed by EXPLICIT pid afterwards, live daemon verified untouched): the whole reported scenario,
plus the API refusing a held-AND-released request. The myrig side was driven with a stubbed plan and a
stubbed `sesh`: setup-the-DOC issues 2 holds + 3 releases covering every kept thread (the old code
would have released 1), unhold-DOC releases the in-DOC held thread, and a failing op now names the
machine AND the reason. SWEPT (H75 leak class, not mine): one leaked `/tmp/sesh-conformance-*` daemon
from 2026-08-29, killed by explicit pid with no suite running.

DEPLOY: **api 47→48 + store migration 24→25 ⇒ rebuild AND supervised restart on all six**, plus a
myrig render (no conf change, no binding change, so no `source-file`). Additive and mixed-mesh safe in
both directions: the derivation is owner-side, so a pre-48 VIEWER reads the correct `on_hold` either
way, and a pre-48 OWNER simply ignores the unknown column and keeps the old behaviour. Take the usual
pre/post `VACUUM INTO` backups with row counts — migration 25 is a bare ADD COLUMN and touches no
existing data, but the fleet convention is belt-and-braces.
**DEPLOY RESULT (2026-08-30).** ALL SIX at sesh abd5816 + myrig 1a8e94d, every installed binary
`vcs.modified=false`, every checkout verified clean BEFORE pulling (the deploy script REFUSES a dirty
one — H49/H63). Migration REHEARSED first against a fresh `VACUUM INTO` copy of mymain's real
1,841-thread store: 24→25, every count byte-identical, `hold_release_until` present and 0 everywhere.
Then per machine: pre-deploy `VACUUM INTO` backup with counts → build → `.new` + `mv` → supervised
restart → schema/api check → post backup. Pre→post counts BYTE-IDENTICAL on every machine:
**mymain 1,841/372/40; macbook 120/44/0; macstudio 25/1/0; ideapad 6/1/0; pocket4 27/3/0; termux
0/0/0** (`~/.sesh/backups/sesh-{pre,post}-v25-h103-20260830.db`). Every daemon reports store schema
25 and api 48. **Termux has no `sqlite3`**, so its backup is a plain file copy taken while the daemon
was STOPPED (consistent, and its own store is 0/0 — everything it shows is the replicated peer
cache); it was built with plain `go build` (go1.27.0 android/arm64, H22), the old daemon killed by
EXPLICIT pid 13882, and the zshenv login guard relaunched it as pid 14990 (H36). Build BEFORE the
kill there, so the phone's downtime is only the seconds until the guard fires. myrig rendered on all
six (python3; the Macs via `uv run --with jinja2`, H46) and verified in FRESH login shells. Mesh
healthy afterwards: all five API peers reachable and synced 0s, doctor clean.
**LIVE-PROVEN on the real fleet, on the exact thread from the diagnosis:** `315f89fc
scuttlebug-dagster-build-2` (in the DOC, parked by `dagster-netrun`) — `--clear` refused loudly
naming the ancestor, `--release` freed it while `dagster-netrun` stayed parked. Left released: it is
an in-DOC thread and being parked was the bug.
**A running SIDEBAR keeps the binary it launched with (H70), so `h` inside Lukas's sidebar still has
the old toggle until `prefix+r`.**
FOLLOW-UP FILED, not built: ticket **3128e9d4** — sesh-ui reads `on_hold` (already release-aware) but
its WRITE surface is pre-48, so an "unhold" in the app on an inherited-held thread still does nothing.

## H102 — THE THREE H99 LOOSE ENDS: Codex 0.151 title-subthread stamp guard, one tmux capture client per maintainer tick, and one WAL leaf per peer delta (2026-08-29, sesh 66d2e6e merged to concurrent main as 1db958e; store migration 23→24, NO API/wire change — schema stays 47; **DEPLOYED ALL SIX** with pre/post DB backups)
Ticket 752c3e66, plus the Codex regression ticket aab369a9. This entry is H102 rather than the
requested H100 because the default-agent and mmt-enter-box sessions landed H100/H101 on main while
this ticket's full matrix was running; never overwrite concurrent history.

**(1) CODEX 0.151'S SECOND NOTIFY WAS A DIFFERENT, UNPERSISTED CONVERSATION.** After a first
headed turn, Codex 0.151.0 generates the session title in an internal sub-thread and invokes the
same notify hook for it. Measured ordering: the real conversation notified at 15:33:45.099 (rollout
already on disk); the title helper notified at 15:33:45.220 with a different id and NO rollout;
the real conversation notified again on the next turn at 15:33:51.915. H62 deliberately lets a
later reported id correct an earlier stored one, so a one-turn thread ended stamped with the helper
id, which Codex itself refused with `no rollout found for thread id`. The four red cells were the
external effect: `thread.resume/codex/local`, `thread.send.headless/codex/{local,remote}`, and
`thread.codex-session-capture`.

The reporter now refuses a Codex id only when a COMPLETE rollout-tree walk positively finds no
matching file (`agents.EphemeralCodexSession` beside the H82 Claude foreign-cwd arm). It never
matches title-prompt text. The asymmetry is load-bearing: a persisted id stamps; certain absence
does not; any read error is uncertainty and therefore does NOT refuse. The inherited
`FindRolloutByID` swallowed `WalkDir` errors, contradicting that rule; it now propagates them, with
a direct missing-tree/empty-tree/persisted-rollout unit. A refused stamp keeps the old id (or empty)
and still accepts the state event, so the next real notify self-heals. Anti-gaming: neutering the
arm made the Codex cells fail with the exact `no rollout found` symptom (resume escaped the ~100 ms
race once, then failed on repeat); reversing the edit restored `reportstate.go` byte-identically
(MD5 `aeea3cdb0013681728cd85afe86dce21`) and all four passed with `-count=1`.

**(2) MYMAIN'S 37-PANE MAINTAINER WAS MOSTLY FORK TAX.** H99 measured daemon own 15.2 % of one
core, **92.2 % in reaped children**, and tmux server 6 %: every ~300 ms tick ran one `ps -e` over
~1,100 processes and one `tmux capture-pane` client per headful pane. `CapturePanes` now sends one
tmux command list for the whole sweep, with a crypto-random line sentinel after each real
`capture-pane -p`. Every returned pane string is byte-identical to single `CapturePane` — essential
because formatting drift would look like activity. Tmux aborts the REST of a command list when one
pane vanished, while preserving earlier stdout and writing `can't find pane: %N`; the batch maps
those earlier captures, drops exactly the failed pane, and retries the remainder. Any other error
fails the tick loudly. The maintainer captures before its worker pool and passes the immutable map
to `refreshThread`; an absent key is the old pane-vanished path.

On Linux, `NewProcSnapshotFor` walks only each marked pane PID's subtree via
`/proc/<pid>/task/*/children` + `cmdline`, depth-bounded exactly like agent resolution. Reading only
`task/<pid>/children` was subtly wrong: Go can spawn the child from a non-leader OS thread, so every
task's file is unioned/deduped/sorted. An unreadable root falls back to the full `ps` snapshot,
never a plausible empty tree; macOS keeps `ps`. Test traps: the pane fixture needed to wait until
its command had rendered, and a 12-row tmux left the fourth pane only one line high and scrolled
`gamma` away — use a 40-row isolated server. Anti-gaming: reversing pane→capture assignment made
the exact equality test red; the reversal restored `threads.go` byte-identically (MD5
`8fcd5dadf0f6c8d14b866536ada6d751`).

**(3) ONE-ROW PEER DELTAS NOW DIRTY ONE 4 KB LEAF.** Migration 24 rebuilds `peer_threads` as
`WITHOUT ROWID`: the `(machine,id)` primary key is the table b-tree rather than a second autoindex.
`UpsertPeerThreads` no longer updates `peer_meta` in the same transaction; view freshness is
authoritative, `applyDelta` marks meta dirty, and the existing 60 s `flushMeta` persists it. A hard
crash can only under-claim freshness on boot — the safe direction. Measured write amplification was
12–16 KB per one-row round before and ~4 KB after. Rehearsed BEFORE deployment against fresh
`VACUUM INTO` copies, never live DBs: mymain schema 23→24 with 1,816 threads / 366 tickets / 35
subscriptions / 178 peer rows / 4 peer-meta rows; termux 0/0/0 / 1,994 / 5. Every count survived
and `sqlite_master` contained `WITHOUT ROWID`.

**GATES, INCLUDING THE UNPRETTY RUN.** Gofmt on every touched file (the three known drift files
untouched); `go vet ./...`; every non-conformance package plain then `-race` (never concurrent);
full TUI claims in 207.340 s; mesh ×6; state-authority ×4; flagged Pi ×2; runtime-state ×6. One
runtime-state Codex remote run under row load stayed busy past its bound, then passed immediately
serially; the final full run passed it. The first full matrix was honestly **244/253**, all nine
reds Claude-only: its real binary said `You've hit your session limit · resets 5:50pm (UTC)` and
the remaining cells timed out busy behind that response. After the reset, a fresh FULL run took
2,455.189 s and was **253/253 pass, 0 fail, 0 skip, 0 missing, 0 not-run, 2 justified N/A**.

Concurrent main then added H100's `thread.default-agent/{local,remote}`, making the merged registry
255 cells. A second back-to-back full real-Claude sweep would likely have exhausted the newly reset
allowance again, so do not misreport one: on the exact merge 1db958e, every non-conformance package
passed plain/race + vet, the two new cells passed, and the affected integration blast passed Codex
4/4 (each its own exact `-run`), runtime-state 6/6, state-authority 4/4, flagged Pi 2/2, mesh 6/6.
The 253-cell full artifact belongs to the feature tree; the two merged additions have focused green
evidence, exactly as H100 records for their own change.

**DEPLOY RESULT (2026-08-29).** Feature commit 66d2e6e; fetched concurrent main ee1ebb1; required
`--no-ff` merge 1db958e; both branches pushed. Per machine: fresh consistent `VACUUM INTO` copy +
verified counts at schema 23 → build from a CLEAN checkout at exactly 1db958e (mymain's real
checkout had another session's uncommitted CLI work, so it was not touched; build used throwaway
`/tmp/sesh-h100-mymain.cXNPOh`) → every binary `vcs.modified=false` → `.new` + `mv` → supervised
restart. Termux built natively with CGO=1, killed only explicit old daemon PID 30380, and a fresh
SSH login's zshenv guard relaunched PID 19521. Macbook was closed/asleep: its exact LAN identity
`macbookpro.mynet` / `ea:a8:b4:5b:15:92` was resolved from Mac Studio, an exact WoL packet opened a
dark-wake window, and a verified 33 KB incremental Git bundle (known base 69db27c → main 1db958e)
avoided the GitHub fetch that repeatedly outlived dark wake; `/opt/homebrew/bin/go` built it and
supervisor relaunched PID 37447. `caffeinate` does NOT defeat closed-lid sleep — do not rely on it.

Pre→post thread/ticket/subscription counts were BYTE-IDENTICAL on every machine (hook mutes also 0):
**mymain 1,820/370/35; macbook 120/44/0; macstudio 25/1/0; ideapad 6/1/0; pocket4 27/3/0;
termux 0/0/0.** Every post DB reported schema 24. Both copies live locally as
`~/.sesh/backups/sesh-{pre,post}-v24-h100-20260829.db` and centrally on mymain under
`~/.sesh/backups/fleet/<machine>-sesh-{pre,post}-v24-h100-20260829.db` (SHA-256 recorded). Final
mymain mesh: self + all four inbound API peers reachable and current; doctors exited 0 on all six
at their deployment check (Termux retains its designed inbound-less/no-Claude/no-Codex warnings).
After the final fleet check, Android remained online but Termux sshd and its daemon briefly died;
when SSH returned, the fresh-login guard self-healed it again as PID 17613. Final recheck: schema
24, revision 1db958e, `vcs.modified=false`, and doctor saw ALL five peers (Macbook included) current.
The transient outage is recorded rather than silently omitted; no store/history changed.

**LIVE MEASUREMENTS.** Two 20 s `/proc/<pid>/stat` samples on mymain with **39** marked panes:
daemon own 19.3–20.0 %; reaped children **8.6–9.9 %** (was 92.2 %, a 9.3–10.7× collapse); tmux
server 8.0–9.35 % (was 6 %). The remaining child cost is the one batched client plus the other
pre-existing tmux probes; same-pane capture work still costs the server, and in-process targeted
walk/parsing makes daemon-own slightly higher than the 15.2 % baseline — report all three, not just
the dramatic win. Termux's first genuine idle-cadence 20 s window: daemon own **0.70 %** (was
0.8 %), reaped helpers 1.75 %, total 2.45 %, RSS 45,548 KB.

**CODEX 0.151 LIVE SMOKE ON THE FIXED DAEMON.** Disposable headed thread 707b24d7 took exactly ONE
turn; its stored session `01a04edf-7ccb-7510-9fea-871888bc4a14` had rollout
`rollout-2026-08-29T18-54-24-...jsonl`. Stop/resume retained that exact resumable id. Fork created
thread 44a7423a with its own persisted rollout-backed session
`66b2f6ca-acc3-4e6d-b6c9-fbf49877e700`. Both records were stopped and deleted.

**SMALL DEPLOY/HARNESS TRAPS:** `status` is a read-only special variable in zsh (the first post
check stopped before writing its backup; use `daemon_json`); `git bundle create A..B` refuses an
empty unnamed bundle (use named `main ^A`, verify prerequisites/tip at both ends); old H99 backup
helpers safely open schema 24 because the migration loop is append-only and do not rewrite it; and
the Codex `/tmp` helper-binary warning remains cosmetic under isolated test homes.

## H101 — mmt-enter-box's "extreme slowness" was the box-checkout PROBE: mesh-reachable ≠ ssh-reachable, and the probe had no timeout of its own (2026-08-29, myrig 991a856; NO sesh change; render-only, DEPLOYED 5/6 — macbook asleep, pending)
Lukas: "why is `mmt-enter-box` so extremely slow? I see no reason why it should be to this extent
when `mt-enter-box` is not. It opens up the fzf relatively quickly, but the slowness ... is the wait
after selecting a box and trying to enter it."

**THE ASYMMETRY IS STRUCTURAL and is the whole answer:** scope=all calls
`_mt_box_checkout_machines` — one ssh per mesh-reachable machine, then a bare `wait` — while
scope=this resolves from `~/dev` and opens **ZERO** ssh connections (MEASURED 0.00s).

**THE DEFECT: the probe keys on the wrong signal.** `sesh mesh`'s `reachable` is an HTTP fact on a
cached cadence; ssh-reachable is a DIFFERENT fact, and the two disagree exactly when a laptop
sleeps. MEASURED on mymain: the mesh reported macbook reachable while `ssh-target macbook true`
took **52.61s** and then FAILED; a later probe was **still hanging at 177s** when the harness gave
up. Healthy baseline 0.41–0.49s over ten consecutive samples. macbook flapped
reachable→false→true→false three times in twenty minutes, and every window where it is still
listed costs the full hang. Same tax on `mmt-enter-box-thread` and `mmcd`, which share the probe.

**THIS CORRECTS H87**, which recorded this exact residual as "brief" and "bounded by ... a ~45s
ceiling". It is neither, and the reason is worth keeping: `ConnectTimeout` covers only the TCP
CONNECT (the wedge connects, then goes silent, falling through to `~/.ssh/config`'s
ServerAliveInterval 15 × CountMax 3 ≈ 45s); **ServerAlive only applies once the transport is up**,
so a peer that accepts TCP and stalls in key exchange is bounded by NOTHING (the 177s case); and a
slave session on a STALE ControlMaster socket hangs on the mux where neither applies (H94).

DIAGNOSIS METHOD, because the bug is invisible on a healthy fleet: every component measured
sub-second (picker prep identical for both scopes; nav 0.09s; routed `tmux info`/`create-session`
~0.17s each; `zsh -lc wait` 0.06s, killing the theory that a shell-init background job was blocking
the bare `wait`). It only surfaced when macbook slept MID-INVESTIGATION. **The one command that
settled it: print the mesh's claim and a TIMED real ssh for every machine, side by side.**

FIX: a hard wall-clock deadline, `_MT_BOX_PROBE_TIMEOUT=6`. Four details carry it — `exec` so the
background pid IS the ssh process (a straggler is genuinely killed, not orphaned); BOTH streams
redirected, because a background job still holding the function's stdout keeps the caller's `$( )`
blocked on EOF and would reinstate the very hang the deadline removes; the answer is the probe's
**EXIT STATUS**, never its stdout, so an ssh banner cannot be collected as if it were a machine name
(this also dropped the temp dir entirely); and a machine that misses the deadline is named LOUDLY
and treated as "not checked out there" — never silently, since silence would turn an unreachable
machine into a confident wrong answer.

**ZSH TRAP, cost a debugging round: `assoc[key]=$!` stores the LITERAL "$!"** when the assignment
target carries a subscript (`pid=$!; assoc[key]=$pid` and `assoc[key]="$!"` are both fine). It fails
SILENTLY — every `kill -0` then misses, so the deadline never sees a straggler and the function
returns instantly having waited for nothing. Commented at the site.

VERIFIED: identical results to the old code on five real boxes ([mymain], [ideapad], [ideapad],
[macstudio], []); healthy timing unchanged at 0.42–0.50s; against a wedged peer 6.12s instead of
52s-unbounded, with the loud line; multi-machine aggregation still correct and sorted (stub-driven —
no box is currently checked out on two machines, and fabricating one in `~/dev` is forbidden); no
orphaned processes; `zsh -n` clean; scope=this untouched.
**CAUTION on cleanup: my process sweep matched a bare `sleep 120` and killed two processes belonging
to OTHER agents' polling loops** (harmless — those loops just polled a cycle early, and both parents
were verified still alive). Match on the stub's PATH, never on a generic command line.

DEPLOY: render-only — no conf/binding change (so no `source-file`), no daemon restart, no sesh
binary change. **5/6 at myrig 991a856** — mymain (local), ideapad + pocket4 + termux (python3),
macstudio (`uv run --with jinja2`, H46) — each verified in a FRESH login shell (the constant is 6,
the loaded function is the bounded one, and a real probe returns the right machine). **macbook
ASLEEP** (ssh :22 timed out, twice) → PENDING, harmless: it just keeps the unbounded probe until it
catches up. When it wakes: `cd ~/mysetup/myrig && git pull && uv run --with jinja2 python3
scripts/install-home.py "$MYRIG_TARGETS"`.

## H100 — CONFIGURED DEFAULT AGENT: `[defaults] agent = "pi"`; owner-resolved, explicit override, no hidden built-in (2026-08-29, sesh 9a73116 + myrig d8044a2; NO schema/wire change; DAEMON rebuild + restart; DEPLOYED 5/6 — macbook asleep, pending)
Ticket 978c8543: "Does sesh have a way to set the default agent harness … If not … add [it] … set the default to pi." It did NOT for the CLI/API: `thread new` rejected an omitted `--agent`. sesh-ui separately already had `ui_config.toml default_agent`, but that is only a modal preselection (and an empty value already falls back to pi in the app); it is deliberately not the mechanism default.

**DESIGN.** New `[defaults] agent = "claude"|"codex"|"pi"` in `~/.sesh/config.toml`. Resolution lives in the OWNING daemon, not the invoking CLI, so `thread new --machine X` uses X's policy and every API client gets the same contract. Precedence is explicit request > fork source's agent > configured default > loud 400. There is NO built-in fallback: unset still says agent is required, and an invalid configured value prevents daemon startup while naming the field/value. Virtual/divider threads branch before resolution and still have no agent. This is an explicit user-owned policy exception to SPEC §6's no-magic-default rule, not a silently shifting program default.

**SURFACE + POLICY.** `--agent` is now optional in `thread new` help, always remains the per-call override, and the sesh-cli skill distinguishes this daemon default from sesh-ui's display-only setting. myrig's rendered `home/.sesh/config.toml.jinja` sets `agent = "pi"` fleet-wide. Defaults are loaded at daemon startup, so changing them requires a service-manager restart.

**HONEST TEST.** New matrix row `thread.default-agent` (agent-agnostic × local/remote): target config says pi, a REAL `thread new` with no `--agent` spawns a REAL pi process in a real marked tmux pane; remote drives the real `--machine`/ssh hop; an explicit headless claude record proves override; unset refuses; invalid config prevents daemon start. Anti-gaming: neutering the configured assignment turns the local cell red with the exact required-agent refusal; restored and both cells green. Config units cover parse/unset/invalid. GREEN: config/daemon/cmd plain + race, every non-conformance package sequentially, `go vet ./...`, focused matrix 2/2. FULL MATRIX NOT RUN: 255 cells total after this row; only the focused 2 were run (2 pass, 253 not-run, 2 justified N/A).

**DEPLOY.** LIVE on mymain, macstudio, ideapad, pocket4 and termux: each active `~/.sesh/config.toml` has `agent = "pi"`; each installed binary reports `vcs.revision=9a73116` + `vcs.modified=false`; supervised daemons were restarted only with `supervisorctl`; termux's old explicit pid 19928 was killed and its zshenv guard relaunched pid 30380. The mesh is healthy for all awake machines. The durable myrig template is d8044a2; active configs were patched exactly rather than running the whole home renderer because mymain carried another agent's uncommitted `shell.sh.jinja` WIP (deploying all home files would have shipped it). LIVE SMOKE on mymain: an isolated headless `thread new --name default-agent-deploy-smoke --cwd /tmp --no-parent` with NO `--agent` recorded `agent_kind=pi`; the disposable record was then deleted. **macbook ASLEEP** (SSH timeout and mesh unreachable) → pending; when it wakes: pull sesh+myrig, build `.new`+`mv`, render home (or add the exact config line), and `supervisorctl restart sesh-daemon`. Mixed versions are safe: no schema/wire change.

## H99 — TERMUX RESOURCE DIAGNOSIS → THE MESH SCALE PASS: per-thread peer cache rows, diff-fed eventer, O(live) maintainer sweep, hash reconcile (2026-08-29, sesh 0b77422 merged to main as 69db27c; store migration 22→23, NO api/wire change — schema stays 47; DAEMON rebuild + supervised RESTART; **DEPLOYED ALL SIX**; fleet DB backups taken before AND after)
Lukas: "explore the possibility of optimizing sesh for Termux. I'm worried that it consumes too
many resources on my phone… diagnose the issue first and then figure out ways to improve it."
Then, on seeing the diagnosis: archived threads must NEVER be deleted, and sesh "should be designed
such that you can have tens of thousands of archived threads… once they're archived they're just
in history". Design record: `_dev/MESH_SCALE.md` (MESH.md's blob-storage section superseded).

**THE DIAGNOSIS, all measured on the phone (`/proc` deltas, per-task ticks, `/proc/<pid>/io`, adb
batterystats/top; the method is the reusable part):** the sesh daemon burned **15–18 % of a core
CONTINUOUSLY with ZERO local threads and ZERO forks** — pure in-process Go work, spread evenly over
its OS threads (a GC/hot-loop signature), 160 wakeups/s, 40 MB RSS — the **#3 CPU consumer on the
entire phone** behind system_server and the media provider, running 24/7 under Termux's permanent
wake lock (77 % of the phone's partial-wakelock time). Cause, proven with an on-device benchmark
against a copy of the real store: **the eventer re-loaded every peer's snapshot blob from SQLite and
JSON-decoded the ENTIRE replicated mesh every 1 s tick — 1.48 MB / ~1,990 threads → 146.9 ms per
tick = 14.7 % of a core, 7.3 MB/s of garbage.** mymain's snapshot alone is 1.38 MB: **1,810 threads,
1,763 archived**. With the TUI open the daemon doubled to 32 % and wrote **88 KB/s to flash**: at
active cadence every delta round re-marshaled the whole working set and rewrote the 1.4 MB blob.
Everything else was small: the maintainer's zero-thread early-out works; `sesh-current-status`
(a `zsh -lc`, ~0.9 s wall every 15 s) ~1.8 % [CORRECTED 2026-09-15, H108 follow-up 3: it runs about
once per SECOND per attached client, not per 15 s, and on termux every run leaked an ssh-agent]; tmux servers ~1 %; the cockpit's 5 ssh links ~0.
Fleet-wide, not phone-specific: macbook (hooks-pinned, full 1 s cadence) 6.6 % on battery, ideapad
3.1 %. NOT sesh's fault: the phone is memory-starved (swap 96–98 % used) [CORRECTED 2026-09-15, H108
follow-up 3: ~1.9 GB of that was ~2,000 leaked ssh-agents under the Termux uid, invisible from inside
Termux, fed by the cockpit's own status line] — that is system_server +
kswapd, the load average of 4–8. **Also confirmed en route: H84's phantom-killer setting SURVIVED
A REBOOT** (`max_phantom_processes=2147483647` at 17h46m uptime) — that open item is closed.

**WHY ARCHIVED THREADS COST ANYTHING — the structural answer Lukas asked for.** The WIRE was already
incremental (H44 delta sync: an archived row transfers once). Every layer on either side of it
treated a machine's thread set as ONE homogeneous value, re-processed whole per tick: the owner
swept all 1,810 records every 300 ms (`ListThreads(true)` + refreshThread + a `DeepEqual` each);
the observer stored each peer as one JSON blob (any one-row change = full re-marshal + full
rewrite), kept 3+ decoded copies (meshsync `working`, eventer `prev` + per-tick `cur`), re-decoded
the world per second in the eventer and per request in `/v1/mesh`, and loaded the full blobs to
answer one-bit questions (mastermaint's reachable flag, the fan-out gate, the subscriptions owner
lookup). MESH.md had recorded the blob as a deliberate simplicity choice ("keeps it dead simple");
the archive grew ~30× under it. At 10k archived (~7.6 MB) the phone's eventer tick alone would be
~750 ms — the daemon pegged doing nothing.

**LUKAS'S DESIGN, which IS the fix ("why can't we just have a local cached database of all the
threads across machines and only poll for diffs, and periodically do a full check that we haven't
diverged?"):** exactly right, and it maps onto the system as C1–C4 below; the only thing it needed
adding was that his "not every 300 ms" conflates the OWNER's local pane-probe loop (which stays at
300 ms but must stop touching archived records) with the cross-machine sync (already 1 s/60 s and
diff-based on the wire). An interim "write-behind blob checkpoints" fix I had designed was
SUPERSEDED before being built: per-thread rows make eager writes cheap and exact instead.
- **C1 — per-thread peer cache rows** (store migration 23: `peer_threads(machine,id,snapshot JSON)`
  + `peer_meta(machine,synced_at,reachable)`; blobs converted via JSON1 `json_each` guarded by
  `json_valid` — a corrupt row is skipped like the old undecodable-blob path, the cache is derived
  data; `peer_snapshots` DROPPED so a rolled-back binary fails LOUDLY instead of serving a frozen
  blob; `revs` table + AFTER INSERT/UPDATE/DELETE TRIGGERS on threads+tickets). **Verified JSON1 is
  present in modernc.org/sqlite before relying on it** (1,987 rows extracted from the real termux
  DB). Rehearsed row-for-row against copies of the REAL termux (1,987 rows / 5 machines) and mymain
  (178 / 4) stores: zero mismatches; on-device migration of the real cache 1.2 s.
- **C2 — one shared view + diff-fed events** (`internal/daemon/meshview.go`): ONE decoded copy,
  seeded from rows at boot (silent baseline), updated by meshsync's transitions — rows FIRST, then
  view, then `(old,new)` pairs to the eventer (`DeepEqual`-filtered: no phantom pairs from a
  formatting-only refetch). **The eventer's 1 s ticker is GONE**: `observe(pairs)` fires the same
  events with the same empty-string guards; zero work when nothing changed; an edge can never be
  MISSED (the property H44's hooks-pin protected — hooks still pin cadence, but for latency now,
  not correctness). Touch is view-only with a 60 s `flushMeta` + flush on shutdown — a crash can
  only UNDER-claim boot freshness; `markUnreachable` persists eagerly. `contentWrites`/
  `rowsWritten` counters make O(changed-rows) writes test-observable.
- **C3 — O(live) maintainer sweep**: tick reads `ThreadsRev()` (one integer, trigger-bumped —
  STRUCTURAL: no write path can dodge it); unchanged rev + no hold deadline passed ⇒ sweep only the
  UNSETTLED set (marked pane, shell session, in-flight headless turn, authority entry, or a
  last-published live state), record list + ticket digests cached between full sweeps; `RuntimeIndex`
  still runs every tick (a hand-stamped pane marker has no record write). Hold expiry (OnHold flips
  with NO write) forces the full sweep via `nextHoldExpiry`. `publish()` emits the pair, suppressed
  during the FIRST (baseline) sweep. Counters `fullSweeps`/`sweptThreads`.
- **C4 — hash reconcile** (Lukas's addition): hourly, ONLY off a provably-quiet round (304/empty
  delta — otherwise a mismatch could be ordinary staleness), the observer sends a `snapshotETag` of
  its own view as `If-None-Match`: 304 proves byte-identity for ~100 B, a 200 is a LOUD log + heal
  with the full payload in hand. Zero API change. ssh peers full-fetch every round anyway.

**THE A/B THAT SETTLES IT** (two isolated staging daemons ON THE PHONE, one per binary, each
syncing READ-ONLY from the real five peers — 1,988 threads, pocket4 back online — identical
phases: sync, 75 s cooldown past the 60 s demand window, 120 s idle, 90 s with a TUI-shaped 3 s
`mesh --json` poll): **idle 10.2 % → 0.7 % of a core; active 23.7 % → 3.2 %; idle flash writes
4.4 MB → 287 KB per 2 min; RSS 62 → 36 MB.** Active-phase writes stayed ~90–120 KB/s in BOTH runs
because the full matrix was churning hundreds of real threads on mymain at the time — genuinely
changed rows. RECIPE WORTH KEEPING: peers.json with tailnet IPs (not names) lets a
`CGO_ENABLED=0 GOOS=android` cross-build work on the phone (H22's constraint is DNS only); ship the
script by scp (H83); own `SESH_HOME`/sockets, no API; poll `mesh` only in the setup phase — a mesh
read is DEMAND and would falsify an idle measurement.

**TRAPS AND FINDINGS:**
- **`thread.notify/-/remote` does NOT exercise the remote event chain** — it PASSED with view
  emission neutered (1.7 s: it only round-trips the routed toggle). Found because the neuter run's
  output was first swallowed by my own grep (a neuter you cannot SEE proves nothing — H88 again);
  re-run visibly, then the honest guard was identified: `daemon.hooks/-/remote`, the observer-bound
  hook on a PEER thread's real turn, goes red with "the LOCAL hook never fired for the PEER
  thread's edge". All four neuters now discriminate with exact messages (view emission →
  daemon.hooks/remote; boot seed → the NEW cold-boot step in mesh.offline-listing; publish emission
  → daemon.hooks/local + maintscale; unsettled selection → maintscale), each reversed
  byte-identically (md5-checked) and re-run green.
- **The live termux daemon DIED during the A/B window and self-relaunched** (H36's zshenv guard,
  new pid 7386, correct env, old binary, store untouched at v22). Almost certainly Android memory
  pressure at ~97 % swap. NB a staging daemon MATCHES the guard's `pgrep -f 'sesh daemon run'`,
  so while one runs the guard would NOT relaunch a dead live daemon — harmless here, worth knowing.
- **`-run 'TestMatrix/mesh'` also matches `daemon.mesh-read`** (substring on the path element).
- **codex 0.149.1 → 0.151.0 landed on mymain since H93 and BROKE resume of headed-TUI codex
  sessions**: `thread/resume failed: no rollout found for thread id …` plus a new "Refusing to
  create helper binaries under temporary dir /tmp" warning. Reproduced BYTE-IDENTICALLY on a clean
  worktree at base 48be613 ⇒ pre-existing, NOT this pass. Isolated probe: `codex exec` + `codex exec
  resume` work under BOTH a /tmp and a non-/tmp CODEX_HOME (the /tmp warning is cosmetic), so the
  break is specific to sessions the headed TUI creates — that is `thread.resume/codex/local`,
  `thread.send.headless/codex/{local,remote}` (headed-born) and `thread.codex-session-capture`.
  Ticket aab369a9 (triage, full repro + narrowing probe inside); Lukas's live codex threads may be
  affected on revive — CHECK before assuming.

GREEN: `go vet ./...`; gofmt on every touched file (the 3 pre-existing drift files untouched);
store units incl. `TestMigrationBlobToRows`; internal/daemon plain + `-race`; the full
non-conformance sweep; 25 blast-radius cells (mesh ×6 incl. the delta byte-proxy and the new
cold-boot step, route.parity ×2, daemon.doctor, daemon.hooks ×2, thread.notify ×2,
thread.state-authority ×4 real claude+pi, thread.flagged/pi ×2, thread.hold ×2, shell.lifecycle
×2, thread.subscribe ×2, thread.await/pi ×2); the FULL TUI claims suite (209 s). **FULL 253-CELL
MATRIX: 248 pass, 5 fail, 0 skip, 0 missing, 0 not-run, 2 n/a** — every red pre-existing:
`thread.resume/pi/remote` passes serially (load flake, the H8/H62 class); the four codex cells are
the 0.151.0 regression above, identical on the base commit. Read the grid with `go run ./cmd/sesh
matrix grid` (a passing `go test` streams nothing — H93).

**BACKUPS (Lukas: "make sure to back up the thread databases and stuff so that we don't
accidentally delete any of my history once we migrate").** Migration 23 never touches `threads`/
`tickets`, but belt-and-braces: a consistent `VACUUM INTO` snapshot of every machine's live
`sesh.db`, verified by opening the COPY and counting rows — mymain 1,810 threads/363 tickets/35
subscriptions, macbook 120/43, macstudio 25/1, pocket4 27/3, ideapad 6/1, termux 0/0 = 1,988
threads, cross-checking the replicated corpus exactly. Two copies each: local
`~/.sesh/backups/sesh-pre-v23-20260829.db` and central `mymain:~/.sesh/backups/fleet/` (+
`MANIFEST.txt` with both recovery paths + `blobs-*.tgz` of the four non-empty blob stores).
**The DESIGNED rollback needs no backup**: reinstall the old binary and recreate the empty
`peer_snapshots` table (one CREATE TABLE, in the manifest); the cache refills in seconds.

**DEPLOY RESULT (2026-08-29, Lukas: "merge and deploy"):** merged `--no-ff` as 69db27c, then ALL
SIX in parallel, each: fresh `VACUUM INTO` backup with counts → build from the machine's clean
checkout at 69db27c (every binary `vcs.modified=false`) → `.new`+`mv` → `supervisorctl restart
sesh-daemon` (termux: old pid killed explicitly, the zshenv guard relaunched pid 9633 with the
right four SESH_* vars, H36) → `schema_version: 23` → a POST-migration backup whose counts are
BYTE-IDENTICAL to the pre-deploy ones on every machine (mymain 1,813/364/35, macbook 120/43/0,
macstudio 25/1/0, pocket4 27/3/0, ideapad 6/1/0, termux 0/0/0) → mesh: all five peers reachable,
synced 0 s, every converted cache serving (live fan-out 1,991 rows), APIs bound, doctor clean.
Backups live at `~/.sesh/backups/sesh-{pre-deploy,post-v23}-20260829.db` per machine + the
fleet dir on mymain. LIVE NUMBERS AFTER (real daemons, `/proc` deltas): **termux 0.8 % of a core
at idle cadence (was 15–18 %), RSS 34 MB (was 40–44), 176 KB/90 s of writes**; ideapad 1.4 % (was
3.1 %); macbook 3.5 % hooks-pinned (was 6.6 %); **mymain 13.7 % — and that is NOT this pass's
territory: it has 37 HEADFUL panes, each `capture-pane`d every 300 ms plus a `ps -e` snapshot per
tick (~125 forks/s), the pre-existing L1 probe cost MESH.md §3 itself flags ("if N ever gets
large, throttle"). Next lever for an owner with many live panes: stagger/throttle captures and
cache the `ps` snapshot across ticks — a separate change.** ALSO MEASURED, worth knowing: at
ACTIVE cadence (a TUI open) termux still writes ~30–50 KB/s — one SQLite transaction per delta
round (WAL page overhead on a one-row change), not payload; idle cadence is the phone's steady
state and is near zero. Coalescing rounds into fewer transactions is a possible follow-up if the
phone's TUI-open time ever matters. NB the macstudio ssh hit the H94 stale-ControlMaster trap
mid-deploy ("Session open refused by peer"); it fell back to non-multiplexed and completed,
socket cleared with `ssh -O exit` afterwards.

**FOLLOW-UP DESIGNED, NOT BUILT** (BACKLOG #6): stage D — `/v1/mesh?since=` client deltas,
serve-from-rows (RAM O(live)), daemon-side archived search. Trigger: the phone's TUI poll or a
>10k-thread mesh.

## H98 — the cockpit ,/. ring became a FLAGGED working set, and the sidebar cursor now TRACKS the cockpit (bell-nudged) (2026-08-28, sesh ceb3f03 + myrig 026cc85/de6bd65; NO schema/API/CLI-flag change; BINARY-ONLY, no daemon restart; DEPLOYED 5/6 — pocket4 offline, pending)
Three asks in one session, arriving in sequence, each changing the last.

**(1) `prefix+,` / `prefix+.` cycle the ACTIVE VIEW, not every headed thread** (myrig 026cc85).
The keys already existed and cycled every headful thread mesh-wide in (machine, created, id)
order. MEASURED: 41 threads, **12 of them ON HOLD** — nearly a third of the ring was threads he
had deliberately parked, which is exactly what hold is for. The set is now the active view's
predicate copied from `builtinViewAdmits` — (flagged OR not archived OR headful OR busy) AND NOT
on hold — and the ORDER is the TUI's own tree walk from `internal/tui/filter.go`: roots sorted
(machine, name, id) with PINNED roots first by fractional key, each root followed by its
children, a child whose parent the view filtered out promoted to a root. Implemented as one jq
program over `thread grid --all-machines --archived`.
**THE VERIFICATION THAT EARNED ITS KEEP:** rather than trust the reimplementation, I captured a
REAL `sesh tui --all-machines --expand` in an isolated tmux and diffed its rendered rows against
the jq order — 42 rows, position for position. It immediately caught that my order was missing
the 3 DIVIDER rows... except it hadn't: dividers have EMPTY names and my comparison script
filtered empty strings. **A diff that disagrees is not yet a bug — check the harness first.**

**(2) THE RING IS NOW THE FLAGGED SUBSET** (myrig de6bd65), plus `prefix+f` to toggle the flag.
He asked for a "no flagged active threads" popup, which only makes sense if the ring is flagged;
asked, and he confirmed the narrowing ("I meant that it should cycle through flagged active
threads"). So: flag the threads you are juggling, `,`/`.` rotate between them, `prefix+f` curates.
DESIGN POINTS. The walk is still built over the WHOLE active view and the flagged rows selected
FROM it, so they keep their on-screen positions. An on-hold thread stays out even when flagged,
because hold beats flag in the view itself. `sesh thread flag` deliberately has **no toggle verb**
(explicit `--on`/`--off` only), so the toggle is myrig policy: read `.thread.flagged`, set the
opposite. `f` was free on BOTH prefix tables.
**A `run-shell -b` BINDING HAS NOWHERE TO PRINT** — that is why the old empty-ring case looked
like a dead key, and why `_mt_flash_popup` exists. It obeys THE WHICH-CLIENT LAW (client from
`$SESH_NAV_CLIENT`, pane from `$SESH_MT_PANE`): tmux cannot map a run-shell subprocess back to the
client that pressed the key, so without it the flash lands on an arbitrary terminal. The two empty
cases are reported DIFFERENTLY because they need different actions — "No flagged active threads."
vs "Flagged threads are all dead." NB its text is interpolated into the command display-popup
runs, so it must stay a literal owned by that file.
RE-HIT the H89 gotcha deliberately: the possibly-empty `session_name` goes LAST in the `@tsv`,
because an empty field in the MIDDLE collapses under `IFS=$'\t' read` and shifts every later one.

**(3) THE SIDEBAR CURSOR NOW TRACKS THE COCKPIT** (sesh ceb3f03) — "I don't think the current
bindings do this which makes it a bit confusing". Correct: `WithMasterCursor` resolved the master
window's current thread ONCE at startup and never again, so every cockpit-side move left the `>`
behind. Conferred first; he chose continuous tracking, leave-the-cursor-alone when the thread is
not in the view, and — his own refinement — a dedicated poll **with a way to force it**.
**THE RULE THAT MAKES IT SAFE, and it is the whole design: act on a CHANGE in what the cockpit is
showing, never on "the cockpit disagrees with the cursor".** They are not the same. Arrowing onto
a row the follow policy skips (a headless thread — a preview must never revive) leaves the cockpit
where it was, so a disagreement-driven tracker would yank the cursor straight back on the next
tick and make the sidebar unbrowsable. `lastMasterThread` records the last OBSERVED value and only
a differing resolve moves anything — which also makes the sidebar's own follows self-cancelling
and removes any need for an epoch/sequence guard against a stale in-flight resolve.
**THE NUDGE IS A BELL, NOT THE ANSWER.** `sesh tmux nav` writes `<home>/nav-bell` at ONE seam
(`tmuxNav` wrapping `tmuxNavRun`, so a fifth nav path added later inherits it rather than silently
not ringing). It says only "the cockpit moved" and carries no claim about WHERE, because a nav may
have targeted a different origin master or a window this sidebar is not showing — the
authoritative read stays `MarkerClientCurrent`. Every 250ms tick costs one small file read; only a
rung bell or an expired 3s backstop spends a real resolve (two tmux calls, plus a mesh round trip
when the active window is REMOTE). That is what buys immediacy without a per-second cross-machine
call on an all-day process. A no-information resolve (failed, or no cockpit context) must NOT be
recorded as "the cockpit shows nothing", or the next success looks like a change and jumps the
cursor — hence `masterCursorMsg.ok`.
Out-of-view threads leave the cursor alone, deliberately NOT `goto-uuid`'s escalate-to-a-view-that-
shows-it: that is right for a command someone typed and wrong for an ambient tracker that would
retitle the list under a reader. No preselect is armed either — it would land minutes later.

TESTS. Units for the cadence truth table, the change-not-disagreement rule, the no-information
case, the shell-pane observation, the out-of-view no-op, the bell reader, and that a plain `Init`
stays the LONE fetch cmd. New claim `sidebar-tracks-cockpit` (registered AND declared — the H25
gotcha) with nothing stubbed: real daemon, two real pi threads, a real tmux client attached to the
work server and recorded in a real master-client marker, a real `sesh tmux nav` subprocess, the
real bell, the real resolve — and it asserts the BASELINE cursor position first so it cannot pass
vacuously. ANTI-GAMING (reverse-edited, never git-checkout — H44; `-count=1` — H75): the
disagreement rule, never moving the cursor, arming a preselect, ignoring the bell, and removing
the bell from nav each turn something red, the second reproducing the report verbatim ("the
sidebar cursor never followed the cockpit onto track-b").
**TRAP, and it cost a debugging round: `renderUntilRow` drives `Init()()` expecting the lone fetch
cmd a plain `Init` returns.** A model built with tracking already armed hands the harness a
`BatchMsg` instead of the mesh, and NO row ever appears — which reads as "the daemon never
published" rather than "your Init changed shape". The claim arms tracking AFTER the first render.
**SECOND TRAP, self-inflicted: I restored a neutered file from a snapshot taken BEFORE I had
appended a function to it, silently deleting the function.** `go build ./...` stayed green because
the lost function was only used by tests. Snapshot-restore is not `git checkout`; re-verify what
you restored, and prefer reversing the exact edit.

GREEN: internal/tui plain and -race; internal/tmux; cmd/sesh; `go vet ./...`; the tmux.nav rows
local and remote (real tmux + real ssh hop) 7/7; the FULL TUI claims suite serially — **70 pass, 0
fail**, no pre-existing reds on this box. The full 253-cell matrix was NOT run — do not read this
as all-green.
LIVE-PROVEN read-only on the real fleet: the flagged ring resolves to the 3 flagged enterable
threads in the TUI's tree order (next→first, prev→last); all four empty/edge cases driven through
the real code with a stubbed grid; the toggle round-tripped false→true→false→true on a disposable
headless thread with the daemon record re-read each time, then deleted; the popup rendered for real
in an isolated tmux with a NESTED ATTACHED CLIENT and captured from the client's own screen (an
empty server has no client, so `display-popup` silently draws nothing — that first attempt proved
nothing); and `sesh tmux nav` rang the bell on the LIVE mymain daemon via a deliberate NO-OP nav
(target = the session the master was already on, so nothing moved for Lukas).

DEPLOY: **binary-only, NO daemon restart, no schema/API/wire change** (a pure TUI-client feature)
plus a myrig render + conf `source-file` (prefix+f is a NEW binding, so running servers need it).
**LIVE ON 5/6** at sesh ceb3f03 / myrig de6bd65 — mymain, ideapad, macbook, macstudio, termux
(plain `go build`, CGO_ENABLED=1 / android verified on the box, H22; install-home logged to `$HOME`,
/tmp is unwritable there — H38). Every installed binary `vcs.modified=false`; every checkout
verified clean before pulling (H49/H63); prefix+f verified bound on every running work and master
server; the functions verified in FRESH login shells. **pocket4 OFFLINE** (ssh :22 timed out) →
PENDING, harmless; when it returns: `cd ~/mysetup/myrig && git pull && python3
scripts/install-home.py "$MYRIG_TARGETS"` then `cd ~/mysetup/sesh && git pull && go build -o
~/.local/bin/sesh.new ./cmd/sesh && mv -f ~/.local/bin/sesh.new ~/.local/bin/sesh`.
**A running SIDEBAR keeps the binary it launched with (H70), so the tracking does not exist inside
Lukas's sidebar until `prefix+r`** (or mmt-kill/mmt-start). Told him.
CONCURRENT SESSIONS twice: myrig gained two gh_runner commits and sesh gained H97's head glyphs
mid-flight; both rebased cleanly (H97 touches the same TUI package but different files), and the
claim was re-run green AFTER the rebase rather than assumed.

### H98 follow-up — the flag now shows in the base STATUS LINE, and doing it uncovered a field-collapse bug that had been there all along (2026-08-28, myrig 01eb71e; still NO sesh change; render-only, DEPLOYED ALL FIVE)
Lukas: "Would it be possible to show in the tmux status bar whether it's flagged or not?"
It was nearly free. The work server's top status row (`status-format[0]` →
`sesh-current-status`) already renders the thread owning the current pane and already
fetches that thread's whole record, so the flag cost NO extra call — and that row is what
you see INSIDE the cockpit anyway, because a master window is an attach into the machine's
work server. Renders `⚑ FLAGGED` red and `⌁ auto-flag off` grey, the TUI's own gutter
glyphs, so both surfaces read the same vocabulary. Marker-only, matching the existing
`🗄 archived` precedent.
**MEASURED: tmux EXPANDS `#[...]` inside the output of a `#()` command**, so a status
script can colour its own text (captured `^[[31m` on the glyph from a real attached
client). The markers must stay LAST on the line — they open a colour and never close it,
which is only safe at end of line, because a tmux format has no "restore previous" and the
conf owns the surrounding style. A comment says so at the site.

**THE PRE-EXISTING BUG THIS TURNED UP, and it is the durable lesson: `IFS=$'\t' read`
COLLAPSES consecutive tabs, because tab is IFS *whitespace*.** `sesh-current-status` had
always split its jq row that way, so a thread with no tags that was archived arrived as
`tags="1", archived=""` — the status line showed a tag literally called "1" and no
archived marker. A NAMELESS thread was eaten the same way from the front (leading IFS
whitespace is stripped). Adding two more optional fields would have made it worse. Fixed
with `${(@ps:\t:)row}`, which preserves empty fields — verified across all-empty-tail,
flag-only, tags+archived+flagged and nameless. This is the H89 gotcha one layer down, and
H98's own ring hit the same family (session_name moved last in its `@tsv`). **Prefer
`${(@ps:\t:)}` to `IFS=$'\t' read` whenever ANY field can be empty.**

Also: a toggle now repaints the owning machine's status line at once
(`_mt_refresh_status_on`). tmux's `status-interval` is its 15s DEFAULT (never set in these
confs) and the sesh row is a `#()` command re-run only on that beat, so prefix+f looked
like it had done nothing for up to fifteen seconds. It refreshes EVERY client rather than
"the current" one — `refresh-client` with no `-t` picks an ambient client (THE
WHICH-CLIENT LAW) and repainting a status line is harmless to all of them; the remote hop
is backgrounded and best-effort, since a keypress must never wait on ssh. Proven
DISCRIMINATING: with the underlying state changed, the bar was still stale 1s later and
repainted the instant the helper ran.
DEPLOY: render-only, NO conf change (so no source-file) and NO restart — the status bar
re-runs `zsh -lc 'sesh-current-status …'` from the rendered shell.sh on its next beat, so
it picks the new function up by itself. ALL FIVE at myrig 01eb71e; verified with the
DEPLOYED code on ideapad (unflagged → no marker, flagged → red ⚑, flag restored) and on
mymain end-to-end. pocket4 still offline.
NOTED, not mine: a concurrent session landed `5b38667` putting `C-a f` on termux's
on-screen extra-keys "to toggle the thread flag" — i.e. it DEPENDS on H98's new prefix+f
binding existing. No collision; the two compose.

### H98 follow-up 2 — "the sidebar doesn't move" was NOT the tracker: `display-message -c` does not scope a format, so master-current returned ANOTHER master's thread; plus prefix+f was paying 1s for a boolean and 0.6s for a popup (2026-08-28, sesh 16bfb0a + myrig fa04657; NO schema/API/CLI change; DAEMON rebuild + RESTART; DEPLOYED 4/5 — termux sshd down, pocket4 offline)
Lukas: "Why is the prefix+f so slow? Also I just tried the ,/. commands and the sidebar
doesn't move its selected row with it. You can verify it yourself by starting mmt-start
here and driving it through there."

**THE SIDEBAR BUG WAS IN `MarkerClientCurrent`, NOT IN THE TRACKER — and it had been
there since master-current was built.** It resolved with `display-message -p -c <client>
-F …`, but **`-c` says where to PRINT, not what to expand the format against**, so the
format resolved against whatever tmux ambiently considered current. MEASURED with two
real clients on two sessions, each pane carrying its own `@sesh-thread-id`:
    -c /dev/pts/43 => THREAD-B | B      <- WRONG, that is the OTHER client
    -c /dev/pts/67 => THREAD-B | B
**The ambient pick follows the most recent `switch-client`** (measured: switch A → both
read A; switch B → both read B). That is why it hid for so long: with ONE master attached
to a work server the ambient client IS the right one, and every quiet machine and every
test had exactly that. It goes wrong the moment several masters watch one machine — the
normal state of a busy box. LIVE on mymain, whose work server had THREE master clients:
before the fix all three origins returned the same session; after it each returns its own
marker client's (mymain→ituc, macbook→mysetup/myrig, termux→empty, its client being gone).
Since the tracker deliberately acts on a CHANGE in what the cockpit shows, a constant
ambient answer meant it never moved — exactly the report. The same call also aimed the
`,`/`.` start point and `mmt-toggle-flag`'s target, so both were mis-resolving too.
FIX: **one `list-clients -F` pass. list-clients iterates CLIENTS and expands the format
once per client**, so the pane-scoped `@sesh-thread-id`, the session and the window index
all resolve against THAT client's own active pane (proven: `/dev/pts/43 tid=THREAD-A
sess=A win=0`, `/dev/pts/67 tid=THREAD-B sess=B win=1`). It still liveness-checks name AND
pid, and is one fewer tmux call than before. **GENERAL RULE: to read anything about a
SPECIFIC client, iterate `list-clients -F`; `display-message -c` is not a client selector.**
NB `display-message -t "=<session>"` is not an escape hatch either — it returned EMPTY,
the `=` exact-match prefix being unhonoured here as it is for set-option/show-options (H89).

**THE CONFORMANCE CELL WAS GREEN THROUGHOUT AND COULD NOT HAVE CAUGHT IT** — the H81
`master.reconnect` shape again, and worth internalising: a cell that exercises the happy
topology proves nothing about the one that breaks. `tmux.master-current` had a single
master client. Strengthened with a DECOY thread + its own attached client. **THE FIRST
DECOY DID NOT DISCRIMINATE — it passed against the old code** — because the master's own
nav was the last `switch-client`, so the ambient pick was right by luck; the cell only
became honest once the decoy is switch-client'd AFTER each nav so it is the last mover.
That is not a contrivance: it is precisely what another master navigating its own window
does. Do not add a decoy and assume it discriminates — NEUTER AND CHECK.
Unit regression in internal/tmux: two real nested clients, both markers must resolve to
their OWN client (one client passes vacuously, so it waits for both), plus a stale marker
resolving to nothing. ANTI-GAMING: the old implementation turns the unit red ("alpha:
thread = THREAD-BETA … resolved ANOTHER client's thread") and the cell red naming the
decoy at both resolve points.

**PREFIX+F SLOWNESS — two causes, both measured, neither guessed.** (a) The toggle read
the flag with `sesh thread info`: **739ms LOCAL and 3308ms ROUTED**, because it also
resolves the pane, both state axes, attachment and the thread's TICKETS for a boolean.
Step-by-step timing put 1033ms of a 1109ms toggle in that one call. `thread list --json
--archived` gives the same field in 80ms/216ms and is what sesh-current-status already
reads (`--archived` is required: an archived-but-headful thread is flaggable and shows in
the active view). (b) **`display-popup -E` BLOCKS its caller until the popup closes** —
606ms for a 0.5s flash — so the confirmation popup cost half a second to say what the
status line was about to say anyway. Dropped from the toggle (the ⚑ IS the confirmation,
and it is repainted immediately); kept for the empty `,`/`.` ring, where there is nothing
else to show. MEASURED AFTER: local 1109→92ms, routed ~3500→274ms.
**LESSON: `sesh thread info` is a DIAGNOSTIC verb, not a field read. Never put it on a
keypress path.**

GREEN: internal/tmux; `go vet ./...`; master.watchers / master.up / master.holding /
tmux.nav-master-multi / tmux.nav-master-http / tmux.nav-window / tmux.master-current; the
FULL TUI claims suite serially — 70 pass, 0 fail. The full 253-cell matrix was NOT run.
LIVE-PROVEN in a REAL cockpit on mymain, as Lukas asked (he was on macbook, and mymain's
master had no clients, so nothing of his was touched): with the fixed daemon and a
REFRESHED sidebar, prefix+. / prefix+, walk the flagged ring and the cursor follows every
time — six presses both directions, wrapping, and landing on a NESTED child whose
ancestors the tracker expands. Test client detached afterwards; the cockpit left as found.
**Its sidebar had been running a binary from 2026-08-19, which is the H70 property and the
OTHER half of why Lukas saw nothing: a deployed fix does not exist inside a running
sidebar until `prefix+r`.**
DEPLOY: **DAEMON change (master-current is served by the daemon) — rebuild AND supervised
restart**, not binary-only. 4/5 at sesh 16bfb0a / myrig fa04657 (mymain, ideapad, macbook,
macstudio), every binary `vcs.modified=false`, mesh healthy after all four restarts.
**termux PENDING — its sshd is down again** (H83); pocket4 still offline.
TRAP: mymain's binary was first built from a DIRTY tree during debugging and stamped
`vcs.revision=<previous> vcs.modified=true`. The code was right but the stamp lied;
rebuilt from the committed HEAD so the fleet's provenance is honest. Build from a clean
tree, or re-build after committing.

## H97 — HEAD-GLYPH VOCABULARY: shell threads were another small round-ish blob; fix = one STROKE CLASS per kind — agent `●`/`◌`, shell `❯`/`›`, virtual `◇`→`≡` (2026-08-28, sesh 70f4710; NO schema/API/CLI change; BINARY-ONLY, no daemon restart; DEPLOYED 5/6 — pocket4 offline, pending)
Lukas: "the current icon for shell threads is not very distinct from the others and it's a bit hard
to make it out by eye sometimes."

**THE DIAGNOSIS IS THE REUSABLE PART, and it is not "pick a nicer shape".** The head cell carried
five sigils — `● ◌ ◇ ▮ ▯` — and every one of them was THE SAME KIND OF MARK: a small shape centred
in one cell, differing only in outline and fill. So the shell pair was structurally a COPY of the
agent pair (solid/hollow) with different corners, and the only thing separating `▮` from `●` was a
few pixels. At one cell a glyph is read by its STROKE CLASS long before its outline, so the fix is
to move a kind to a different CHANNEL, not to another silhouette in the same one. The new rule,
written into HeadGlyph: **one stroke class per kind** — round (agent), chevron (shell), stacked
lines (virtual). `▮`/`▯` had been squeezed into narrow rectangles by H78 because the virtual `◇`
occupied the hollow-quadrilateral family; moving virtual to `≡` freed that family as a side effect.

CONFERRED (AskUserQuestion with rendered gutter previews — the H49 swatch method, which works
because the previews render in HIS terminal font): offered `$`/`~`, `■`/`□`+`≡`, `▬`/`▭`, `❯`/`›`.
He took the chevrons and then asked for `≡` for virtual as well, i.e. the option's shape plus the
family-freeing move from a different option.

**TWO CONSTRAINTS I MEASURED, and the first one killed the idea I started out preferring:**
1. **A pair distinguished purely by INK DENSITY inverts on the selected row.** The selection band is
   `Reverse(true)`, which swaps ink and ground — so `█`/`▓`/`░` (a "terminal block cursor", the
   semantically obvious choice for a shell) would render the live shell as the ghost and vice versa
   on exactly the row you are looking at. Fill-vs-hollow of a BOUNDED shape survives (the outline
   still reads); cell-filling texture does not. LIVE-CONFIRMED the chevron survives: the captured
   selected row is `ESC[7m>  ❯` with no separate SGR, i.e. the terminal's own reverse swap, and the
   wedge is still legible.
2. **Width.** Every candidate is 1 cell per `go-runewidth`, matching the gutter's one-cell-per-axis
   assumption. NB the geometric shapes (old and new) are all EastAsianWidth AMBIGUOUS, so a terminal
   configured to render ambiguous-width as double would misalign the gutter — ASCII is the only
   class that is unconditionally safe. Not a regression (the old set had the same property), but it
   is the reason `$`/`~` was on the menu.

**`❯` DOES NOT COLLIDE WITH THE BUSY `▶`, AND THE REASON IS STRUCTURAL RATHER THAN LUCKY:** head and
busy are DIFFERENT gutter columns, and `BusyGlyph` renders a shell thread's busy cell BLANK (a shell
has no turn), so the two can only ever appear DIAGONALLY — never side by side on a row, never stacked
in one column. That property is now pinned by `TestShellRowNeverNeighboursBusyGlyph`, so if a shell
ever grows a busy axis the head glyph has to be reconsidered and the test is where that lands.

**THE GUARD GAINED A DOCTRINE IT HAD BEEN DODGING.** `TestGutterGlyphsDistinct` refuses two states
from one confusable FAMILY, but `●`/`◌` are both circles and there was no circle family — the
question had never been put. A chevron pair forces it. The principle now written down: **a live/dead
pair on ONE axis is MEANT to look related** (the shared family is what says "same axis, two states");
H78's failure was two states from DIFFERENT axes looking alike (`⌀` flag-disabled beside `⊘`
archived). So a pair counts as ONE occupant of its family — the not-live half is exempt, the live
half is not, which keeps the family protective: a THIRD glyph reaching into it still trips. Three new
families (`round`, `chevron`, `stacked lines`), one per kind; `hollow quadrilateral` KEPT although
nothing draws from it any more, as a pure drift guard against walking back into the look that
constrained the shell pair in the first place.

TESTS. Units: the guard (families + the two pair exemptions), `TestShellRowNeverNeighboursBusyGlyph`,
`TestVirtualHeadGlyph` re-pinned to `≡`. ANTI-GAMING (reverse-edited, never git-checkout — H44;
`-count=1` — H75): a THIRD chevron in the gutter (flag-disabled `⌁`→`»`) turns the guard RED naming
`[head/shell-live ❯ flag/disabled »]`; virtual walked back to `◇` with a hollow-square shell turns it
RED naming the hollow-quadrilateral pair; reverting virtual to `◇` turns the `action-virtual-enter`
CLAIM red against a real daemon ("virtual row does not render the ≡ glyph"). All three reversed and
re-run green (H88's rule).
GREEN: internal/tui plain and -race; cmd/sesh (the help meta-tests); `go vet ./...`; gofmt clean on
every touched file; conformance claims `action-virtual-enter` and `shells-view`. FULL TUI CLAIMS
SUITE serially (177s): 2 reds, both PRE-EXISTING macbook-environment ones recorded in H80/H88/H91 and
byte-identical here — `action-fork` ("transcript <id>: exit status 1", the pi-transcript class) and
`uuid-popup-copy` ("clipboard tool was never invoked" — the claim stubs `wl-copy`, a WAYLAND tool, on
a Mac). Every non-conformance package green plain and -race EXCEPT the long-standing
`TestMaintainerDropsStaleReportedBusy` "baseline: busy=idle authority=" (H75/H81/H88/H91;
internal/daemon is not in this diff at all). The full 253-cell matrix was NOT run — do not read this
as all-green.
LIVE-SMOKED in a fully isolated sandbox (own SESH_HOME/daemon/tmux sockets under a short `/tmp/sk.XXX`
path, every inherited `SESH_*` stripped, the binary called by ABSOLUTE PATH — myrig defines a `sesh`
shell FUNCTION that pins SESH_HOME to the LIVE `~/.sesh`, H91): five real rows, one per kind, rendered
by the real TUI against a real daemon — the head column reads `≡ ◌ ❯ › ●` down the page. Sandbox
daemon killed by EXPLICIT pid, both scratch tmux servers killed, tree and sockets removed; the live
daemon (pid 1242) and its 2 work sessions verified untouched.
**TRAP RE-CONFIRMED (H93): `tmux -L` takes a socket NAME, not a PATH.** Setting
`SESH_TMUX_SOCKET=/tmp/sk.XXX/wk` made tmux nest it under `/private/tmp/tmux-501/`, so every spawn
failed with "error connecting to /private/tmp/tmux-501//tmp/sk.pSJ/wk". Use a short unique NAME.
**AND AGAIN: `pgrep -af 'sesh daemon run'` SELF-MATCHES** — it reported a second "leaked" daemon
twice, with a different pid each time, that had vanished by the next `ps`. It was the pattern text in
my own shell's cmdline. Confirm any suspected leak with `ps -p <pid>` before believing it.

DEPLOY: **binary-only, NO daemon restart, no schema/API/CLI/key change** (a pure TUI-client render),
so a mixed fleet is trivially safe — a machine still on the old binary just renders the old glyphs.
Docs synced in the same change: the sesh-cli SKILL (glyph list, shell + virtual sections, the
Enter-on-virtual paragraph), `sesh help thread new`'s `--virtual` summary, and `_dev/SHELL.md`.
`_dev/V1_FEATURE_AUDIT.md`'s `◆/◇` is v1 archaeology and was deliberately left. sesh-ui needs
NOTHING — its `format.js` only ever renders `●`/`◌` and has no shell or virtual glyph at all.
**A running SIDEBAR keeps the binary it launched with (H70), so a deployed machine still needs
`prefix+r` (or mmt-kill/mmt-start) before the sidebar shows the new glyphs.**
DEPLOY RESULT (2026-08-28, commit 70f4710): live on **5/6** — macbook, mymain, ideapad, macstudio and
termux, every installed binary reporting `vcs.revision=70f4710` + `vcs.modified=false`, and each one
behaviourally confirmed to carry the new text (`sesh help thread new` says "renders ≡ in the TUI").
macbook/macstudio built with `/opt/homebrew/bin/go`, ideapad natively, termux with PLAIN `go build`
(verified on the box: GOOS=android GOARCH=arm64 CGO_ENABLED=1, H22) — its sshd has come back since
H96. Installed .new+mv everywhere (never overwrite a running binary in place on macOS, H57). NO daemon
was restarted anywhere and none needed to be.
**mymain needed the throwaway-clone route again (H49/H63/H91):** its checkout carries ANOTHER AGENT's
uncommitted WIP — 7 modified files including `internal/tui/mastertrack.go` (untracked) and
`internal/tui/model.go`, i.e. THE FILE THIS CHANGE EDITS — so it was never pulled; built from a
`git clone --depth 1` in /tmp (a shallow clone stamps vcs.revision properly, unlike a linked worktree)
and the clone removed. Verified afterwards that the WIP is still there, 7 files, HEAD still cff7ff4.
**pocket4 OFFLINE** (ssh :22 timed out; the mesh already read it unreachable) → PENDING, harmless —
binary-only and schema-neutral, so it simply renders the old glyphs until it catches up. When it
returns: `cd ~/mysetup/sesh && git pull && go build -o ~/.local/bin/sesh.new ./cmd/sesh && mv -f
~/.local/bin/sesh.new ~/.local/bin/sesh` (no restart).

## H96 — FOUR DERIVED-BOX cockpit commands (clone a GitHub repo / copy a box / worktree a box), mmt- + mt- (2026-08-27, myrig 0e0307d; NO sesh change; render-only deploy, 5/6 — termux sshd down, pending; ticket 120735f9 done)
Lukas ticket 120735f9 "Create mmt and mt commands". MYRIG-ONLY (no sesh change), all four
sharing create-box's machinery: `*-create-box-from-repo` (fzf my GitHub repos),
`*-create-box-from-repo-name` (type `<owner>/<repo>`), `*-create-box-from-box` (copy an
existing box), `*-create-worktree-box`. Conferred three decisions first (AskUserQuestion) —
naming, what a BLANK branch means for a worktree, and whether to widen his copy rule; he took
the `create-box-from-*` family name, "a worktree REQUIRES a branch", and his own owner-only
copy rule verbatim.

DESIGN POINTS WORTH KEEPING. (a) `_mt_new_box` was split: `_mt_new_box_on <machine> <name>
[boxyard-new-args…]` is the raw `boxyard new` core (extra flags forwarded, each `${(q)}`-quoted
for the ssh hop), `_mt_new_box` stays the prompt-and-pick wrapper — so the four new commands,
which collect ALL their input up front and then pass `--git-clone`/`--from`/`-g`/`--parent`, do
not each re-implement box creation. (b) EVERY prompt is answered BEFORE any slow work, so a
clone/copy is never interrupted and cancelling at the last prompt has created nothing.
(c) `_mt_prompt_line` — he asked for backspace AND arrow keys, which `read -r` cannot do
(line-disciplined: an arrow inserts a raw escape sequence INTO the value). **`vared` WORKS IN A
NON-INTERACTIVE `zsh -c` as long as it has a real tty** — which the cockpit's `zsh -lc` popups
do — and MEASURED: ZLE draws the prompt and the edited line to the TTY, never to stdout, so
`x=$(_mt_prompt_line …)` shows the prompt and captures ONLY the value. `[[ -t 0 ]]` guards the
tty-less caller down to `read -r`. The older prompts in shell.sh still use bare `read -r`; the
helper is there if he wants them retrofitted.

**FIVE TRAPS, all measured, all worth re-reading before touching this area:**
1. **`${#arr}` INSIDE A JINJA TEMPLATE IS A COMMENT OPENER.** `"${#"` contains `"{#"`, so jinja
   swallowed the template from there to the next `#}` and the rendered file died with a parse
   error 300 lines later at EOF. shell.sh.jinja ALREADY documents this (the `$#folders` NB) and
   I walked into it anyway. Use `$#arr`; `"$#arr count"` interpolates fine in double quotes.
2. **`boxyard new --from` MOVES the tree with `os.rename`, so it fails across filesystems**
   ("Invalid cross-device link"). Staging in `mktemp -d` (i.e. /tmp) works on mymain and FAILS
   on ideapad, whose /tmp is tmpfs. But **`boxyard copy --dest` REFUSES any destination under
   the user boxes path** ("not allowed to prevent conflicts with managed boxes"), so ~/dev is
   out too. `~/.cache` is the only place satisfying both: outside ~/dev, under $HOME, same
   filesystem. Also: `--from` moves the copy OUT of the staging dir and leaves the parent
   behind ON SUCCESS as well as failure — remove it either way, or ~/.cache accumulates.
3. **`boxyard list-groups` prints its FAILURES to STDOUT** ("Box with index name `…` not
   found."). Piping that straight into `boxyard new -g` fails deep inside pydantic with the
   error text quoted back as the group name. Check the exit status AND validate each line
   against `^[A-Za-z0-9_/-]+$` — a line that is not a legal group name is a REPORT.
4. **`boxyard new` applies `-g` groups AFTER creating the box, OUTSIDE its rollback.** So a bad
   group leaves a real box behind even though the command exits non-zero — that is how trap 3
   left an orphan on ideapad. The fix is to validate before creating anything, which is what
   the code now does.
5. **`boxyard new` does NOT claim ownership**: a box created moments ago has `write_owner =
   null`. On this yard 320 of 592 boxes have no owner (only 5 of those are checked out on
   mymain). It matters because his copy rule keys on the owner, so a fresh box takes the
   remote-store path where it does not exist — the command detects exactly that case and names
   `boxyard claim` rather than leaving boxyard's bare "not found on remote storage".

HIS COPY RULE, implemented literally as he chose: owner known AND box checked out THERE AND
that machine reachable ⇒ copy straight off it (`cp -a` when the owner is also the target, else
a tar stream over ONE ssh hop, target-pulls-from-owner — peer↔peer ssh works fleet-wide and
`ssh-target` allocates no pty, so the stream stays binary-clean); anything else ⇒ the remote
boxyard store. The command prints WHICH of the four it chose and why. NOT widened: a box that
is checked out on the TARGET but owned by someone else still goes to the remote store — his
call, and defensible (a non-owner's copy can hold drift it can never push), but it is the one
place this could be made faster if it ever annoys him.

GROUP INHERITANCE (copy + worktree): the source's groups MINUS `ctx/*` (a machine-context tag —
the new box gets its own from $DEFAULT_BOX_GROUPS, and carrying the source's would label a box
on ideapad `ctx/mymain`) and MINUS $MYRIG_BOXYARD_HIDDEN_GROUPS (a box you just made is not
`archived`; inheriting that would hide it from the very picker you would look for it in).
Worktrees additionally keep mysystem's `ms-new-worktree-box` contract: `-g worktrees` + parented
to the source, which is what boxyard's active-worktrees/archived-worktrees views expect.

GITHUB ACCOUNTS ARE CONFIG, NOT CODE (he asked for this explicitly): `[github].accounts` in
myrig's config.toml → `$MYRIG_GITHUB_ACCOUNTS` via a new `^all^github.sh.jinja`, the
`[boxyard].hidden_groups` → `$MYRIG_BOXYARD_HIDDEN_GROUPS` pattern. Adding an account is the
whole change; the command is LOUD when the variable is unset rather than guessing. The repo
list PRE-FLIGHTS the clone with `git ls-remote` on the TARGET machine — boxyard sends `git
clone`'s output to /dev/null, so without it a no-access repo fails with the reason discarded;
with it you get "ERROR: Repository not found." and nothing is created.

TESTED against the real fleet, everything created then deleted (mymain, ideapad, and the
hetzner store all verified clean afterwards, incl. the propagated meta): real clone (right
remote, right branch, upstream history); pasted-URL normalisation; a nonexistent repo refused
by the pre-flight; all FOUR copy-source branches (owner-direct, owner-offline, no-owner,
owner-hasn't-got-it) driven through the real decision code; local `cp -a` copy; cross-machine
tar copy mymain→ideapad (the strong ctx/ proof — the new box came out `ctx/ideapad` + inherited
`physics`, no `ctx/mymain`, no `archived`); remote-store rclone copy; worktree local and ON
IDEAPAD (real `.git` pointer, registered in the source's `worktree list`, source untouched on
its own branch); blank branch refused; an already-existing branch → git fails → the box is
deleted again; copying a WORKTREE box warns loudly and skips the branch. `_mt_pick_box_id_on
ideapad` really restricted the picker to ideapad's 4 boxes vs mymain's 126. The full
interactive chain was driven in a real tmux: fzf over 248 repos across both accounts, then
vared prompts edited with arrow keys + backspace. Regression-checked the refactor: `create-box`
and `create-null` behave exactly as before. `zsh -n` clean on the rendered file.

DEPLOY: render-only (shell.sh is rendered jinja → install-home per machine; menus.sh is a
symlink → the pull is its deploy; the NEW github.sh.jinja needs install-home to exist at all).
NO daemon restart, NO conf re-source (no binding changed), NO sesh binary change. LIVE ON 5/6 at
myrig 0e0307d — mymain (local), ideapad + pocket4 (python3), macbook + macstudio (`uv run --with
jinja2`, H46), each verified in a FRESH login shell for both `$MYRIG_GITHUB_ACCOUNTS` and the
eight functions, and in the prefix+m quick menus. **termux PENDING — its sshd is down**
(`android-main:8022` connection refused; nothing restarts it but a Termux session or a reboot,
H83). When it returns: `cd ~/mysetup/myrig && git pull && python3 scripts/install-home.py
"$MYRIG_TARGETS"` (its python3 has jinja2; /tmp is unwritable there, so don't redirect the log
to /tmp — H38).
NOTED, not mine: pushing my commit also pushed Lukas's already-committed but unpushed
`2eaa590` ("boxyard: turn merge_diverged_boxmetas on, mymain first") — unavoidable, mine sat on
top of it. Harmless: that setting is `{% if current == 'mymain' %}`-guarded in the template, so
rendering it on the four peers produced no change, VERIFIED (`merge_diverged_boxmetas` absent
from every peer's config.toml, present on mymain). ideapad's myrig checkout still carries the
stray uncommitted `home/.zshrc` append from H94; it does not touch any file in this change.

### H96 follow-up — boxyard 0.5.17/0.5.18 landed under the feature: three of H96's five traps are GONE, trap 5 is INVERTED, and a REAL bug in my own worktree path surfaced (2026-08-27, myrig 050fa5a; still no sesh change; render-only, DEPLOYED ALL FIVE)
Lukas fixed all three boxyard issues I filed (#16 cross-device `--from` → `shutil.move`;
#17 orphan box → validate `-g`/`--parent` BEFORE creating; #18 errors on stdout → 28 sites
gained `err=True`, incl. 2 in multi-sync I had not spotted), shipped them as **v0.5.18**, and
separately made `boxyard new` CLAIM the box for the creating machine (**v0.5.17**). Whole fleet
is now on 0.5.18 with `machine_name` set — macbook came back online and got both the myrig
render and the pending `uv tool install --force git+…` (it was on 0.5.13).

**CORRECTION TO H96, and it is the load-bearing one: trap 5 is now INVERTED.** `boxyard new`
DOES claim. A freshly created box is owned by its creator, so the create-box-from-box no-owner
branch is no longer the common case — it is left for LEGACY unowned boxes (320 of 592 on this
yard predate the change) and for a machine still on an older boxyard. LIVE-PROVEN: copying a
just-created box now reports "direct from its owner, mymain" and succeeds, where before it fell
to the remote store and FAILED there ("not found on remote storage") because a fresh box has
never been pushed. So the feature got strictly better without a code change. Traps 3 and 4 are
fixed upstream; **trap 3's guard STAYS in the code anyway** — the fleet is not upgraded
uniformly (macbook sat on 0.5.13 for a day), and an older boxyard still prints failures to
stdout.
ALSO CORRECTED, and I got this wrong in my summary to Lukas before he pushed back: **`boxyard
copy --dest` refusing `~/dev` is NOT a bug.** It is an explicit `# Safety check` in
`12_copy_from_remote.pct.py` guarding BOTH `boxyard_data_path` and `user_boxes_path`, so you
cannot leave something that looks like a box in ~/dev but is not registered. My code complies
with it; nothing was filed. A fourth thing I had worked around — `--git-clone` discarding git's
stderr — turned out to be ALREADY FIXED in 0.5.16 (`stderr=subprocess.PIPE`), so no issue there
either. The `git ls-remote` pre-flight stays because it fails BEFORE any box exists, which is
still better than failing after.

**READ THE INSTALLED VERSION, NOT THE CHECKOUT.** I first read `~/mysetup/boxyard` and reasoned
from it: it sits on branch `feat/write-owner-claim` at **v0.5.2**, 35 commits behind
origin/main, while the installed uv tool was **0.5.16**. Per boxyard's own AGENTS.local.md the
global CLI is a uv tool at `~/.local/share/uv/tools/boxyard`, NOT editable from the repo — so
the checkout can be arbitrarily stale. Re-verify with `git show origin/main:<path>` (read-only —
that checkout is someone's WIP branch, do not switch it), and cross-check `boxyard --version`.
All three issues did survive into 0.5.16, so nothing was mis-filed, but that was luck.

**THE REAL BUG THIS PASS FOUND, in MY code, and it was reachable in ordinary use:**
`_mt_inherited_box_groups` read `boxyard list-groups` from THIS machine's catalog. But
`mmt-create-worktree-box`'s picker is `_mt_pick_box_id_on <target>`, restricted to the TARGET's
~/dev — and `boxyard-groups.py` DELIBERATELY offers a box that is on disk there but absent from
this machine's catalog (its own comment: "a box checked out on disk but absent from the catalog
still shows"). So picking one aborted with `box groups: boxyard could not list "<index>"'s
groups: Box with index name ... not found.` Trigger: create a box on a peer — with
`mmt-create-box-from-box`, say — then worktree it before its boxmeta has propagated (the meta
push is DETACHED and takes ~10-15s). I first saw this in a test and wrote it off as a harness
artifact because I had stubbed the propagation; it is not — reading boxyard-groups.py settled
it. **A failure you can only produce with a stub is worth one more look, not a shrug.**
FIX: the helper takes a `<machine>` and reads there. Worktree passes the TARGET (the box is
checked out there by construction, and it is the catalog `boxyard new` resolves `--parent`
against anyway); copy keeps reading HERE, deliberately, because its picker is the plain
fleet-wide one so the box is in this catalog by construction and may not exist on the target at
all. Verified against a real box present on ideapad and absent from mymain's catalog: red
before, green after.

CLAIM-ON-CREATE, checked rather than assumed: every one of the four commands creates the box ON
the machine that will hold and work on it, so the creator IS the right owner and `--no-claim` is
needed nowhere. Proven both ways — a box created on ideapad by the cross-machine copy comes out
`write_owner=ideapad`, not mymain. The worktree failure path's `boxyard delete` also runs ON the
owning machine (it routes through `ssh-target <m>`), so it needs no `claim --steal`; proven by
forcing a `git worktree add` failure on ideapad and watching the ideapad-owned box be removed.

**TRAP WORTH KEEPING — a python process that exits 120 with NO output is a WRITE failure, not a
crash.** `install-home … > /tmp/ih.log 2>&1` on ideapad returned rc=120 with a 0-byte log; run
without redirection it returned **0** and did all its work. Cause: ideapad's `/tmp` is a tmpfs
mounted **`usrquota`** and lukastk's quota is exhausted — `echo hello > /tmp/x` fails with
`write error: disk quota exceeded`. CPython exits 120 when it cannot flush stdout at exit, which
looks exactly like a failed deploy while the deploy actually succeeded. **Do not read rc=120 +
empty log as "it failed"; re-run without the redirect, and test `/tmp` writability.** `df` is
MISLEADING here — it showed 1.6G Avail, because that is the filesystem's free space, not the
USER's remaining quota (inodes were only 7% used). The culprit was 6.1G / 21,640 files of
`/tmp/pytest-of-lukastk` (14 runs, 20:08–22:15 the same day, no pytest still running) — left by
a test suite. Reported to Lukas; he approved clearing it and it was then **NOT deleted, on
purpose**: by the time the go-ahead came the directory had ROTATED ITSELF down to 36M (pytest
keeps only its last few numbered basetemps), `/tmp` was back to 4% and writable, and a LIVE
self-hosted GitHub Actions runner (`gh-runner/instances/lukastk-mosaic-a1`) was mid-run writing
into it — so the delete would have fixed nothing and broken a running CI job. **RE-CHECK THE
PRECONDITION BEFORE EXECUTING AN APPROVAL: an approval is for the situation you described, and
this one had reversed within the hour.** The durable point is that the peak was transient and
self-limiting; the standing hazard is that ideapad's `/tmp` is a per-user-quota tmpfs and now
also hosts CI, so a heavy run can exhaust it again — with the rc=120 symptom above.
NB my own staging deliberately uses `~/.cache`, not `/tmp` (H96 trap 2), so the derived-box
commands kept working on ideapad throughout — the right choice for a second, unforeseen reason.

DEPLOY: render-only again, NO daemon restart, NO conf re-source. **LIVE ON ALL FIVE** at myrig
050fa5a — mymain (local), ideapad + pocket4 (python3), macstudio + macbook (`uv run --with
jinja2`), each verified in a FRESH login shell. boxyard 0.5.18 on all five. termux is still the
one machine with neither (its sshd is down — H83).

## H95 — "SUBSCRIPTIONS DON'T WORK" was H92's guard doing its job + a supervisor hiding stderr; the REAL defect was the refusal naming a flag that command does not have (2026-08-27, sesh a28acf8; NO schema/API/daemon change; BINARY-ONLY, no daemon restart; DEPLOYED 5/6 — macbook asleep, pending)
Lukas: thread f9ba7068 (jackfruit-hq supervisor) "recently tried to subscribe to a few child
threads but it seems like it didn't get a message back when the subscribees ended their turns."

**SUBSCRIPTIONS WERE NEVER BROKEN. The edges never existed.** The diagnosis order is the reusable
part, and it is three commands long:
1. `sesh subscriptions --json` → the three child edges DID exist by the time I looked.
2. `sqlite3 -readonly ~/.sesh/sesh.db "select subscribee,last_count from subscriptions where
   subscriber like 'f9ba7068%'"` → the NINE older edges carried counts of 5..118 (so the engine
   works) while the three new ones read **0** (so nothing had ever been delivered on them).
3. `grep '\[sesh\].*completed a turn'` in the SUBSCRIBER'S OWN TRANSCRIPT, with timestamps →
   deliveries flowing 10:41→14:56 for the previous batch, nothing for the new one. That is the
   observable that settles it: the delivery text is `[sesh] <name> (<uuid>) completed a turn`, it
   lands IN the subscriber's conversation, and it is timestamped.
Then the parent's transcript again, grepped for the COMMANDS it ran: at **15:15:17** it ran, in a
loop, `sesh subscribe $ID >/dev/null 2>&1` followed by `echo "$lane -> $ID (…, subscribed)"`. Every
call was REFUSED — and the loop discarded stderr and printed success anyway. It re-ran the same
command visibly at 16:32, and the refusal was right there in the tool result.

**THE REFUSAL WAS CORRECT AND H92 EARNED ITS KEEP HERE.** `$SESH_THREAD_ID=ce3e3811` named a thread
whose cwd is `~/mysetup/mysystem`, unrelated to the caller's `~/dev/…jackfruit-hq-mymain/jackfruit`,
with no tmux pane to check it against — the H82 signature of a claude Bash call hosted by the
machine-global `claude daemon run`, which freezes whichever thread's env started it. Without the
guard those three children would have been subscribed to a STRANGER'S thread in another repo, and
their turn reports would have been delivered into it. This is the second time that guard has caught
a real mis-identification in the wild.

**THE ONE REAL DEFECT, and it is why the incident cost an hour instead of a minute: the refusal's
remedy named a flag the command does not have.** It hardcoded `Pass --id <thread>`, and
`sesh subscribe` has NO `--id` — its subscriber flag is `--from`. MEASURED: `sesh subscribe <id>
--id X` → `flag provided but not defined: -id`. So an agent that follows the loud, actionable error
verbatim gets a parse failure. Same mismatch in `ticket list --current` and `hooks test`, which take
`--thread`. A refusal that offers a remedy the caller cannot type is only half a loud error.
FIX: the flag travels WITH the refusal — `unverifiedError.Flag` (empty = `--id`) fed from
`currentInputs.idFlag`, so H92's pure truth table is unchanged and still unit-testable, and the
sibling "not inside a sesh thread" error (same hardcoded remedy, same bug) uses it too.
`resolveThreadIDFor` / `resolveMeshThreadIDFor` are the flag-aware variants; `resolveThreadID` keeps
its signature so the ~20 ordinary `--id` call sites are untouched, and only the three commands whose
flag differs pass their own.

TESTS. Unit (cmd/sesh): defaults to `--id`; names `--from`/`--thread` when the command does; must
NOT mention `--id` in those cases; the not-in-a-thread refusal too. **The WIRING is covered where it
belongs** — `thread.info/-/local`, the cell that already reproduces the H92 incident against a real
daemon, now also runs a real `sesh subscribe` from the contradicting directory and requires the
refusal to name `--from` and not `--id`. ANTI-GAMING (reverse-edited, never git-checkout — H44;
`-count=1` — H75): passing `""` from subscribe's call site turns the CELL red with the verbatim old
message, while the unit test stays green — which is the honest split, the unit covers the resolver
and the cell covers the wiring.
GREEN: cmd/sesh plain and -race; `go vet ./...`; cells thread.info 2/2, ticket.list-current 1/1,
daemon.hooks 2/2, thread.subscribe 2/2.
**`-run` TRAP AGAIN (H92 recorded it; I walked into the other half): the matrix subtest path carries
the AGENT AXIS even when the feature is agent-agnostic** — it is `TestMatrix/thread.info/-/local`,
so `-run 'TestMatrix/thread.info/local'` matched NOTHING and printed `ok` in 0.00s. Confirm with
`-v` that the cells you expected actually appear.

LIVE-PROVEN, read-only, on the real fleet: after the supervisor re-subscribed at 16:33 WITH
`--from`, deliveries resumed within four minutes and all three edges now carry counts (31 / 7 / 26)
with 30 `completed a turn` blocks in the parent's conversation. Also confirmed along the way that
`busy` on those children was HONEST and not an H58/H64 stuck authority: `thread capture` tail alone
LOOKED idle (claude always renders its `❯` input box), and only diffing two captures 3s apart showed
`✽ Billowing… (3m 12s · still thinking with xhigh effort)` — **do not read a claude pane's prompt
line as idle; diff the pane.**

SKILL: the provenance section now names the per-command flags AND carries an agents' note — a claude
Bash call often has no pane, so scripted work should name the thread explicitly
(`sesh subscribe <child> --from <me>`) and must never discard stderr; `>/dev/null 2>&1` around a
loop of sesh calls is exactly how this failed silently for an hour.
NOT CHANGED, deliberately: nothing about inference itself. Corroboration refusing here is the
designed behaviour, and "infer from the cwd instead" would be guessing (several threads share a cwd).

DEPLOY: **binary-only, NO daemon restart, no schema/API/wire change** (CLI-side only, so a mixed
fleet is trivially safe). LIVE ON 5/6 at a28acf8 — mymain, ideapad, macstudio, pocket4, termux
(plain `go build`, CGO_ENABLED=1 / android, H22); every installed binary `vcs.modified=false`.
**macbook ASLEEP** (ssh :22 timed out; it had been reachable earlier today and dropped off the mesh
mid-session) → PENDING, harmless; when it wakes: `cd ~/mysetup/sesh && git pull &&
/opt/homebrew/bin/go build -o ~/.local/bin/sesh.new ./cmd/sesh && mv -f ~/.local/bin/sesh.new
~/.local/bin/sesh`.

## H94 — the `S` SHELLS VIEW had NO VIEWPORT (the cursor walked off the pane), + `mt-promote-session-here` could not be driven from the prefix+m popup (2026-08-27, sesh d1729cb + myrig 75694cf; NO schema/API/CLI-flag change; BINARY-ONLY, no daemon restart; DEPLOYED ALL SIX)
Lukas, three asks: (1) an mmt- twin of `mt-promote-session-here` + both in the prefix+m quick
menus; (2) "I just tried mt-promote-session-here and it didn't work"; (3) "I tried the shells view
in sesh tui, and it seems a bit broken. It should work similarly or pretty much the same as the
threads view (and should reuse the code). Right now it doesn't seem to scroll towards the currently
selected row."

(1) WITHDRAWN by him mid-session once the semantics were laid out: promoting "the session you are
sitting in" only ever means the machine you are on, so a cross-machine twin has nothing to name.
Only `mt-promote-session-here` exists, and it is now in MT_QUICK_CMDS.

**(2) IS THE INTERESTING ONE AND ITS DIAGNOSIS IS THE REUSABLE PART.** He said the shell thread
never appeared in his sidebar — which reads as a rendering/visibility bug. It was not: `thread grid
--all-machines --archived --json | select(.agent_kind=="shell")` across the whole fleet returned
NOTHING, so no record had ever been created and the promote had simply ERRORED. **Query the store
before believing a "it didn't show up" report** — presence in the record set splits "the write
failed" from "the view hid it" in one command, and they have completely different fixes. (Two
candidate errors, both reproduced: a shell older than 2026-08-23 22:42 has no such function — many
of his live work-server sessions predate it, `adobe-suite`/`boxyard-go`/`mosaic`/`politick-hq` —
and a pane on a non-work tmux server gets the daemon's loud 404.) Also ruled out along the way, by
running the OLD binary against the live daemon: his sidebar process is **c550644 from 2026-08-18**,
five feature batches stale (H70: a long-running sidebar keeps the binary it launched with), but an
old TUI renders a shell thread FINE — so staleness was not the cause either. Do not stop at "the
client is old".

THE FIX THAT MATTERED FOR (1)+(2) IS **THE WHICH-CLIENT LAW, MEASURED**: inside a tmux
`display-popup`, `$TMUX_PANE` is NOT the pane you pressed the key on — it is inherited from the tmux
SERVER's environment. In an isolated rig a popup reported `TMUX_PANE=%1226`, the pane of the shell
that had started the server; on a work server started at boot it is unset entirely. So `sesh shell
here`, which reads `$TMUX_PANE` itself, either fails outright from the menu or — proven read-only —
resolves a STALE pane id and would promote a COMPLETELY DIFFERENT session (it resolved
`mysetup/sesh`, my own agent session). `mt-promote-session-here` therefore now takes the pane from
`${SESH_MT_PANE:-$TMUX_PANE}` (the prefix+m binding bakes `SESH_MT_PANE=#{pane_id}`), reads its
session name, and calls **`sesh shell promote --session <name>`** — the by-name verb, which is
drivable from a popup — exactly as `mt-enter-new-thread-here` already did. It also emits a `tmux
display-message` on success: from the popup the popup closes the instant the function returns, so
the printed line is never seen (the binding's `|| sleep 3` only holds on FAILURE).

**(3) THE SHELLS VIEW HAD NO VIEWPORT AT ALL** — `shellsView` looped over every row unconditionally:
no offset, no indicators, no ensure-visible. With 78 live sessions on the real mesh the cursor
walked off the bottom and the selected row was simply not on screen (bubbletea keeps the LAST
`height` lines of an over-tall frame, so the title and the top rows are what get dropped — H70's
mechanism, third appearance).
**The "reuse the code" he asked for was real and worth doing: FOUR surfaces each carried their own
transcription of the same six lines of list-window arithmetic** (grid `scroll.go`, `?` `helpView`,
command palette, reparent picker) **and the shells viewer carried NONE.** Extracted to
`internal/tui/listwindow.go` — `listVisibleRows` / `listEnsureVisible` / `listClampOffset`, pure —
and rewired all four onto it (behaviour unchanged). The viewer then got: viewport-follows-cursor,
▲/▼ indicators, ^j/^k half-page, a `/` fuzzy filter (name + machine + path + the agent threads
inside; enter applies, esc clears, esc only closes the viewer once there is no query left), mouse
(wheel moves, click selects, double-click enters), and a WRAPPING cursor — it used to clamp, and a
list that stops dead next to one that wraps reads as broken (the test that asserted clamping now
asserts wrapping, deliberately).
TWO DESIGN POINTS WORTH KEEPING. (a) **Geometry is resolved ONCE** (`shellResolveLayout`) and read
by BOTH the renderer and the click mapping, instead of the renderer being hand-mirrored by a chrome
count — that mirror is the H41 drift class, and its symptom is clicks landing on the wrong row.
(b) **The cursor is ANCHORED to its session across a re-fan-out** (H42's rule): promote/kill reload
the list, and a positional cursor would slide onto a DIFFERENT session — the one thing a viewer
with a `kill` key must never do. The kill confirmation also moved ABOVE the rows (where the grid
puts its own): an armed y/n prompt must be unmissable, and anything below a full list is the first
thing a short pane cuts. Long errors/warnings are wrapped to the width and COUNTED, and the frame
is clamped to the pane height keeping the TOP.
NOT FIXED, and recorded rather than quietly skipped: the `I` details popup still renders a fixed
~23-line field list with no scrolling (H88 noted it; still excluded from the popup-frame guard).

TESTS. Units: listwindow truth tables; viewport-follows-cursor + half-page asserted on the RENDER,
not the offset; `shellRowAtY` round-tripped against the render at five chrome/scroll/width
combinations; the filter; anchoring incl. the killed-session case; the mouse. The shells view is
added to `TestPopupFramesFitPaneHeight` with and without its optional chrome. Conformance claim
`shells-view` EXTENDED against a REAL daemon and 24 REAL extra tmux sessions: after 20 real `j`
presses the selected session must be the row rendered under the cursor and the frame must fit a
14-row pane, then `/` narrows the real list. ANTI-GAMING (reverse-edited, never git-checkout — H44;
`-count=1` — H75): neutering ensureShellCursorVisible turns the unit test AND the claim red with
the exact user report; dropping the reload anchor slides the cursor onto another session; dropping
the offset from `shellRowAtY`, and forgetting the ▲ line occupies a row, each turn the drift guard
red. All reversed and re-run green (H88's rule).
GREEN: internal/tui plain and -race; every non-conformance package plain and -race; `go vet ./...`;
the FULL TUI claims suite serially (183s, ALL GREEN — no pre-existing reds on this box, unlike the
macbook runs in H80/H88/H91). `gofmt -l` still flags the usual pre-existing drift files (H48); every
file I touched is clean. The full 253-cell matrix was NOT run — do not read this as all-green.
LIVE-SMOKED READ-ONLY against the real mymain daemon and its 78 real sessions (isolated tmux, no
P/x/enter — a double-click would have ATTACHED this terminal to a live work-server session, the H69
resize hazard): 40 × j scrolled to "▲ 23 more" with the cursor row on screen, `/boxyard` narrowed
to 4/78, and a real SGR mouse click landed on exactly the row clicked. The myrig side was smoked in
a popup-shaped context (TMUX_PANE unset, SESH_MT_PANE=<scratch pane>): the new form promotes the
right session where the old form fails; scratch session + record cleaned up.
DEPLOY: **binary-only, NO daemon restart, no schema/API/wire change** (a pure TUI-client feature) +
a render-only myrig change. **LIVE ON ALL SIX** at sesh d1729cb+ / myrig 75694cf — mymain, ideapad,
macbook, macstudio, termux (plain `go build`, CGO_ENABLED=1 / android verified on the box, H22;
render logs to `$HOME`, /tmp is unwritable there — H38) and pocket4; every checkout was verified
clean on main BEFORE pulling (H49/H63), every installed binary reports `vcs.modified=false`, and
`sesh help tui` carries the new text on each. pocket4 was OFFLINE for the first pass (ssh :22 timed
out) and came back ~an hour later; it was deployed the same way (native `go build`, python3 render)
at 341f240 — a doc-only commit ahead of the others, which is harmless and simply what its checkout
had by then. No daemon was restarted anywhere and none needed to be: pocket4's supervised daemon
still runs its old inode, which is correct for a TUI-client change.
**A running SIDEBAR keeps the binary it launched with (H70), so none of this exists inside his
sidebar until `prefix+r` (or mmt-kill/mmt-start)** — and his was pid 69163 from 2026-08-18, i.e.
already five batches behind before this change.
TRAP HIT: `ssh-target macstudio` failed twice with `mux_client_request_session: session request
failed: Session open refused by peer` while the mesh said macstudio was REACHABLE (its http API was
fine). It was a STALE ControlMaster socket, not the host: `ssh -O exit -o ControlPath=~/.ssh/cm/%C
<userhost>` cleared it and the next connection worked. Do not read that error as "the machine is
down" — check the mesh first, then drop the mux socket.
NOTED, not touched: ideapad's myrig checkout carries an uncommitted 2-line `home/.zshrc` append
(a stray `. "$HOME/.local/share/../bin/env"` from some installer). It does not conflict with this
change; it is not mine to commit.

## H93 — CODEX SUBSCRIPTIONS HAD SILENTLY STOPPED DELIVERING: codex dropped `event_msg`/`agent_message` from its rollouts between 0.146 and 0.149.1, so `LastReply` returned ("",0) for every codex thread; fix = read the `response_item` conversation record — and match ONE shape, because legacy rollouts carry BOTH (2026-08-25, sesh e7b3a0d; NO schema/API change; DAEMON rebuild + RESTART; DEPLOYED ALL SIX; **FULL MATRIX 253/253 ALL GREEN**)
Found by running the full matrix after H92 (Lukas: "work on the 3 failures"). `thread.transcript/
codex` was red on `last_reply=""` / `reply_count=0` while the transcript LINES clearly contained the
sentinel — i.e. the file was found and read, only the reply EXTRACTION failed.

ROOT CAUSE: `codexfs.LastReply` keyed on `{type:"event_msg", payload:{type:"agent_message",
message:…}}`. **codex stopped emitting that line entirely** somewhere between 0.146 (H61/H62's
version) and 0.149.1 (this box). Measured, not guessed: a rollout captured from a real 0.149.1 turn
has ZERO `agent_message` lines; the reply appears as `{type:"response_item", payload:{type:
"message", role:"assistant", content:[{type:"output_text", text:…}]}}` (plus two other views of the
same thing — see the double-count trap below).

**THIS WAS A LIVE DEFECT, NOT A RED CELL.** `LastReply` feeds `thread transcript`'s last_reply AND
the SUBSCRIBE ENGINE's dedup marker. In `deliverCompletion` an empty reply means 12 retries at
250ms and then a bare `return` — so **every subscription from a codex thread had been silently
delivering nothing**, after stalling 3s per turn. Nothing surfaced it because the failure is a
silent no-op on the delivery path.

THE FIX READS THE CONVERSATION RECORD (`response_item`/`message`/`role:"assistant"`, concatenating
`content[].text`) — the same shape claude and pi are read through. **TWO CODEX FORMATS EXIST AND
ONLY ONE DRIFTED:** the `codex exec --json` STDOUT stream still emits `item.completed` /
`agent_message`, and `parseCodexExec` (agents/headless.go) reads THAT one — which is exactly why
headless codex turns stayed green throughout and why the bug looked narrower than it was. Don't
conflate the rollout format with the exec-stream format.

**THE TRAP THAT DECIDES THE WHOLE DESIGN — do not "add legacy support for compatibility".** The
obvious instinct is to match the old `agent_message` line as well, for the rollouts already on
disk. MEASURED across ALL 513 live rollouts (2026-07-06..2026-08-25): 232 files carry the legacy
line, and every one of them ALSO carries the `response_item` message **for the same reply, in equal
numbers, with identical text**. Matching both DOUBLES the count on 232 real files — corrupting the
very dedup marker the compatibility was meant to protect. (A third view, `event_msg`/
`item_completed` with `item.type:"AgentMessage"`, is a third duplicate of the same reply.) The
single `response_item` form covers 512/513 files; the one file it misses contains no agent reply at
all (an aborted turn), where 0 is the correct answer. ONE SHAPE. The anti-gaming test pins it.

`Payload` gained `Role` and a TYPED `Content` slice. Content is a JSON list or null in all 513
rollouts (139k occurrences), which matters more than usual because **`OffsetReader.ReadNew` BREAKS
on the first parse error and silently truncates the rest of the file** — a struct that fails to
unmarshal on some line type would be worse than the bug it fixed. A regression test therefore puts
unknown line types (`world_state`, `turn_context`, `token_count` — all new in 0.149.1) BEFORE the
reply and requires it still be found.

TESTS use real line shapes copied from live rollouts, not invented ones: current-format extraction,
legacy-era no-double-count, non-replies ignored (developer/user/`reasoning`/`function_call` and
text-less assistant turns), multi-part content concatenation, aborted-session zero, and the
unknown-line-types guard. ANTI-GAMING (reverse-edited with byte-exact restore verified by `git
diff`, never git-checkout — H44; `-count=1` — H75): reverting to the `agent_message` key gives
count=0 (the shipped bug); ALSO matching the legacy line gives 4 instead of 2 (the double-count
trap). Real-agent cells green: thread.transcript **6/6** (was 4/6), thread.subscribe 2/2,
thread.await 6/6, thread.codex-session-capture 1/1.

**COVERAGE LESSON WORTH KEEPING: `thread.subscribe` is registered `AgentAgnostic`, so the codex
reply-extraction path is never exercised there — it was `thread.transcript`'s PER-AGENT axes that
caught a defect in a different, agent-agnostic feature.** The per-agent axis earned its keep on a
row where it looked redundant. Consider this before declaring a future row agent-agnostic: the
delivery MECHANISM is agent-independent, but the reply EXTRACTION it depends on is not.

THE THIRD RED (`thread.codex-session-capture/codex/local`) — **I COULD NOT REPRODUCE IT, and say so
rather than claim a fix.** It passes serially on this HEAD and on clean a10ff42, and it survived a
deliberate 48-way CPU-contention run (16 cores × 3 busy loops) unchanged at ~51s. The signature is
what the change is reasoned from: it got PAST `headedTurn`'s 150s settle and then failed
`waitStamped`'s 30s. codex is the one agent with NO authoritative turn state (justified N/A on
thread.state-authority), so "settled" is the CONTENT-DIFF HEURISTIC, and a pane stalled on a slow
provider call stops animating and reads as IDLE (H58's frozen-pane class; a full-suite run has many
real agents competing for the same provider). When that fires early, `waitStamped` is still in
truth waiting for the turn — with a third of the time a turn is allowed. Its bound now MATCHES
headedTurn's, the assertion is unchanged, and the failure message distinguishes "still busy" (the
heuristic fired early) from "idle" (the notify chain is broken) because those are different bugs.

**FULL MATRIX: 253 cells — 253 pass, 0 fail, 0 skip, 0 missing, 0 not-run, 2 justified n/a. ALL
GREEN** (46min). NB when the suite PASSES, `go test` streams nothing and the log is two lines — the
grid is persisted, so read it with `go run ./cmd/sesh matrix grid`, not from the test output.

TRAPS HIT THIS SESSION, all variants of documented ones, all worth re-reading:
- **`-run 'TestMatrix/…'` EXCLUDES EVERY NON-MATRIX TEST IN THE PACKAGE.** My blast-radius pass
  after H92 missed two failing tests entirely for this reason. Filtering by subtest path is not
  running the package.
- **A `/` INSIDE a `-run` alternation group is read as a SUBTEST-LEVEL SEPARATOR**, so
  `-run 'TestMatrix/(thread.transcript/codex|thread.codex-session-capture)'` silently ran only ONE
  cell and printed `ok`. I nearly concluded from it that the transcript cells passed on clean HEAD
  when they had never executed. One `-run` per cell, and confirm with `-v` that the cells you
  expected actually appear.
- **`pgrep -f` SELF-MATCH, AND THE BRACKET TRICK DOES NOT ALWAYS SAVE YOU (new variant).** On
  termux `pgrep -f "sesh daemon run"` matched my own ssh shell; switching to `grep "[s]esh daemon
  run"` ALSO self-matched, because the pattern text is itself in my command's cmdline. It made a
  1h-old daemon look like it was restarting every 3s, and made a script wrongly conclude the
  zshenv guard had relaunched the daemon. **Use the daemon's OWN self-reported pid
  (`sesh daemon status`) and confirm via `/proc/<pid>/exe`** — which is also how the real problem
  showed up: the exe read `…/sesh (deleted)`, i.e. still the old inode after `mv`, proving the
  restart was genuinely needed.

DEPLOY: **NO schema/API/wire change, so a mixed fleet is safe — but `LastReply` runs in the DAEMON
(subscriptions.go + threadops.go), so this is a rebuild AND RESTART, unlike H92's binary-only.**
LIVE ON ALL SIX at e7b3a0d, `vcs.modified=false` everywhere; every remote checkout verified clean
before pulling (H49/H63). mymain/ideapad/pocket4 native + `supervisorctl restart sesh-daemon`,
macbook/macstudio `/opt/homebrew/bin/go` + supervisor, termux PLAIN `go build` (CGO_ENABLED=1 /
android, H22) with the old daemon killed by EXPLICIT pid and the zshenv login-guard relaunching it
(H36/H89) — verified afterwards that its exe is no longer `(deleted)` and it carries the right four
SESH_* vars (no SESH_API_ADDR: inbound-less leaf, the H75 warning is EXPECTED there).
LIVE-PROVEN on the real fleet, read-only, after deploy: four real codex threads that would every
one have read `reply_count=0 last_reply=""` now report 22 / 163 / 1155 replies with real text on
mymain, and 22 on macstudio ROUTED over the mesh.


## H92 — `sesh info` CONFIDENTLY NAMED ANOTHER THREAD when the caller had no pane, and a self-compact hijacked it: fix = inference reports PROVENANCE + refuses an env id contradicted by the caller's cwd (2026-08-25, sesh 0c69e41+6271fb8+f462537; NO schema/API/daemon change; BINARY-ONLY, no daemon restart; tickets d7be88ef + 6ea1f6eb done; DEPLOYED ALL SIX)
Lukas's ticket d7be88ef: "`sesh info` silently resolves to ANOTHER thread's id when the calling
shell has no tmux pane, and a self-compact routine acted on that answer — compacting an
unrelated agent's session and injecting a foreign handover prompt into it." An agent in
1777a4ac (boxyard-go, cwd `~/dev/…__boxyard-go`) asked `sesh info` who it was, was told
093da760 ("mysetup - sesh", cwd `~/mysetup/sesh`), and its runner compacted THAT thread and fed
it someone else's handover. Lukas intercepted before the victim acted; the compaction is not
reversible.

ROOT CAUSE IS H82's, AT A THIRD CALL SITE. `resolveThreadID` falls back to `$SESH_THREAD_ID`
when there is no pane — correct for a headless turn, fatal for a claude BACKGROUND job, which is
hosted by a machine-global `claude daemon run` that froze whichever pane's env started it. So the
env names a **valid** thread that is simply not this one, and from sesh's side it looks perfect.
That is also exactly what ticket 6ea1f6eb's 2026-07-10 investigation found for `thread new`'s
silent mis-parent — it concluded "sesh can't detect this" and parked. It can: **the caller's cwd
is evidence**, the same lever H82 used for the session stamp.

CONFERRED FOUR DECISIONS before building (AskUserQuestion); Lukas took all four recommendations.
(1) Warn ALWAYS on an env-derived answer AND refuse on a cwd clash (`--allow-unverified`
overrides). (2) Mutating verbs get NO separate rule — they inherit (1), so a headless turn whose
cwd matches keeps working while a contradicted id can mutate nothing. (3) SKIP the floated
`thread send` slash-command guard: supervisor→worker sends are a core workflow, and the
self-compact runner fires from the **tmux server** with no thread identity at all, so a
"caller must equal target" rule would break the very skill it was meant to protect — and (1)
blocks it at the source anyway, before a wrong id is ever computed. (4) Fold in 6ea1f6eb.

MECHANISM (all CLI-side — cmd/sesh only). The truth table moved into a PURE, injectable
`resolveCurrentThreadFrom(client, currentInputs{env,paneID,cwd,allowUnverified}) → (id,
idSource, notes, err)`; `resolveThreadID` keeps its signature so all ~20 inferring call sites are
untouched, `resolveCurrentThread` exposes the source. `srcPane`/`srcExplicit` are VERIFIED — a
process elsewhere cannot inherit them; `srcEnv` is not, is announced on stderr, and is
corroborated against `os.Getwd()`. Contradiction ⇒ a typed `*unverifiedError` naming both paths,
`--id` and `--allow-unverified`. `sesh info` renders a `source:` line and `"source"`/`"verified"`
in `--json`, so a destructive caller can require `pane`. `--allow-unverified` is a PSEUDO-GLOBAL
stripped in main like `--machine`, so every inferring verb accepts it without declaring it (only
`info`'s usage line lists it, which keeps the help meta-test honest).

**H82's ONE-DIRECTIONAL EVIDENCE RULE IS THE LOAD-BEARING CONSTRAINT AGAIN:** only a POSITIVE
contradiction may refuse. Missing cwd (a virtual thread has none), a relative/unreadable path,
and containment in EITHER direction all still resolve. Paths are ~-expanded, absolutised and
`EvalSymlinks`'d first — a `~/dev` box reached through a symlink would otherwise read as an
unrelated tree — and containment tests on a SEPARATOR boundary, so `root-sibling` is not "inside"
`root`. A false positive costs one loud, actionable error; a false negative costs someone else's
session.

6ea1f6eb, same commit: `thread new` now ANNOUNCES the parent it inferred AND its provenance
(silent success is how the mis-parent hid for ten hours), and on a contradicted id refuses to
infer, says so, and makes a ROOT — distinguished from the ordinary "not inside a sesh thread" by
`errors.As(*unverifiedError)`, which is why the sentinel type exists. Fixed the two stale doc
sites that still described the OLD env-first precedence and misled that ticket's diagnosis:
`internal/matrix/features.go` (thread.info) and the sesh-cli SKILL's ⚠️ PARENT INFERENCE box.
SKILL also gains an "Am I really this thread?" section with the copy-pasteable `select(.source ==
"pane")` guard.

**THE FIX ONLY HALF-WORKS WITHOUT THE SKILL, so myagent 1869f39 too**: `self-compact` step 1 said
`sesh info` is "more reliable than $SESH_THREAD_ID" — true ONLY in a pane; with no pane it IS
that variable, so the skill inherited the drift it claimed to avoid. Step 1 now filters
`select(.source == "pane")` and aborts when empty, with the reasoning inline so nobody drops the
filter: corroboration is evidence, not proof — an inherited id naming a thread in the SAME tree
still resolves as `env`, so requiring `pane` is what actually closes it.

TESTS. Units: the corroboration truth table incl. every must-NOT-refuse case, and the full
provenance table (pane needs no corroboration even standing elsewhere; pane beats a disagreeing
env with the drift note; env-no-pane resolves but flagged; a subdirectory corroborates; THE
INCIDENT refused as `*unverifiedError`; the override loud; a cwd-less thread is no evidence; and
"not inside a sesh thread" is deliberately NOT an unverifiedError). `thread.info/local`
reproduces the incident against a REAL daemon and asserts a MUTATING verb is refused too and the
tag never reaches the victim; `thread.parent` gained the announce + refuse-to-infer assertions.
ANTI-GAMING (all reverse-edited, never git-checkout — H44; all `-count=1` — H75): corroboration
disabled → cell RED with the exact user report AND "hijacked" landing on the victim; naive prefix
in `withinDir` → sibling case RED; unverified note dropped → env case RED; refusal disabled →
thread.parent RED "silently parented … — the mis-parent bug". Each reversed and re-run green
(H88's rule: a reversal is not verified until green).

**BLAST RADIUS WAS REAL AND THE FIX WAS FAITHFULNESS, NOT WEAKENING.** Three cells
(thread.parent, ticket.list-current, and info's own env steps) drove inference with
`$SESH_THREAD_ID` set while standing in the TEST PROCESS's cwd (the repo) against threads parked
at `/tmp` — i.e. they were shaped exactly like the incident, and were correctly refused. A real
agent of a thread stands IN that thread's cwd (a headless turn's process cwd IS its thread's), so
they now run from `th.Cwd` via a new `runWithEnvDir`. No assertion relaxed, no axis dropped.
**TRAP, and I walked into it in the very cell where I had already written the warning:
`t.TempDir()` lives under `/tmp`, so a thread parked at `/tmp` CONTAINS it and reads as
CORROBORATION** — my first contradiction fixture could not be staged and passed vacuously. Stage
a contradiction as two UNRELATED SIBLINGS under one base, never against a `/tmp` thread.

GREEN: every non-conformance package plain AND `-race`; `go vet ./...`.
**FULL 253-CELL MATRIX RUN (48min, sesh f462537): 250 pass, 3 fail, 0 skip, 0 missing, 0 not-run,
2 justified n/a.** All three reds established as NOT mine rather than assumed:
`thread.transcript/codex/{local,remote}` reproduce BYTE-IDENTICALLY on a clean detached worktree
at a10ff42 (`last_reply = ""`, `reply_count = 0`) and the cell passes `--id` at every call site so
it never touches inference — pre-existing, unfixed here; `thread.codex-session-capture/codex/local`
passes serially 2/2 on this HEAD and on clean a10ff42, red only under full-suite concurrent load =
the real-codex flake class H62 recorded for this exact cell.
**THE FULL RUN CAUGHT TWO FAILURES MY BLAST-RADIUS PASS HAD MISSED, and the reason is the lesson:
`-run 'TestMatrix/...'` EXCLUDES EVERY NON-MATRIX TEST IN THE PACKAGE.** `TestEmptyIDFlagIsLoud`
and `TestTailCLIForms` had the same test-process-cwd fixture problem as the three cells I did fix,
and I never ran them. Filtering by subtest path is not a substitute for running the package —
`go test ./internal/conformance` includes plenty that no `TestMatrix/` filter reaches. Both fixed
the same faithful way (tail via `runWithEnvDir`, emptyid via `cmd.Dir` on the exec.Command it
builds itself; the emptyid comment records why it matters THERE specifically — that cell is about
the empty-SELECTOR footgun, and refusing on provenance before the guard under test is reached
would silently test the wrong thing).
**SECOND `-run` TRAP, same session: a `/` INSIDE an alternation group is read by go test as a
SUBTEST-LEVEL SEPARATOR**, so `-run 'TestMatrix/(thread.transcript/codex|thread.codex-session-
capture)'` silently ran only ONE of the two cells and reported `ok` — I nearly concluded from it
that the transcript cells passed on clean HEAD when they had never executed. Give each cell its
own `-run`, and confirm with `-v` that the cells you expected actually appear. `gofmt -l` still flags the usual pre-existing
toolchain-drift files (H48); every file I touched is clean.
LIVE-SMOKED in a fully isolated sandbox (own SESH_HOME/daemon/short `/tmp/sk.XXX` sockets, every
inherited SESH_* stripped, sandbox daemon killed by EXPLICIT pid and the tree removed; the live
daemon verified untouched): the incident refused verbatim; the legit no-pane case and a
subdirectory both resolve flagged `env`/unverified; the override works and announces itself; a
mutating verb rc=1 with the victim untagged; `thread new` announced a legit inferred parent and
made a ROOT (loudly) from the contradicting directory — both parents confirmed in the records.
Also LIVE-VERIFIED READ-ONLY on macbook against its REAL daemon and real threads (resolve from
the thread's cwd, refuse from `/usr` — the `~`-shortening renders on macOS too), and the skill's
step-1 snippet run verbatim in this very thread returns `source=pane verified=true`.
SWEPT (H75 leak class, not mine): four leaked `/tmp/sesh-conformance-*` sandbox daemons, 1.8 and
7 days old, killed by EXPLICIT pid (never `pkill -f` — H22/H74) with no suite running.
DEPLOY: **binary-only, NO daemon restart, no schema/API/wire change** (CLI-side only, so a mixed
fleet is trivially safe). **LIVE ON ALL SIX** at 304ef59, every installed binary
`vcs.modified=false`; every remote checkout was verified clean on main with nothing unpushed
BEFORE pulling (H49/H63). mymain/ideapad native, macbook+macstudio /opt/homebrew/bin/go, termux
PLAIN `go build` (verified CGO_ENABLED=1 / GOOS=android on the box, H22). **pocket4 needed
nothing** — it came back already at 304ef59 with its supervised daemon started 4s AFTER the binary
was written (myrig's post phase builds sesh per machine), so it had self-healed its whole H90/H91
backlog too; verified rather than assumed by comparing daemon start time against binary mtime.
termux has no local threads, so its check is `sesh help info` carrying the new text — the
resolution path cannot be exercised there.
**A running TUI/sidebar keeps the binary it launched with (H70), but nothing here changes the TUI.**


## H91 — TUI `goto-uuid`: jump the cursor to a thread by UUID, switching to the FIRST view that shows it (2026-08-23, sesh <this commit>; NO schema/API change; BINARY-ONLY, no daemon restart; ticket 83d1edbd)
Lukas ticket 83d1edbd "new sesh tui command - go to thread with given uuid": "opens up a prompt
where you have to type in the full UID of the thread or the short form of the UID. It then takes
you to that thread. It finds the thread in the first view where it appears." Pure TUI-client work
on top of H88's command registry — a new command is now literally a registry row plus a case.

CONFERRED two edges before building (AskUserQuestion). (1) When the CURRENT view already shows the
thread: "It should move the cursor to that thread" — i.e. NO view switch while you are browsing;
the display-order search is only for a thread the current view hides. (2) A thread the grid hides
by a DISPLAY SETTING: "Refuse loudly. But if the machine is online it should be there in at least
the `all` view" — which is exactly right and is why the only refusals left are machine-level
(offline owner + hide-offline, or a peer on a self-only grid) plus the filter.

BEHAVIOUR (internal/tui/goto.go): palette-only command `goto-uuid` (no default key — the surviving
key set is Lukas's, H88; rebindable via `[[tui.key]]`) opens a TARGETLESS line prompt — the typed
uuid IS the target, so unlike every other prompt it carries no row and works on an empty grid.
Lookup is a PREFIX match (case-insensitive, trimmed) over `m.machines` — the LAST-FETCHED MESH, not
the current view's rows, which is what lets it find a thread the view hides. Then: current view if
it admits the row, else the first view in DISPLAY order (`orderedViews`, so a custom `[[tui.views]]`
placed first WINS) that admits it; ViewAll admits everything so a visible thread always has a home.
A view switch defers the cursor to the PRESELECT path in the meshMsg handler (which also expands a
nested child's ancestors — H80's lesson that most interesting threads are children), and says so in
the note line. It LOCATES, it does not enter — `enter` still navs.

EVERY OTHER OUTCOME IS A LOUD REFUSAL THAT CHANGES NOTHING (no cursor move, no view change, and —
the load-bearing one — NO PRESELECT LEFT ARMED: a preselect that cannot land would sit there and
jump the cursor minutes later when the machine reconnects). Refusals: unknown prefix; ambiguous
prefix (names the candidates, "type more of it"); input that isn't hex+dashes (a name typed by
mistake gets "uuids are hex digits and dashes" instead of a confusing not-found); a match on an
OFFLINE machine while hide-offline is on (names the machine + `toggle-offline`); a match on a peer
while the grid is self-only (names `--all-machines`); and the ACTIVE FILTER hiding it (query
mismatch, or ^y's child exclusion) — a filter narrows EVERY view, so unlike a view mismatch it
cannot be fixed by switching, and silently ignoring it would leave the cursor sitting still with no
explanation.

ONE CONVERSION, NOT TWO: the snapshot→row build was extracted from `flattenMeshRows` into `meshRow`
and shared. View admission reads Archived/OnHold/Head/Busy/Flagged, so a second hand-written
conversion in the goto lookup that dropped a field would pick a view the grid does not actually
render the thread in — the plausible-but-wrong class.

TESTS. Units (goto_test.go) build their rows THROUGH the real `flattenMeshRows` so the model's view
filtering is the shipped one: full/short/uppercase/whitespace forms, the current-view-wins rule,
archived→archived + held→on-hold + all-stays-all, a custom view placed first, a nested child, and
every refusal; plus the prompt flow (open/type/enter, esc cancels, empty cancels, works with NO
selection) and a guard that the `?` popup and palette really list it. `goto-uuid` is in the offline
gate's LOCAL list (it never touches an owner — and gating it would refuse a jump AWAY from an
offline row). New conformance claim `goto-uuid` (registered AND declared — the H25 gotcha) drives a
REAL daemon through the PALETTE: archive two threads for real, wait until the active view really
drops them, jump to one by SHORT id → the grid switches to `archived` and the cursor is on it; jump
back to a live thread by FULL uuid → back to `active`; an unknown uuid is refused with nothing moved.
ANTI-GAMING (all reverse-edited, never git-checkout — H44; all `-count=1` — H75): current-view rule
removed → RED "landed in view archived, want all"; ambiguity check removed → RED; filter check
removed → RED both ways; the offline/self-only gate neutered → RED; the view-switch removed → claim
RED landing in view 3 (ViewAll — the meshMsg escalation, i.e. the plausible-but-wrong fallback);
preselect dropped on the switch → claim RED "cursor never landed".
**THE VACUITY THE SECOND NEUTER CAUGHT, worth remembering:** the first version of the claim put the
jump target ALONE in the destination view, so a cursor left at position 0 "landed" on it by accident
— the neutered preselect PASSED. The claim now seeds each destination view with a DECOY row that
sorts above the target (alphabetically in `active`; archived LAST so it heads the archived view's
archived_at-DESC order) and asserts the cursor is not already on the target before each jump.

GREEN: internal/tui plain and -race; `go vet ./...`; the full TUI claims suite serially — 65 pass,
2 fail, and both failures are the PRE-EXISTING macbook reds recorded in H80/H88, byte-identical
(`action-fork` "transcript <id>: exit status 1", the pi-transcript class; `uuid-popup-copy`, whose
claim stubs `wl-copy`, a WAYLAND tool, on a Mac). Every other non-conformance package green plain
and -race except the long-standing `TestMaintainerDropsStaleReportedBusy` "baseline: busy=idle
authority=" (H75/H81/H88; internal/daemon untouched here). `gofmt -l` still flags
internal/tui/predicate_test.go + internal/tui/tickets.go on clean HEAD (toolchain drift, H48) —
only touched files formatted.
LIVE-SMOKED in a fully isolated sandbox (own SESH_HOME/daemon/sockets under a short /tmp path,
inherited SESH_* stripped, four scratch threads, two archived; sandbox daemon killed by explicit pid
and the tree removed; the live daemon never touched): `p`→"goto"→enter→"4ec6271d"→enter switched to
`[archived]`, printed `go to 4ec6271d "parked-two" · switched to the archived view`, and put the `>`
on parked-two — NOT on the decoy row above it; the full uuid of a live thread jumped back to
`[active]` onto live-one; `deadbeef` and `dagster` both printed their loud ✗ lines with nothing moved.
**SMOKE TRAP (new, macOS): myrig defines a `sesh` shell FUNCTION that pins SESH_HOME to the LIVE
`$HOME/.sesh` and machine=macbook**, so a sandbox `zsh -c "source env.sh; sesh daemon status"` talks
to the LIVE daemon however carefully you set SESH_HOME. Call the sandbox binary by ABSOLUTE PATH.
Also: `setsid` does not exist on macOS — plain `nohup … &` for a sandbox daemon.

DEPLOY: **binary-only, NO daemon restart, no schema/API/CLI-flag change** (a pure TUI-client
command). Docs synced in the same change: `sesh help tui` long text + the sesh-cli SKILL
(palette-only list + a "Going to a thread by uuid" paragraph). A running SIDEBAR keeps the binary it
launched with (H70), so a deployed machine still needs `prefix+r` (or mmt-kill/mmt-start) before the
command exists inside its sidebar.
DEPLOY RESULT (2026-08-23, commit 250e394): live on **5/6** — macbook, macstudio, ideapad, termux
(plain `go build`, per H22) and mymain, every installed binary reporting `vcs.revision=250e394` +
`vcs.modified=false`, and `sesh help tui` carrying the goto text on all five. **pocket4 OFFLINE**
(ssh :22 timed out; it was already pending for H90) → PENDING, harmless (binary-only, mixed-mesh
trivially safe); when it returns: `cd ~/mysetup/sesh && git pull && go build -o ~/.local/bin/sesh.new
./cmd/sesh && mv -f ~/.local/bin/sesh.new ~/.local/bin/sesh` (no restart).
**mymain needed the throwaway route again** (H49/H63): its checkout is on ANOTHER AGENT'S branch
`shell-threads` at be04707 with 14 modified files, so it was never pulled — built from a detached
`git worktree` at origin/main, then the worktree removed. **GOTCHA WORTH KEEPING: a binary built in a
linked git WORKTREE gets NO `vcs.revision` stamp at all** (Go's VCS detection wants a real `.git`
DIRECTORY; the worktree has a `.git` FILE), so the verification step silently printed nothing and the
deploy looked failed when it had actually worked — confirmed behaviourally via `sesh help tui`, then
rebuilt from a `git clone --depth 1` of origin so the stamp is real. Also: mymain's `/usr/bin/go` is
1.24.4 while go.mod wants 1.25.0 (the toolchain auto-download covers it), and `go` is NOT on the
non-login ssh PATH there — use the absolute path.
## Trap digest — durable gotchas from the archived June 2026 build-out

The June 2026 build-out entries (H1-H33, plus the WHICH-CLIENT LAW / session-naming /
env-leak notes) were moved to `_dev/AGENTS.local.archive.md` (NOT imported into agent
context) on 2026-08-22 to keep this file lean — the live-log guidance is "read the last
few entries", so the two-month-old build log was pure per-session token cost. Nothing was
lost: those entries stay in full in the archive and in git history, the durable design
lessons are distilled in the "Reference" section just below, and the traps still
cross-referenced by shorthand in the entries above are kept here so those references
still resolve.

- **the H22 lesson** — on termux, build sesh with PLAIN `go build` (CGO=1 / GOOS=android).
  A `CGO_ENABLED=0` binary runs but its pure-Go resolver can't resolve tailnet MagicDNS
  names (termux has no /etc/resolv.conf) -> the box silently drops off the mesh. Same
  Go-resolver-vs-system class as H45 (NM clobbered resolv.conf) and H73 (self-bind DNS).
- **the H30 pipe-exit trap** — never gate a check/deploy on `cmd | tail` (or any pipe): a
  pipeline's exit status is the LAST command's, so a failed build / `install-home` piped
  to `tail` reports success and leaves a stale half-deploy. Check the real command's
  status. Related zsh trap: a bare `===` / `=word` token in a compound line aborts the
  WHOLE line silently before later commands run.
- **the H33 install-home rule** — `python3 scripts/install-home.py "$MYRIG_TARGETS"` takes
  the FULL comma-separated list. A lone machine name makes `all`-gated files "not match
  targets" and DELETES their symlinks. shell.sh is a rendered jinja (needs a render); the
  confs are symlinks (a `git pull` suffices, but a running tmux server needs source-file).
- **the H25 gotcha** — a new TUI conformance claim must be added to the HARDCODED
  `declaredTUIClaims` list (tui_test.go) AND registered; `registerTUIClaim` only BINDS, so
  a claim missing from `declaredTUIClaims` silently never runs (TestTUIClaimsComplete only
  checks declared->bound, not the reverse).
- **termux daemon relaunch** (H21/H38, repeated in every later termux deploy) — no
  supervisor: kill the old daemon by EXPLICIT pid (never `pkill -f` — it matches your own
  ssh shell, H22/H74), `mv` the new binary in first, then `setsid nohup
  ~/.local/bin/sesh daemon run` with SESH_HOME=~/.sesh SESH_MACHINE=termux sockets
  sesh/sesh-master. /tmp is unwritable (log to $HOME); termux is an inbound-less leaf (no
  SESH_API_ADDR / token).
- **Android hides non-dumpable processes from their own uid** (H108 follow-up 3) — OpenSSH marks
  `ssh-agent` non-dumpable and Android's `/proc` (hidepid) then hides it from every Termux-side
  `ps`/`pgrep`/`/proc` walk; only adb (`dumpsys meminfo`) sees them. Every Termux-side process
  count or RSS sum (H83, H84, H99, H108's "193 MB") is blind to that class. Also: until the myrig
  fix lands, every non-interactive `ssh-target termux '…'` runs termux's zshenv and leaks one
  ssh-agent (~0.9 MB) — count your probes.
- **THE WHICH-CLIENT LAW** — tmux cannot map a popup/pane/subprocess pty back to the
  client that triggered it; `display-message -p '#{client_name}'` there is an AMBIENT
  guess. Resolve the client via a BINDING's own `#{client_name}` carrier (baked into
  $SESH_NAV_CLIENT by the myrig popup bindings), never ambiently.

## Reference: foundational decisions & gotchas (from the original 76-cell build)

Run `go run ./cmd/sesh matrix grid` (after `go test ./internal/conformance`) to see the
rendered grid.

Every feature is honest (real agent in a real tmux pane; remote = real `ssh
localhost` hop). All 24 feature rows green across their axes:
matrix spine; daemon + SQLite/WAL; tmux layer incl. **nav** (local + remote via
`--machine` routing); thread layer local+remote — new.headed/kill/list/resolve-pane,
runtime-state, send.headful, **new.headless + send.headless** — for all 3 agents;
ticket layer (create/list-by-thread/set-status/needs-input/send-prompt/ownership);
api.http-json; daemon.mesh-read.

### Things resolved that were once blockers

- **codex trust**: codex shows a per-directory "trust this dir?" prompt that ate
  input. Fixed by `agents.EnsureCodexTrust` — sesh writes `[projects."<dir>"]
  trust_level="trusted"` into codex's `config.toml` at spawn; `CODEX_HOME`
  (SESH_CODEX_HOME) lets tests isolate it with auth.json symlinked.
- **activity probe**: codex's thinking-phase animates only a ~1s timer; probe now
  EARLY-EXITS on working (fast) with a ~3s idle-confirm window.
- **send timing**: codex drops input when text+Enter are back-to-back → 250ms settle
  before Enter (tmux.SendText + test sendKeys).
- **headless**: stateless-per-turn (Lukas's choice). A headless thread is a durable
  conversation, no tmux window; a turn = `<agent> --print/exec --resume`; "working"
  = a turn process is in flight (daemon in-memory registry). pi is NOT N/A — it has
  `--print --session-id`. codex's session id is parsed from `codex exec --json`
  (thread.started) on the first turn.

## Key decisions baked in

- **runtime-state = two orthogonal axes** (activity from pane content-diff, attachment
  from `tmux list-clients`); needs-input = activity==waiting regardless of attachment.
  Lukas signed off (provisionally) in Phase 3b. SPEC §3/§4 updated. See the memory
  `sesh-v2-runtime-state-design`.
- **content-diff probe**: samples a pane 4× over ~1.14s, working iff a MAJORITY of
  intervals change (rejects one-off idle blips like claude's rotating hints / MCP
  startup; catches a real turn's animated spinner). All three TUIs animate while
  working AND are byte-stable when idle.
- **`--machine X` routing**: pseudo-global flag in cmd/sesh; main forwards the command
  (minus --machine, plus the peer's SESH_HOME/SESH_MACHINE) over a real ssh hop.
  Excludes meta commands (peer/matrix/help). This is the honest "remote" path.
- **ticket ownership**: SESH_TICKET_OWNER; ticket commands auto-route to the owner.

## Test gotchas learned the hard way

- A freshly spawned agent's pane is blank → byte-STABLE → content-diff misreads it as
  waiting. Always `waitThreadReady` (TUI rendered ≥3 non-blank lines AND activity
  waiting) before sending, or keystrokes are lost.
- `tmux display-message -t =session` silently returns empty for the `=` exact-match
  prefix → use `list-sessions`/`list-clients` and match the name in Go.
- tmux escapes control bytes in `-F` output (0x1f → literal `\037`); use a TAB field
  separator (passed through verbatim) and treat a wrong field count as a loud error.
- Nested `tmux attach` works headlessly via `env -u TMUX tmux attach` in a viewer
  session (used to test the attached state).
- **Store migrations are APPEND-ONLY** — a mid-list insert desyncs already-deployed DBs
  (their `meta.version` skips the inserted element). Always append a new migration last.
- **Never settle a conformance claim on a row's ABSENCE alone** — it is vacuously true
  before the maintainer first publishes the row. Settle on PRESENCE first, then assert
  the negative.
- **The conformance suite CANNOT catch deploy-env gaps** — test daemons inherit the dev
  shell (PATH, mise shims, API keys), but the supervised production daemon has a bare env.
  So live-smoke after deploy is MANDATORY for daemon-exec paths (headless turns run via
  `$SHELL -c`, like tmux runs pane commands); the supervisor ini pins PATH/shims/SHELL.
