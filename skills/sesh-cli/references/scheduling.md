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
sesh schedule list [--all-machines] [--thread <id>] [--json]
sesh schedule show --id <id|prefix|name>          # definition, rules, next/last fire, counters
sesh schedule runs --id <ref> [--limit N]          # the run history: fired | skipped: <why> | failed: <err>
sesh schedule run-now --id <ref> [--force]         # fire it NOW and print the outcome — the way to test one
sesh schedule pause|resume|remove|edit --id <ref>  # resume recomputes next fire from now (no backlog)
```

**The clock.** Exactly one of `--cron '<min hour dom mon dow>'` (names, ranges, lists, `*/n`;
a restricted day-of-month AND day-of-week match when either does), `--every <dur>`
(minimum 10s, anchored at creation or `--not-before`), or `--at '<YYYY-MM-DD HH:MM>'`
(fires once, then disables itself). `--tz` defaults to the OWNING machine's zone and is
RECORDED (a routed create means the owner's zone). A local time that does not exist on
spring-forward day is skipped; a repeated one on fall-back day fires once. Occurrences
missed while the daemon was down are NEVER replayed: `--catchup skip` (default) rolls
forward and counts them (`missed N` in the listing, loud in the daemon log); `--catchup
once` fires exactly one catch-up run. `--not-after`, `--max-fires` (delivered runs only —
skips do not count) and `pause` bound a schedule's life; a schedule that fails
`[schedules] disable_after_failures` (10) times running is DISABLED with the reason
(`schedule list` shows `DISABLED: …`; `sesh doctor` warns).

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

**Seeing them.** `schedule list` per machine (`--all-machines` fans out); the opt-in TUI
column `sched` shows a thread's earliest next fire (`in 12m`, with a count when several);
the `I` details popup has a `schedules` row; `sesh doctor` reports breaker-disabled
schedules, an overdue enabled one (the loop is wedged), and the kill switch. Deleting a
thread removes the message schedules targeting it in the same transaction (the delete
says so). Hooks: `schedule_fired` / `schedule_failed` events with
`SESH_SCHEDULE_ID/NAME/OUTCOME` (skips are not events). `[schedules] enabled = false`
stops a machine firing anything (`run-now` still works).
