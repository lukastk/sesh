#!/bin/sh
# sesh-agent-state — Claude Code hook reporting turn lifecycle to the sesh
# daemon, giving sesh EXACT busy/idle for claude threads instead of the pane
# content-diff heuristic (schema 43, issue #4 — see _dev/STATE_AUTHORITY.md).
#
# Registration (policy, lives in myrig's claude settings): run this script for
# the UserPromptSubmit / Stop / PostToolUse / PreToolUse / Notification hook
# events. Hook input arrives as JSON on stdin (hook_event_name, session_id, ...).
#
# It also carries claude's live `session_id` as --agent-session so the daemon
# stamps it onto the thread record every turn (schema 46). claude mints a NEW
# session id on compaction/rewind, so the record's spawn-time id drifts stale;
# stamping the live id each turn keeps `thread resume`/reopen landing on the
# session claude is ACTUALLY in, instead of relying on the fragile leaf resolver.
#
# Self-gating: outside a sesh thread (no SESH_THREAD_ID in the environment —
# claude sessions sesh spawned or register-then-exec'd have it) this does
# nothing. Subagent hook invocations (agent_id present) are skipped — only the
# MAIN conversation's turns are thread state (and only its session id is
# stamped). SubagentStop is never mapped: claude recap/away-summary turns can
# emit it after the main turn already stopped (the herdr claude-integration lesson).
# BACKGROUND sessions are skipped for the same reason, see below.
#
# ALWAYS exits 0: a nonzero Stop hook would block claude from stopping, and a
# reporting failure must never break the agent. The daemon side stays honest —
# a thread with no live reports remains on the heuristic floor, visibly
# (state_authority=heuristic).

# $SESH_BIN = the spawning daemon's own binary (pane PATH may hold an older
# installed sesh); PATH fallback only when absent (adopted/pre-43 spawns).
[ -n "${SESH_THREAD_ID:-}" ] || exit 0

# A BACKGROUND agent (claude's `agents` feature) does NOT run in this thread's
# tmux pane — it runs under claude's machine-global `claude daemon run`, from a
# pool of pre-forked spare processes. That daemon is started by whichever claude
# happens to need it first and INHERITS that process's environment, so every
# spare carries the SESH_THREAD_ID of an unrelated thread — whichever sesh
# thread's agent started the daemon. Unix env is frozen at exec, so it can never
# be corrected. A bg session therefore reports under a FOREIGN thread id, making
# all of its facts wrong: its busy/idle marks the wrong thread, and its
# --agent-session stamps the wrong record. Report nothing (same reasoning as the
# subagent guard).
#
# This is not hypothetical: on 2026-08-05 a bg agent hosting thread bd2d0b3c's
# conversation stamped that conversation's session id onto thread 86304b66's
# record, leaving bd2d0b3c pointing at a transcript frozen hours earlier and
# unable to resume ("Session … is currently running as a background agent").
# The daemon carries an independent backstop (reportstate.go) in case this env
# var is ever renamed — do not rely on this guard alone.
[ "${CLAUDE_CODE_SESSION_KIND:-}" != "bg" ] || exit 0

SESH_CMD="${SESH_BIN:-sesh}"
command -v "$SESH_CMD" >/dev/null 2>&1 || exit 0
command -v python3 >/dev/null 2>&1 || exit 0

input="$(cat 2>/dev/null || true)"

# Emit TAB-separated: <event>\t<reason>\t<session_id>\t<background JSON>.
# Empty event => no report. The background field is set only for Stop, and only
# when claude sent `background_tasks` (older claudes do not: then the field is
# empty and the daemon records "not reported", never "nothing running").
out="$(printf '%s' "$input" | python3 -c '
import json, sys

try:
    h = json.load(sys.stdin)
except Exception:
    sys.exit(0)
if h.get("agent_id"):  # subagent invocation, not the main turn
    sys.exit(0)
sid = str(h.get("session_id", "") or "").replace("\t", " ").replace("\n", " ")
name = h.get("hook_event_name", "")
event = ""
reason = ""
if name == "PreToolUse":
    # AskUserQuestion = the agent is stalled on a QUESTION (verified live:
    # PreToolUse fires with the full question JSON exactly when the prompt
    # shows). Report blocked with the actual question as the reason — it
    # becomes the flag reason/toast text. Other tools are not stalls.
    if h.get("tool_name") == "AskUserQuestion":
        q = ""
        try:
            qs = (h.get("tool_input") or {}).get("questions") or []
            q = str(qs[0].get("question", "")) if qs else ""
        except Exception:
            q = ""
        event = "blocked"
        reason = (q or "agent asked a question")
elif name == "Notification":
    # Notification carries permission requests, question prompts AND idle
    # reminders ("Claude is waiting for your input"); the permission match
    # covers the approval prompts (and backstops AskUserQuestion, whose message
    # also says "needs your permission"). Evidence-based message match — the
    # only signal claude exposes here.
    msg = str(h.get("message", "") or "")
    if "permission" in msg.lower():
        event = "blocked"
        reason = msg
else:
    # PostToolUse = a tool completed, i.e. any permission/question stall
    # resolved and the turn is running again.
    event = {"UserPromptSubmit": "turn_started", "Stop": "turn_ended", "PostToolUse": "unblocked"}.get(name, "")
if not event:
    sys.exit(0)
# Stop carries the in-flight background work (issue #24): background shells,
# Monitors, background subagents, MCP tasks and workflows in background_tasks,
# in-session wake-ups (ScheduleWakeup, CronCreate, /loop) in session_crons.
# claude wakes the session itself when a task finishes (UserPromptSubmit with a
# <task-notification> prompt), and the next Stop carries the updated list.
bg = ""
if name == "Stop" and isinstance(h.get("background_tasks"), list):
    def clip(v, n=160):
        return str(v if v is not None else "")[:n]
    tasks = []
    for t in h.get("background_tasks")[:50]:
        if not isinstance(t, dict):
            continue
        desc = t.get("command") or t.get("description") or t.get("name") or ""
        tasks.append({"id": clip(t.get("id")), "type": clip(t.get("type"), 40),
                      "status": clip(t.get("status"), 40), "description": clip(desc)})
    wakeups = []
    crons = h.get("session_crons")
    for c in (crons if isinstance(crons, list) else [])[:50]:
        if not isinstance(c, dict):
            continue
        wakeups.append({"id": clip(c.get("id")), "schedule": clip(c.get("schedule"), 80),
                        "recurring": bool(c.get("recurring"))})
    bg = json.dumps({"tasks": tasks, "wakeups": wakeups}, separators=(",", ":"))
reason = reason.replace("\t", " ").replace("\n", " ")[:200]
print(event + "\t" + reason + "\t" + sid + "\t" + bg)
' 2>/dev/null || true)"

event="$(printf '%s' "$out" | cut -f1)"
reason="$(printf '%s' "$out" | cut -f2)"
sid="$(printf '%s' "$out" | cut -f3)"
bg="$(printf '%s' "$out" | cut -f4)"

[ -n "$event" ] || exit 0
# Build argv so the optional --reason / --agent-session are only passed when set.
set -- --id "$SESH_THREAD_ID" --source sesh:claude-hook --event "$event"
[ -n "$reason" ] && set -- "$@" --reason "$reason"
[ -n "$sid" ] && set -- "$@" --agent-session "$sid"
[ -n "$bg" ] && set -- "$@" --background-json "$bg"
"$SESH_CMD" thread report-state "$@" >/dev/null 2>&1 || true
exit 0
