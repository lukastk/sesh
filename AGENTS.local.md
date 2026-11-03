# sesh — current traps and memory index

History is **on demand**: [_dev/ENGINEERING_INDEX.md](_dev/ENGINEERING_INDEX.md)
routes H1–H121, including corrections and dated deployment evidence. Never treat an
archive's “LIVE”, “pending”, or “all green” as today's fleet/test status.

- **Which-client law (H98/H117):** carry the pressing client from the binding.
  `display-message -c` chooses where to print, not whose context to expand.
  Iterate `list-clients -F`; name the session in targets (`master:=<machine>`).
- **Identity (H92/H112–114):** inherited `SESH_THREAD_ID` can name a stranger.
  Use `sesh whoami` as the verified gate, not `info` as proof. An unresolved
  identity does not establish that the caller is not a thread. Read IDENTITY first.
- **Delivery (H121):** Pi messages are literal RPC steering, commands explicit;
  no paste fallback, auto-reload, or blind retry after uncertain ACKs. Submission
  is not completion. See PI_DELIVERY before changing any send site.
- **Tests (H91/H113/H118):** a filtered `go test` can run zero tests and succeed;
  inspect `-v`. A compile failure is not a discriminating regression. Sandbox-only
  PIDs/sockets; no `pkill -f`, widened live kill matchers, or default homes.
- **Schedules (schema 54):** a schedule's lifecycle is `state`
  (active/paused/spent/broken/archived), the only firing gate; `enabled` +
  `disabled_reason` remain as written-but-unread mirrors for rollback. `schedule
  list` defaults to the live states and prints what it held back — a test or script
  expecting every row needs `--all`. Nothing prunes records automatically. See
  [SCHEDULING §9.6](_dev/SCHEDULING.md).
- **Deploy (H70/H112):** sidebars keep old binaries/config; installed skill copies
  may stay stale after a repo pull. Verify actual artifacts, not just checkout SHA.
  Termux requires plain native `go build` (CGO=1); see OPERATIONS before deploying.
- **`--machine` is two mechanisms (#17):** ssh re-execs on the peer; **http keeps the
  command in this process** and only redirects daemonClient through the local daemon's
  route proxy. So client-side FILESYSTEM work must sit in `httpRoutable`'s carve-out
  (`backup`/`restore`/`copy`/`import` do now) or it resolves the target's paths on the
  caller. `blob put` / `schedule --prompt-file` belong on http on purpose: they send
  local BYTES to the peer's daemon. Every peer with an `api_addr` takes the http path,
  so an ssh-only-era test proves nothing about today.
- **Archive gate (schema 57):** `thread archive` refuses a thread that is still
  flagged or still running, naming every unmet condition; `--force` overrides, and
  the by-contract archivers (a schedule's `--on-turn-end`, `delegate`'s worker) pass
  it. Un-archiving is ungated. Archived-but-headful (H40) still exists through those
  paths, so the default view's flagged/running/headful exception stays load-bearing.
  The gate lives in the daemon: it arrives with a restart, not with a new client.
- **Models (schema 58):** a thread has a REGISTERED model (`Thread.Model`, what sesh
  launches with) and an OBSERVED one (owner-read from the transcript, never stored).
  Never `/model` a live claude pane: Claude Code's typed `/model <m>` writes `"model"`
  into the GLOBAL `~/.claude/settings.json` — `switch-model` restarts the pane in place
  instead. Defaults live in `[spawn.<agent>] model`, written by a key-level text edit
  that goes THROUGH the symlinked config.toml (see the config bullet below). Pi
  cannot run `anthropic/*` turns on this account ("Third-party apps now draw from your
  extra usage"), so tests drive pi on `openai-codex/*`.
- **Thread context (schema 61, [THREAD_CONTEXT](_dev/THREAD_CONTEXT.md)):** claude =
  per-thread `<SESH_HOME>/thread-context/<id>/CLAUDE.md` (`--add-dir`, a copy of the
  store) + a hook for mid-session changes; codex = hooks only, trusted in its config
  (hash from `codex app-server hooks/list`, never computed; a changed hook command = a
  refused codex launch), re-sent at a headed `SessionStart(resume)`, which a rewind fires;
  pi = `integrations/pi/sesh-agent-state/context.ts`, live from THIS checkout (myagent
  symlink) the moment it is saved. Launch flags cannot carry it: claude/codex freeze them.
  An injection can be REWOUND away, so "delivered" never proves the model has it. Claude
  surfaces only the FIRST mid-session edit of an instruction file. Daemon log =
  `supervisorctl tail`, not `~/.sesh/daemon.log`.
- **Config layout (schema 59, 2026-11-02):** `~/.sesh/config.toml` is a SYMLINK to myrig's
  plain `home/.sesh/config.toml` (shared by the fleet); `~/.sesh/config.local.toml` is
  rendered per machine from `config.local.toml.jinja` and OVERLAYS it (tables merge key
  by key; arrays and arrays of tables REPLACE). Never put a shared list's entries in the
  overlay — it would drop the shared ones. Apply with install-home `--only-source` for
  each file (termux has no uv: run `python3 scripts/install-home.py`), install a sesh
  that reads the overlay FIRST, then restart the daemon.

Last recorded open leads (**2026-10-05**, H121; not reverified): Pi symlink-CWD
transcript lookup (`/tmp` vs `/private/tmp`), two nonreproducing Codex full-run
failures, and a development SDK bundled audit finding.

Fleet on **2026-11-03 21:15Z**: all six machines (mymain, macbook, macstudio, ideapad, termux, pocket4)
run binary AND daemon `65bd00c` (wire schema 60, store schema 28, thread context) on the split config with
myrig `b52b5d4` (CTX column) and refreshed skill copies; pre-60 DB backups are in each `~/.sesh/backups/sesh-pre60-*`.
Live smoke on mymain's supervised daemon: claude/codex/pi headless turns followed a context. macbook's `pi`
shim has no mise Node version (`pi` itself fails there). Re-check the fleet rather
than repeating any recorded status, here or in an archive.

Keep this file small. Put detailed findings in dated `_dev/` topic/history docs,
linked from the index; do not eagerly import those docs. This memory and its
archives are tracked; never move unrelated private notes into tracked docs.
