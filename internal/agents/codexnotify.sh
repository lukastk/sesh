#!/bin/sh
# sesh codex-notify reporter (embedded in the sesh binary; materialized at
# <SESH_HOME>/codex-notify.sh and wired into the codex config's `notify` key
# by the daemon — see internal/agents/codexnotify.go).
#
# codex invokes it with a JSON payload as the last argument; the only type is
# currently agent-turn-complete, but the filter keeps future types from
# misreporting. Reports the NO-AUTHORITY turn-end (codex cannot report turn
# starts, and one-directional busy authority would pin idle through real
# turns) — the daemon evaluates auto-flagging from it. The payload's
# "thread-id" is codex's OWN session (rollout) id: it rides along as
# --agent-session so the daemon can stamp the thread record (schema 46,
# ticket 49d4299b — headed codex threads otherwise never capture their
# late-minted id, which broke fork and made revive discovery ambiguous).
# Self-gating: outside a sesh thread (no SESH_THREAD_ID) it does nothing;
# failures are silent toward codex, and stay visible daemon-side as the
# thread simply not flagging.

[ -n "${SESH_THREAD_ID:-}" ] || exit 0

for arg in "$@"; do payload="$arg"; done
case "${payload:-}" in
  *agent-turn-complete*) ;;
  *) exit 0 ;;
esac

# codex's session id, e.g. "thread-id":"019f9bb9-...". Extracted with sed (no
# jq dependency); the payload is only ever expanded as a quoted argv value,
# never re-parsed by a shell. Absent (an older codex) => reported without it.
sid=$(printf '%s' "$payload" | sed -n 's/.*"thread-id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

SESH_CMD="${SESH_BIN:-sesh}"
command -v "$SESH_CMD" >/dev/null 2>&1 || exit 0
if [ -n "$sid" ]; then
  "$SESH_CMD" thread report-state --id "$SESH_THREAD_ID" --source sesh:codex-notify --event turn_ended_no_authority --agent-session "$sid" >/dev/null 2>&1 || true
else
  "$SESH_CMD" thread report-state --id "$SESH_THREAD_ID" --source sesh:codex-notify --event turn_ended_no_authority >/dev/null 2>&1 || true
fi
exit 0
