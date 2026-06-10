# V1-parity roadmap — every feature, ticked off one by one

Agreed with Lukas 2026-06-10 (the (b)-list discussion on `V1_FEATURE_AUDIT.md`).
This is the working contract: each feature is built **full-featured, not minimal**
("no scarcity mindset" — the v1 behavior is the starting point, cut only with a
concrete reason), adapted to v2's methodology: loud errors, the two-axes state
model, mesh routing, and matrix/claims honesty. **Every feature starts with a
research pass over its v1 implementation** (refs below are the starting points,
in `~/mysetup/sesh`).

Status legend: `[ ]` todo · `[~]` in progress · `[x]` shipped (gate green + deployed).

## Checklist (dependency order)

- [ ] **A1** TUI column system + `[tui]` config defaults
- [ ] **A2** `[[cwd_label]]` display rules + CWD column + `sesh cwd-label`
- [ ] **A3** Full fzf-style filter mode (fuzzy scorer, caret editing, ctrl+t search modes, `--filter`)
- [ ] **A4** Predicate grammar + custom views (`[[tui.views]]`, ticket-aware)
- [ ] **A5** Parent/child threads + collapsible tree view
- [ ] **B1** `[[hooks]]` event hooks + `sesh hooks` CLI
- [ ] **B2** Per-thread notifications (on/off + config default) + myrig toast wiring
- [ ] **C1** `sesh await`
- [ ] **C2** `sesh delegate` (+ `--sandbox`)
- [ ] **C3** `sesh subscribe` + turn-delivery engine
- [ ] **D0** Transcript-resolution layer (per-agent transcript file location)
- [ ] **D1** `sesh tail`
- [ ] **D2** `sesh copy` (transcript copy)
- [ ] **D3** `sesh new --fork-from` (rewind-and-branch)
- [ ] **D4** `sesh backup` / `restore`
- [ ] **D5** Adopt/register foreign agents
- [ ] **D6** `meta` KV on threads
- [ ] **E1** SESH_MACHINE: daemon refuses silent hostname fallback
- [ ] **E2** `sesh doctor`
- [ ] **E3** Spawn knobs: `--msg`, `--sandbox`, config.toml spawn defaults
- [ ] **E4** v1→v2 records import (migration)

Dropped by decision: **autoname** (Lukas 2026-06-10). Still deferred (revisit on
demand): watch-stream/emit contract and the other (c)-list items in
`V1_FEATURE_AUDIT.md`.

---

## A — TUI

### A1. Column system + `[tui]` config defaults
**Why:** prerequisite for CWD/CREATED/ID columns, per-column toggles, and A4/A5.
Lukas: HEAD/BUSY text columns OFF by default (the `●▶` glyphs carry the state).
**v1:** `internal/tui/columns.go` (+`columns_test.go`) — named column registry
(id, name, cwd, agent, machine, socket, tags, created, TKT, …), per-column width
+ renderer, `[[tui.columns]]` config adds RULE columns (a predicate → glyph),
`i`/`ctrl+w` toggles, `--match`/`--full` flags, horizontal scroll clamp.
**v2 design:** named-column abstraction in `internal/tui` replacing the hardcoded
`fmt.Sprintf` row; flags `--columns a,b,c` / `--no-columns x,y`; `[tui] columns`
default in `<SESH_HOME>/config.toml` (extends the existing config file; loud on
unknown column names). Default set: glyphs, attachment, MACHINE, AGENT, NAME,
TAGS (+CWD once A2 lands). HEAD/BUSY available, off by default. Rule columns
arrive with A4's predicates.
**Verify:** TUI claims: column defaults render, flag/config overrides render,
unknown name loud. Unit tests for layout.

### A2. `[[cwd_label]]` rules + CWD column + `sesh cwd-label`
**Why:** the CWD column should show transformed, readable paths.
**Decision:** SEPARATE rule table from `[[session_name]]` (labels carry no tid8) —
same engine (`internal/config/naming.go` generalized).
Lukas's rules: box root → `{boxname} <{boxid}>`; box subdir →
`{boxname}/{rel} <{boxid}>`; `~/mysetup/x` → `mysetup/x`; else path with `$HOME` → `~`.
**v1:** `internal/cli/cwdlabel.go` + `[[tui.cwd_rules]]` (config.go:152) — first
match wins, no subprocess, in-process regex→template.
**v2 design:** `[[cwd_label]]` in config.toml; applied at TUI render time (and
available to pickers via a `--label` on list/grid JSON? — decide during build);
`sesh cwd-label <path>` debug verb. Loud on bad regex/placeholder (daemon-load
parity with session_name).
**Verify:** unit tests over the four rules; TUI claim: CWD column shows the
transformed label of a real thread's real cwd.

