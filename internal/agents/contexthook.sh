#!/bin/sh
# sesh context hook — claude/codex SessionStart + UserPromptSubmit hook that
# injects the sesh thread's custom context (schema 60; api/context.go).
#
# Materialized by the sesh daemon at <SESH_HOME>/context-hook.sh and passed to
# every claude (--settings) and codex (-c hooks.*) the daemon launches; $1 names
# the harness. All logic lives in `sesh context hook`, which verifies the
# thread identity, asks the owning daemon what to inject, and prints the hook
# output. This file only finds the right sesh binary: $SESH_BIN is the launching
# daemon's own (a pane's PATH can hold an older install).
#
# Outside a sesh thread (no SESH_THREAD_ID) it does nothing. The path and the
# command string stay fixed across sesh versions: codex trusts a hook by a hash
# of its declaration, so a changing command would need re-trusting.
[ -n "${SESH_THREAD_ID:-}" ] || exit 0
exec "${SESH_BIN:-sesh}" context hook --agent "$1"
