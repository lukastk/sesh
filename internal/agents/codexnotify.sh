#!/bin/sh
# sesh codex-notify reporter (embedded in the sesh binary; materialized at
# <SESH_HOME>/codex-notify.sh and wired into the codex config's `notify` key
# by the daemon at spawn — see internal/agents/codexnotify.go).
#
# codex invokes it with a JSON payload as the last argument; the only type is
# currently agent-turn-complete, but the filter keeps future types from
# misreporting. Reports the NO-AUTHORITY turn-end (codex cannot report turn
# starts, and one-directional busy authority would pin idle through real
# turns) — the daemon evaluates auto-flagging from it. Self-gating: outside a
# sesh thread (no SESH_THREAD_ID) it does nothing; failures are silent toward
# codex, and stay visible daemon-side as the thread simply not flagging.

[ -n "${SESH_THREAD_ID:-}" ] || exit 0

for arg in "$@"; do payload="$arg"; done
case "${payload:-}" in
  *agent-turn-complete*) ;;
  *) exit 0 ;;
esac

SESH_CMD="${SESH_BIN:-sesh}"
command -v "$SESH_CMD" >/dev/null 2>&1 || exit 0
"$SESH_CMD" thread report-state --id "$SESH_THREAD_ID" --source sesh:codex-notify --event turn_ended_no_authority >/dev/null 2>&1 || true
exit 0