### A3. Full fzf-style filter mode
**Why:** Lukas wants the COMPLETE v1 apparatus, explicitly not a minimal core.
**v1:** `internal/tui/fuzzy.go` (FuzzyMatchV1 scorer), filter parts of
`model.go`/`update.go`/`view.go`: `/` enters; typed query narrows live with
match count `13/24`; **caret editing** (left/right/home/end, insert at caret);
**Esc APPLIES the filter and returns to normal mode** (filter stays active,
shown); enter jumps to best match; **ctrl+t toggles search mode name+cwd ↔ uuid**
with the mode + `^t→<other>` hint right-aligned in the prompt; `--no-filter`
flag (v1 STARTED filtered by default).
**v2 design:** port `fuzzy.go` + the filter mode wholesale, re-targeted at
ThreadRow fields (name + cwd-label as primary mode, uuid as the ctrl+t
alternate). `sesh tui --filter` starts in filter mode; the tmux `prefix+s`
binding launches `--filter --cursor`. Quit-Esc stays normal-mode-only (shipped
claim `quit-esc` already encodes this).
**Verify:** claims: filter narrows real rows (count line correct), caret editing,
ctrl+t switches target (uuid query matches only in uuid mode), Esc applies and
keeps the filtered view, `--filter` starts filtering.

### A4. Predicate grammar + custom views (ticket-aware)
**Why:** Lukas's motivating example: a view of "threads WITH an open ticket" and
"threads with NO ticket". Tab cycles views; the title names the current one.
**v1:** `internal/tui/predicate.go` — compiled mini-language shared by rule
columns + `[[tui.filters]]` view modes. Grammar: `or/and/not`, parens,
`selector == != ~ !~ literal`, bare state atoms (idle/busy/attached/detached/
live/archived), `meta.<key>` presence. Compile-time loud validation.
**v2 design:** port the grammar re-mapped to the two axes — selectors: `head`
(headful/headless), `busy` (busy/idle), `attached`, `archived`, `agent`,
`machine`, `name`, `cwd`, `id`, `tags`, **`ticket`** (open-ticket state: needs
ticket info joined into grid rows — daemon-side: grid rows gain ticket summary
fields, e.g. open count + needs-input), and `meta.<key>` once D6 lands. Bare
atoms: `headful, headless, busy, idle, attached, detached, archived, ticketed`.
`[[tui.views]]` in config.toml: `{ name = "ticketed", filter = "ticket and not archived" }`;
Tab cycles built-ins (active/archived/all) + custom views in order.
**Verify:** unit tests for the grammar (incl. loud compile errors); claims: a
custom view from config shows exactly the matching real threads; ticket
selector flips when a real ticket is created/closed.

### A5. Parent/child threads + collapsible tree view
**Why:** Lukas: children collapse under their parent (▾/▸ + ├/└ rails), DEFAULT
COLLAPSED, configurable in config.toml (`[tui] expand_children = false`),
nesting supported.
**v1:** `internal/tui/model.go:262-` (expanded map, defaultExpanded,
`--expand` flag), `:505` parent-chain walk w/ cycle guard, `:562-671` visible-set
building (children nest under VISIBLE parents; a child whose parent is filtered
out promotes to top level), tree_test.go; `→` expands / `←` collapses.
**v2 design:** data model first — `parent` on api.Thread (+store migration +
`thread new --parent <id>` + wire). Then the tree rendering port. Cross-machine
nuance v1 didn't have: parent and child may live on different machines (mesh
rows) — group by parent uuid regardless of machine. Glyph rails per v1.
**Verify:** matrix cell for `thread.new --parent` (record + wire, local+remote);
claims: tree renders real parent/child rows nested, default collapsed, →/←
fold/unfold, config default honored, filtered-out parent promotes child.

## B — Daemon eventing

