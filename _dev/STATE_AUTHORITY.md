# Authoritative agent turn-state reporting — design notes

*Status: design + mechanics extraction. Issues: #4 (state authority), #5 (blocked), #6 (done/seen). Origin:
the herdr assessment of 2026-07-23 (myvault pad "Herdr vs sesh - migration assessment", AGENTS.local.md H49).
This revises SPEC §3's runtime-state derivation: the pane content-diff probe becomes the FLOOR; an in-agent
reporter becomes the preferred authority for the busy axis when fresh.*

## Why (the bug lineage this kills)

busy/idle is currently a pane content-diff heuristic (≥2 content changes in 2s, sampled ~300ms). It
approximates a fact the agent knows exactly, and the approximation produced: the H24 scale stall (sweep
slower than the busy window ⇒ busy never latched), H36 lag amplification, H47 spurious notify toasts
(keystroke echoes + client-switch redraws latch busy; the settle after user interaction emits the
busy→idle edge hooks subscribe to), and H48's attachment/activity-age gating built to compensate.
An authoritative "turn started / turn ended" report makes the state exact and the notify gate trivial.

## Mechanics extracted from herdr (v0.7.5, src/integration/assets/)

herdr's layered model: screen-detection manifests as the floor; per-agent "integrations" (in-agent hooks
reporting over the local socket) as the upgrade; the server tracks which authority is active per pane and
integrations can RELEASE authority. Their per-agent mechanics, verbatim from the shipped assets:

- **pi** (`pi/herdr-agent-state.ts`, a pi EXTENSION):
  - Events used: `session_start` (gated on `ctx.hasUI === true` = root interactive session only),
    `agent_start` → working, `agent_settled` + `ctx.isIdle() === true` double-check → idle (the settled
    check prevents transient message boundaries publishing idle mid-turn — their #943/#1189 fix),
    `session_shutdown`.
  - **Release-authority footgun**: pi tears down + rebinds extension runtimes on `/reload`, `/new`,
    `/resume`, `/fork` WITHOUT the agent process exiting — only `session_shutdown.reason === "quit"` may
    release authority, else the replacement runtime's reports get suppressed.
  - Reload mid-turn: `session_start` re-derives activity from `ctx.isIdle() === false` (an `agent_start`
    may never re-fire).
  - blocked: an event (`herdr:blocked` with active/label, from a sibling extension) maintains a COUNTER
    (nested asks), not a boolean.
  - Session identity: `ctx.sessionManager.getSessionFile()` (preferred, absolute path) /
    `.getSessionId()`, re-reported on session_start AND agent_start (native session replacement — their
    #943 "stale saved session references" fix).
  - Wire discipline: per-report monotonic `seq`; a serialized in-flight queue (never interleave writes);
    state dedupe (skip if state+message unchanged); send retry once with a longer timeout; everything
    fail-silent toward the agent (a reporting failure must never break the agent).
- **claude** (`claude/herdr-agent-state.sh`, a Claude Code HOOK script):
  - herdr only harvests SESSION IDENTITY from claude hooks (`session_id`, `transcript_path` from the
    hook's stdin JSON) — claude STATE stays on their screen detection.
  - Subtleties they encode: skip subagent hook invocations (`agent_id` present in hook input); NEVER map
    `SubagentStop` to working (claude recap/away-summary can emit it after the main turn stopped).
- **codex**: no integration asset — screen/OSC-title detection only. Its `notify` config can give
  turn-end externally.

## sesh design sketch (decisions to confer marked ⚑)

- **Ingestion**: `POST /v1/threads/report-state` on the OWNING daemon:
  `{thread_id, source, event: turn_started|turn_ended|blocked|unblocked|release, seq, agent_session_id?,
  agent_session_path?, message?}`. Thread-keyed (SESH_THREAD_ID is already in every spawned pane's env —
  simpler than herdr's pane-keyed model since sesh threads are the identity). Unknown thread / stale seq /
  malformed ⇒ loud 4xx. Reports land in an IN-MEMORY per-thread authority map (runtime state is re-derived,
  never persisted — SPEC §2; a daemon restart falls back to the heuristic floor until the next report).
- **Resolution**: maintainer's refreshThread prefers reported activity when the authority entry is live;
  content-diff remains the floor for uninstrumented threads. Authority is bounded by runtime liveness:
  pane death / headless transition CLEARS the entry (a crashed agent can't pin busy). ⚑ whether an
  additional max-turn-age guard is wanted, or pane-liveness bounding suffices.
- **Visibility**: ThreadSnapshot/ThreadRow gain `state_authority: reported|heuristic` (additive, omitempty,
  api schema bump; mixed-mesh safe). The TUI/hooks can then see WHICH authority produced busy — silent
  degradation is forbidden.
- **Per-agent reporters** (the policy/provisioning split ⚑ — confer):
  - pi: a sesh-owned pi extension (mirroring herdr's event usage above). ⚑ where it lives and how it's
    provisioned: sesh spawn-time (like EnsureCodexTrust) vs myagent-owned config.
  - claude: hooks.json is myrig/myagent policy; sesh ships the endpoint + a reporting script. Stop hook =
    turn_ended, UserPromptSubmit = turn_started, Notification = blocked candidate (issue #5). Must skip
    subagent invocations (agent_id) per the herdr footnote.
  - codex: `notify` config = turn_ended only; ⚑ whether turn_started is derivable (likely stays heuristic;
    document honestly, no faking).
- **Matrix**: new feature (e.g. `thread.state-authority`) × (local, remote) × agents-with-reporters. Honest
  cells: a REAL agent turn drives reported busy↔idle both directions; killing/silencing the reporter must
  flip `state_authority` back to `heuristic` (assert the authority field, not just the state). codex axes
  declared per what's honestly reportable, N/A justified where not.
- **Sequencing**: #4 substrate+pi first (richest events, sesh already speaks pi RPC for adopt), then claude,
  then #5 blocked (needs a design call on extending the activity enum vs a separate axis), then #6 done/seen
  (consumes #4's turn-end + H48's attachment ages). #7 (send --wait) rides the same eventer edges. #8 (UI
  sidebar) LAST, design-discussion-first.
