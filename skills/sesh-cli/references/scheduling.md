# Scheduled messages and thread spawns

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules. Dated incidents/version notes describe
the measured builds, not current fleet deployment.

<!-- BEGIN PRESERVED TOPIC -->
## Scheduled work (`sesh schedule`)

A schedule is a clock (cron / interval / one-shot) plus an ACTION plus RULES, with its own
recorded state. Two actions: **`message`** delivers text into an EXISTING thread; **`spawn`**
creates a NEW thread in a directory with a prompt. A schedule lives on the machine that
EXECUTES it — a message schedule on its target's owner (`schedule message` auto-routes
there), a spawn schedule where its cwd is (`--machine` to place it elsewhere) — and is never
mesh-replicated: `schedule list --all-machines` is a live fan-out. The daemon validates
everything at creation and prints back the zone and the first fire, so what you typed is
what will happen.

```bash
# a heartbeat: every 20 min, only if the thread has been quiet 10 min, never into a dead pane, at most 30 times
sesh schedule message --id <thread> --every 20m --text 'Continue with the plan. If blocked, say so and stop.' \
    --if idle --idle-for 10m --when-headless skip --max-fires 30 --not-after '2026-09-20 18:00'
sesh schedule message --id <thread> --cron '0 9 * * 1-5' --text 'Morning status?' --when-headless revive
sesh schedule spawn --agent pi --cwd ~/dev/proj --cron '0 9 * * 1-5' --prompt 'Review the CI failures since yesterday' --parent <virtual-group>
sesh schedule spawn --agent claude --cwd ~/dev/proj --every 6h --prompt-file ~/prompts/audit.md --on-turn-end stop+archive --max-runtime 30m
sesh schedule list [--all-machines] [--thread <id>] [--state <s>] [--all] [--json]
sesh schedule show --id <id|prefix|name>          # definition, rules, state, next/last fire, counters
sesh schedule runs --id <ref> [--limit N]          # the run history: fired | skipped: <why> | failed: <err>
sesh schedule run-now --id <ref> [--force]         # fire it NOW and print the outcome — the way to test one
sesh schedule archive --id <ref>                   # DONE WITH IT: off the clock, out of the listing, record kept
sesh schedule unarchive|resume --id <ref>          # back on the clock; next fire recomputed from now (no backlog)
sesh schedule pause --id <ref>                     # off for now, still in the listing
sesh schedule edit --id <ref> [--cron …|--every …|--at …] [--name …] [--text …]
sesh schedule remove --id <ref>                    # DELETE one record and its runs
sesh schedule prune [--state spent] [--before <t>] [--dry-run]   # bulk delete; never runs on its own
```

**Archive a schedule you are finished with — do not leave it paused, and do not delete it.**
`archive` is the verb for "this served its purpose": it stops firing, leaves the default
listing, and keeps the record and its run history, which is the only account of what was
scheduled and what it did. `pause` means "off for now, I am coming back" and deliberately
STAYS in the listing, so a paused pile is noise that hides the live schedules. Deleting
(`remove`/`prune`) throws the history away, so leave it for a record you are sure is junk.
This matters most for the one-shot backstops and reminders an agent sets for itself: when the
work lands early, **archive the backstop**.

**States** (`list` shows the live ones and names what it held back):

| state | meaning | in `list` by default |
|---|---|---|
| `active` | on the clock | yes |
| `paused` | off by hand, coming back | yes |
| `broken` | the failure breaker tripped — needs a human | **yes** |
| `spent` | its clock is exhausted: a one-shot that fired, `--max-fires` reached, `--not-after` passed. Nothing can make it fire again, so there is nothing to resume | no |
| `archived` | set aside deliberately, or carried there by archiving its TARGET THREAD; `unarchive` puts it back | no |

`--state <s>` (comma-separated) or `--all` shows the rest; `--state spent` is how you find
what finished. `spent` and `broken` are the daemon's own verdicts and cannot be set by hand.
A one-shot that fires becomes **`spent`**, not `archived` — it leaves the listing either way,
but `unarchive` on a passed instant would be a lie.