### B1. `[[hooks]]` + `sesh hooks` CLI
**v1:** `internal/hooks/hooks.go` (Runner: match → exec with event env, timeout,
muted set), `internal/events/events.go` (Event types — status edges),
`[[hooks]]` config (event + filters + command), CLI `hooks list/enable/disable/test`
(persisted runtime mute; synchronous test).
**v2 design:** the maintainer already computes every state edge per tick —
emit events (busy→idle, idle→busy, head changes, needs-input onset) into a hook
runner in the daemon. `[[hooks]]` in config.toml: `event`, optional filters
(agent/machine/tag/name regex), `command` (run via `$SHELL -c`, thread fields in
`SESH_EVENT_*` env). `sesh hooks list/enable/disable/test` (mute persisted in
the store, not the config file). Loud: unknown event names refuse the daemon.
**Verify:** matrix cells: a real hook command fires on a real busy→idle edge
(file-touch assertion), filters scope it, disable mutes it, test runs it
synchronously. Both localities where sensible (hooks are per-daemon — local).

### B2. Per-thread notifications
**Lukas:** per-thread on/off toggle, config.toml default `notifications = true`.
**v1:** notif-on/notif-off (myrig) + the hook layer; sesh-notify toast.
**v2 design:** `notify` bool on the thread record (default from `[defaults]
notifications = true` in config.toml); `sesh thread notify --id X --on/--off`;
TUI key (`n`) toggles + a column/glyph (muted bell); hook events carry
`SESH_EVENT_NOTIFY=0/1` so the user's notification hook filters on it (the
DAEMON doesn't know about toasts — myrig's hook command does, keeping
mechanism/policy split). myrig: port sesh-notify + a default needs-input hook.
**Verify:** cells: toggle persists + round-trips the wire; hook env reflects it;
TUI claim for the toggle key + glyph.

## C — Agent-to-agent tier

### C1. `sesh await`
**v1:** `internal/cli/await.go` — block until a thread settles (busy→idle),
`--timeout`, `--poll` interval, `--json` result; mesh-aware.
**v2 design:** verb over the status/mesh primitives: await `--id` (routable
`--machine`), settles when `busy == idle` (either head state), `--timeout`
(loud non-zero on expiry), `--poll` default ~1s vs the maintainer tick. Exit
code contract documented for scripting.
**Verify:** matrix cells (all 3 agents × localities): await returns when a real
turn really finishes; times out loudly when it doesn't.

### C2. `sesh delegate` (+ `--sandbox`)
**v1:** `internal/cli/delegate.go` — ephemeral one-shot headless worker:
spawn→ask→await→print reply→delete; `--keep`, `--sandbox`, `--model`, remote
spawn support.
**v2 design:** composition verb over green primitives (new --headless +
send-headless + await + delete), `--machine` routable, `--keep` keeps the
thread, `--sandbox` (see E3) restricts the agent, prints the reply to stdout
(+`--json`). Failure leaves NO orphan: delete on error paths unless --keep.
**Verify:** cells (3 agents × localities): a real question really answered by a
real ephemeral agent; record gone after (kept with --keep); sandbox flag
observable (E3's verification).

### C3. `sesh subscribe` + turn-delivery engine
**v1:** 3 verbs + engine: auto-push a subscribee's replies into subscriber
sessions, dedup, cycle guard, rate limit.
**v2 design:** research pass FIRST (the engine is the most design-heavy port):
subscriptions in the store; the daemon's maintainer/hook layer observes a
subscribee's busy→idle edge, reads the last reply (needs D0's transcript layer
for reply text — sequence AFTER D0), delivers via `thread send` into subscriber
panes (or send-headless), dedup by turn, cycle guard (A subscribes B subscribes
A), rate limit. Cross-machine: subscribee and subscriber on different daemons —
delivery routes over the mesh.
**Verify:** cells: a real reply lands in the real subscriber pane exactly once;
cycle guard refuses; unsubscribe stops delivery; cross-machine twin.

## D — Transcript layer

### D0. Transcript-resolution layer
**Why:** tail/copy/fork/backup/subscribe-delivery all need "where is this
thread's transcript and how do I read it".
**v1:** `internal/agents` state-dir resolution (CLAUDE_CONFIG_DIR, ~/.codex,
~/.pi) + per-agent transcript formats (claude: ~/.claude/projects JSONL; codex:
rollout files — v2 already discovers these for resume; pi: session files).
**v2 design:** `internal/agents` gains per-agent transcript locate+read
(+last-reply extraction for C3/D1). Loud when a transcript is missing — never a
silent empty. This is READ-ONLY; mutation arrives only with D3/D4.
**Verify:** unit+cells: locate+read a real transcript for each of the 3 agents
after a real turn; last-reply matches what the agent actually said (VERMILION-
style sentinel).

