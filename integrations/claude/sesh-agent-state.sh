#!/bin/sh
# sesh-agent-state — Claude Code hook reporting turn lifecycle to the sesh
# daemon, giving sesh EXACT busy/idle for claude threads instead of the pane
# content-diff heuristic (schema 43, issue #4 — see _dev/STATE_AUTHORITY.md).
#
# Registration (policy, lives in myrig's claude settings): run this script for
# the UserPromptSubmit and Stop hook events. Hook input arrives as JSON on
# stdin (hook_event_name, session_id, ...).
#
# Self-gating: outside a sesh thread (no SESH_THREAD_ID in the environment —
# claude sessions sesh spawned or register-then-exec'd have it) this does
# nothing. Subagent hook invocations (agent_id present) are skipped — only the
# MAIN conversation's turns are thread state. SubagentStop is never mapped:
# claude recap/away-summary turns can emit it after the main turn already
# stopped (the herdr claude-integration lesson).
#
# ALWAYS exits 0: a nonzero Stop hook would block claude from stopping, and a
# reporting failure must never break the agent. The daemon side stays honest —
# a thread with no live reports remains on the heuristic floor, visibly
# (state_authority=heuristic).

# $SESH_BIN = the spawning daemon's own binary (pane PATH may hold an older
# installed sesh); PATH fallback only when absent (adopted/pre-43 spawns).
[ -n "${SESH_THREAD_ID:-}" ] || exit 0
SESH_CMD="${SESH_BIN:-sesh}"
command -v "$SESH_CMD" >/dev/null 2>&1 || exit 0
command -v python3 >/dev/null 2>&1 || exit 0

input="$(cat 2>/dev/null || true)"

out="$(printf '%s' "$input" | python3 -c '
import json, sys

try:
    h = json.load(sys.stdin)
except Exception:
    sys.exit(0)
if h.get("agent_id"):  # subagent invocation, not the main turn
    sys.exit(0)
name = h.get("hook_event_name", "")
if name == "Notification":
    # Notification carries BOTH permission requests and idle reminders
    # ("Claude is waiting for your input"); only a permission request is the
    # blocked state (mid-turn, stalled on the human). Evidence-based message
    # match — the only signal claude exposes here.
    msg = str(h.get("message", "") or "")
    if "permission" in msg.lower():
        print("blocked\t" + msg.replace("\n", " ")[:200])
    sys.exit(0)
# PostToolUse = a tool completed, i.e. any permission stall resolved and the
# turn is running again.
print({"UserPromptSubmit": "turn_started", "Stop": "turn_ended", "PostToolUse": "unblocked"}.get(name, ""))
' 2>/dev/null || true)"

event="${out%%$(printf '\t')*}"
reason="${out#*$(printf '\t')}"
[ "$reason" = "$out" ] && reason=""

[ -n "$event" ] || exit 0
if [ -n "$reason" ]; then
	"$SESH_CMD" thread report-state --id "$SESH_THREAD_ID" --source sesh:claude-hook --event "$event" --reason "$reason" >/dev/null 2>&1 || true
else
	"$SESH_CMD" thread report-state --id "$SESH_THREAD_ID" --source sesh:claude-hook --event "$event" >/dev/null 2>&1 || true
fi
exit 0
