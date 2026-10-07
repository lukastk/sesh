# Messages, Pi commands, delegation, and state reporting

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules. Dated incidents/version notes describe
the measured builds, not current fleet deployment.

<!-- BEGIN PRESERVED TOPIC -->
## Driving an agent, delegating, awaiting

```bash
sesh thread send --id <id> --text 'run the tests'          # live Pi: RPC steer; others: guarded terminal paste
sesh thread command --id <pi-id> --text '/compact retain the plan' --timeout 5m # explicit Pi command; real completion/error
sesh thread send --id <id> --text 'fix it' --wait --timeout 5m  # ...and block until the turn SETTLES (idle/blocked);
                                                           # fails fast (~5s) if the input produces no state change
sesh thread wait --id <id> --until settled --timeout 5m    # block until a state: busy|idle|blocked|settled
                                                           # (settled = idle-or-blocked; loud error naming the last state on timeout)
sesh thread send-headless --id <id> --text 'summarize'     # run a stateless turn on an idle thread
sesh thread send-headless --id <id> --text 'quick check' --model anthropic/claude-haiku-4-5  # override the model for THIS turn only
sesh thread headless-reply --id <id> --json                # poll a headless turn's result
sesh await <id> --timeout 5m                               # block until a turn finishes (mesh-aware)
sesh delegate --agent pi 'summarize this repo'             # spawn worker → ask → reply → archive
sesh delegate --agent claude 'run CI' --cwd ~/proj --keep  # leave the worker active instead of archiving
sesh subscribe <subscribee> --from <subscriber>            # pipe one thread's turns into another
sesh thread send --id <id> --text 'now' --respect-typing 0 # bypass the typing guard for this one paste
```

**The typing guard (every paste into a live pane).** A pane paste is
`paste-buffer` then Enter, so text delivered while a human is mid-line in that
pane is appended to their half-typed prompt and SUBMITTED with it — the live
case was child threads reporting into a supervisor its user was typing in. So
for terminal-delivered agents (Claude/Codex/shell), the owning daemon holds every thread-level delivery (`thread send`, `ticket
send-prompt`, subscription deliveries, scheduled messages) while the thread's
session has seen viewer INPUT within `[send] respect_typing` (default 60s —
tmux's `client_activity`, bumped by keystrokes through an attached client, not
by agent output), and pastes it once the pane has been quiet that long. What a
held delivery does is `--on-typing`: **`defer`** (the default — the daemon queues
it per thread, FIFO, prints `deferred <id>` and delivers later; a delivery still
held at `[send] respect_typing_deadline` (10m) FAILS loudly and auto-flags the
thread with `undelivered message from …` as the reason, never pasting anyway),
**`wait`** (block the command until the pane is quiet, bounded by `--timeout`;
the default under `--wait`), or **`skip`** (refuse with a non-zero exit, queue
nothing — for periodic senders). `--respect-typing <dur>` overrides the window
per call (`0` = paste now regardless), `--typing-deadline <dur>` the bound. A
detached session (nobody viewing) is quiet by definition. Held deliveries live
in daemon memory: a daemon restart drops them, loudly in its log. NB tmux counts
ANY key as input — PgUp while reading holds the guard like typing does.