### D1. `sesh tail` — print/follow a thread's transcript (v1
`internal/cli/*tail*`; research exact flags). Routable cross-machine.
**Verify:** cells per agent: tail shows a sentinel reply that was really given.

### D2. `sesh copy` — copy transcript/last-reply to clipboard (v1 comms.go/
copy_remote.go; research scope: last reply vs whole transcript vs file). Reuses
the TUI clipboard helper + `tmux stage-file` for cross-machine.

### D3. `sesh new --fork-from <id> [--message-id N]` — rewind-and-branch a
conversation under a NEW thread (v1 `internal/fork/fork.go`). Per-agent
transcript rewrite under a fresh agent session id. The 409 "would fork" guard
stays for ACCIDENTAL forks; this is the deliberate verb.
**Verify:** cells per agent: forked thread continues from the branch point
(remembers pre-fork sentinel, diverges after); original untouched.

### D4. `sesh backup` / `restore` — idempotent sha256 transcript backups into
portable SQLite; `--to`, `--rewrite-cwd`, `--force` (v1 `internal/backup/`).
Research what of the v1 schema carries over; restore must round-trip D0's
formats.
**Verify:** cells: backup→wipe→restore→resume continuity for each agent.

### D5. Adopt/register foreign agents — bring an agent sesh didn't spawn under
management (v1 `import.go`/register). v2 stance was provenance-only; the
deliberate counter-design: `sesh thread adopt --pane <pane>` detects the agent
kind+session, birth-stamps the pane, creates the record. Loud when detection
is ambiguous.
**Verify:** cells: a manually-launched real agent pane becomes a managed thread
(status/send/stop all work on it).

### D6. `meta` KV — `sesh meta set/get/list` arbitrary per-thread metadata
(v1 meta.go); feeds A4's `meta.<key>` selectors and dynamic columns.
**Verify:** cells: set/get round-trip + wire; predicate selector sees it.

### E — Hygiene

### E1. SESH_MACHINE silent fallback — **yes, this is a real v2 bug**: v2's
`config.Load()` falls back to `os.Hostname()` when SESH_MACHINE is unset — the
exact class of silent fallback the AGENTS.md rules forbid (v1 refused instead).
Machine identity is load-bearing (mesh routing, ownership). Fix: the DAEMON
refuses to start without an explicit SESH_MACHINE; pure-client CLI calls may
keep the hostname default (nothing persists it) — decide during build whether
even that should warn.
**Verify:** cell: daemon start without SESH_MACHINE = loud refusal naming the
variable.

### E2. `sesh doctor` — checklist verb (v1 doctor.go as reference): agents on
the DAEMON's PATH (via a daemon endpoint, not the caller's shell), SHELL/zshenv
parity, tmux conf actually loaded on the work server, peers reachable +
transports, api token file perms (600), clipboard tool present, config.toml
parse status (session_name/cwd_label/views/hooks). Each check: OK/FAIL + fix
hint. `--json`.
**Verify:** free tests: each check fires on a real induced misconfiguration in
a sandbox.

### E3. Spawn knobs — `--msg <text>` on `thread new` (initial prompt once the
pane is READY — reuse the readiness probe daemon-side, never the blank-pane
race); `--sandbox` on new/resume/delegate (claude: permission mode; codex:
sandbox flag; pi: research equivalent — loud N/A if none); spawn DEFAULTS in
config.toml (`[spawn]` table: default sandbox per agent, default model?, …).
NOTE: Lukas's message cut off here ("you should be specify in config.toml
what") — assumed: per-agent spawn defaults. CONFIRM the intended scope.
**Verify:** cells per agent: --msg's text really reaches the conversation;
--sandbox observably restricts (agent-specific assertion, e.g. a write refused).

### E4. v1→v2 records import — `sesh import --from-v1 <SESH_HOME>`: thread
records (uuid, name, tags, cwd, agent, session-ids, parent once A5 lands,
archived) from v1's store; transcripts stay in the agents' own homes (v2 resume
works off session ids). Per-machine run. Dry-run mode + loud per-record report.
**Verify:** free e2e: import a fixture v1 store; resumed imported thread has
real continuity (the agent remembers).

---

## Execution protocol (per feature)
1. **Research**: read the v1 implementation (refs above) + its tests; write a
   short "v1 did it like this / v2 adapts like this" note into this file.
2. Register matrix cells / declare TUI claims FIRST (red), then implement to green.
3. Full conformance gate, deploy both machines, live smoke, tick the box here.