**The clock.** Exactly one of `--cron '<min hour dom mon dow>'` (names, ranges, lists, `*/n`;
a restricted day-of-month AND day-of-week match when either does), `--every <dur>`
(minimum 10s, anchored at creation or `--not-before`), or `--at '<YYYY-MM-DD HH:MM>'`
(fires once, then disables itself). `--tz` defaults to the OWNING machine's zone and is
RECORDED (a routed create means the owner's zone). A local time that does not exist on
spring-forward day is skipped; a repeated one on fall-back day fires once. Occurrences
missed while the daemon was down are NEVER replayed: `--catchup skip` (default) rolls
forward and counts them (`missed N` in the listing, loud in the daemon log); `--catchup
once` fires exactly one catch-up run. `--not-after`, `--max-fires` (delivered runs only —
skips do not count) and `pause` bound a schedule's life; each of those ends leaves the record
`spent`, out of the default listing. A schedule that fails `[schedules]
disable_after_failures` (10) times running becomes `broken` with the reason — which
`schedule list` keeps showing and `sesh doctor` warns about, because it is a problem report,
not a finished job.

**Message rules — evaluated against the target's live state at fire time, every ending
recorded.** On hold ⇒ `skipped: on hold` (`--ignore-hold` overrides); archived ⇒ skipped
(`--allow-archived`); `--if a,b,c` — all must hold, from the closed list `idle busy headful
headless attached detached flagged not-flagged archived not-archived blocked not-blocked`
(unknown words are refused at creation); `--if idle` implies a 60s quiet dwell since the
last activity (`--idle-for 10m`, `--idle-for 0` for none — a bare idle read can be stale);
a busy target ⇒ `skipped: busy` unless `--when-busy send`. Then the delivery follows the
pane that exists NOW: headed Pi gets literal RPC steering (preserving its editor,
no terminal typing guard); other live agents get a guarded paste (a typing viewer
⇒ `skipped: viewer typing`, never queued — a periodic sender has a next occurrence;
`--respect-typing 0` disables that guard). A headless target gets a real headless turn
(`--when-headless turn`, the default), is REVIVED into a pane first (`revive` — the
daemon's own resume, so codex-before-its-first-turn is the usual loud N/A), or is left
alone (`skip`). NB "attached/unattached" in the sense of "is there a live pane" is the
HEAD axis (headful/headless): the daemon can revive a pane, it cannot make a human look.

**Spawn rules.** Each run is `thread new` on the schedule's machine — headless by default,
`--headed` for a real pane (the prompt uses the agent's delivery transport once ready:
Pi RPC, otherwise guarded paste; an agent never becoming ready fails loudly) — named
by `--name-template` (default
`{schedule}-{date}-{time}`), optionally under `--parent` (point it at a virtual group so a
schedule's runs collapse into one node). Run threads have auto-flagging DISABLED
(`--flag-on-end` re-enables it) — an unattended recurring turn would otherwise flag itself
every time. `--if-previous skip` (default) skips a run while the previous run's thread is
still GOING — a live pane or a turn in flight, never "the record exists" — or
`stop-previous` / `spawn-anyway`; `--max-runtime` stops an overrunning run and records it
failed. `--on-turn-end` applies at the run's first turn end: `keep` (default — the finished
run stays an ordinary headless·idle thread you can read with `sesh transcript` or
continue), `stop`, `archive`, `stop+archive` (delegate's ephemeral contract), `delete`. A
run's row records its thread; a daemon restart mid-run reaps a headless run (its turn was
the daemon's goroutine) and keeps watching a headed one. The effective `[spawn]` mode is
recorded and printed (`mode: yolo`) — an unattended recurring agent under yolo is a
posture worth knowing.

**Seeing them.** `schedule list` per machine (`--all-machines` fans out) shows the LIVE
states and prints `+ N spent, M archived not shown`; the opt-in TUI column `sched` shows a
thread's earliest next fire (`in 12m`, with a count when several) counting ACTIVE schedules
only; the `I` details popup has a `schedules` row; `sesh doctor` reports `broken` schedules,
an overdue active one (the loop is wedged), and the kill switch.

**A thread's archive carries its schedules.** Archiving a thread archives the active message
schedules aimed at it, and un-archiving restores exactly those (not ones you archived by hand,
and not ones already paused) — the `thread archive` output says how many moved. A schedule
created with `--allow-archived` is exempt, since it exists to fire into a parked thread. The
per-run guard still skips delivery into an archived target regardless, so a schedule made
after the thread was parked never fires into it either. Deleting a thread REMOVES the message
schedules targeting it in the same transaction (the delete says so) — gone, not archived,
because a schedule whose target no longer exists can never work. Hooks: `schedule_fired` / `schedule_failed` events with
`SESH_SCHEDULE_ID/NAME/OUTCOME` (skips are not events). `[schedules] enabled = false`
stops a machine firing anything (`run-now` still works).