**State authority.** A headful thread's busy/idle normally comes from a pane
content-diff heuristic, but pi and claude threads carry an in-agent reporter
(a pi extension / claude hooks, installed via myagent/myrig) that reports turn
starts/ends EXACTLY — the snapshot's `state_authority` field says which
mechanism decided (`reported` or `heuristic`; absent for headless threads).
Reporters use `sesh thread report-state` — a mechanism verb you normally never
type: stale `--seq` values are refused, and authority is dropped automatically
when the thread's pane dies. The reporter also passes the agent's live
`--agent-session` id every turn, which the daemon stamps onto the thread record
(schema 46) — so `resume`/reopen lands on the session claude/codex is ACTUALLY
in even after a compaction/rewind fork mints a new id, instead of relying on the
fragile leaf resolver. (**Background agents** — claude's `agents` feature — run
OUTSIDE the sesh pane, under claude's machine-global daemon, so sesh can't
resume a conversation while one holds it: `resume` surfaces claude's own
"currently running as a background agent" refusal, and you either attach via
`claude agents` or branch a copy. Worse, a bg process INHERITS `SESH_THREAD_ID`
from whatever started that daemon, so it reports under an unrelated thread's id;
the hook now reports nothing from a bg session, and the daemon independently
refuses any `--agent-session` whose transcript lives under a different working
directory than the thread's — a refusal logged loudly, keeping the stored id.
Before both guards a bg agent could write its conversation onto a stranger's
record, stranding the real thread on a stale transcript.) Two SYMMETRIC staleness bounds drop a report the
pane contradicts (loudly, in the daemon log, degrading to `heuristic` so it is
visible): a reported-BUSY on a pane byte-stable for 2 minutes (the lost-turn_end
class — claude's Stop hook does not fire on a user interrupt/Esc, which would
otherwise pin busy until the next prompt), and a reported-IDLE on a pane that
has been ANIMATING for 2 minutes (the reporter isn't tracking turns — a session
that predates the reporter hooks, or hooks that stopped firing — which would
otherwise mask a running turn as idle). Blocked (question/approval) reports are
exempt from the busy bound — those panes are legitimately static. codex threads stay heuristic for busy (no
turn-start surface), but their `notify` hook — wired into the codex config by
sesh at spawn — still reports turn ENDS for flagging, and since schema 46 that
report also carries codex's OWN session id, which the daemon stamps onto the
thread record (codex mints its id on its first turn — without this a headed
codex thread could not be forked while live, and reviving it fell back to a
cwd+time rollout guess that could land on a same-cwd sibling's conversation).

**Flags (`sesh thread flag`).** The flag is the "look at this thread" marker:
the daemon auto-flags when a turn ends or the agent stalls on a question /
approval prompt (claude's AskUserQuestion flags with the question as the
reason) — attended or not (no attended gate since 2026-07-25); nothing ever
auto-clears a flag.
`thread flag --off` clears; `--disable` suppresses auto-flagging for a thread
(parent-monitored children; also clears any current flag); `--enable`
re-allows it; `--on` flags manually AND re-enables a disabled thread (one
rule). Heuristic busy→idle edges flag only for agents opted in via `[flags]
heuristic_agents = ["codex"]` in config.toml (default: none — reporter edges
are exact, the heuristic can mistake your own typing-settle for a turn end).

### Headed Pi messages and explicit commands (API 53)

Pi `thread send` is literal RPC input: even `/compact` is a MESSAGE, not a command.
It preserves the editor draft, bypasses terminal typing guards, and steers after a
running tool completes. This applies to initial `--msg`, tickets, subscriptions and
schedules too. Missing/broken sockets fail loudly; there is **no paste fallback** and
no automatic retry after an uncertain delivery. `sent` acknowledges submission, not
model completion (`--wait` observes the turn as before).

Use `thread command --text '/compact [instructions]' --timeout 5m` for compaction;
it returns success only after Pi's completion callback, and reports errors such as
`Nothing to compact`. Also supported: `/model <pattern>`, `/thinking [level]`,
`/name [name]`, `/session` (structured session/context/model info), and registered
extension commands, templates and `/skill:<name>`. Resource dispatch reports
**submitted**, not completed: Pi's public API cannot report asynchronous resource
handler failures to RPC callers; inspect Pi's UI/transcript for those. No UI pickers,
auth, reload, or session-replacement commands (`/new`, `/resume`, `/fork`, etc.).

The command prints a request UUID before sending. After timeout/lost response,
`thread command --id <id> --request-id <uuid> --timeout 5m [--json]` polls without
re-executing. A timeout does not cancel; never blindly resend. Results expire an
hour after completion and are lost on Pi reload/exit. Commands require
pi-rpc-socket 0.2.0/protocol 2: if needed and authorized, update the extension and
manually run `/reload` in Pi — never reload automatically. Existing
0.1.0 runtimes already support ordinary message delivery; no reload is needed for
that. Transcript lookup also honours Pi's `PI_CODING_AGENT_DIR` override.
