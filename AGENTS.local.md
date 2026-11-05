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
- **Daemon-run harness commands go through `$SHELL -c`** (headless turns, model lists,
  hooks): the supervised daemon's own env has NO API keys — they come from `~/.zshenv`.
  A bare exec silently loses every keyed provider (pi's model list showed 2 of 6).
  Conformance cannot catch this: the sandbox daemon inherits the test shell's keys.
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
- **Thread env (schema 62, [THREAD_ENV](_dev/THREAD_ENV.md)):** values live in 0600 files
  (`<SESH_HOME>/thread-env/<id>/`), never in the store or a row: `env_count` only. Claude
  follows via a `FileChanged` hook (`sesh env hook`), pi via the extension's `env.ts`, codex
  follows through the last lines of myrig's `.zshenv`, which also make a thread's value beat
  `.zshenv`'s (`env set` asks the real `$SHELL`; a machine without them says restart-required).
  A shell ACTS on `$SESH_THREAD_ENV_FILE`, so it is on the launch line, never `tmux -e`. Hooks must write the `.` POINTER, never values (claude sources
  every hook file, so a dropped key returns stale), and removals must be `unset` until the
  next launch. Never put user values in `tmux -e` at new-session: they land in the SESSION
  env. Remote pi/codex
  cells need current harnesses on a Linux host: macOS and pocket4 fail the CONTROL cells too,
  and pi's extension loads from the checkout, so a tar'd tree does not test `env.ts`.
- **MCP arguments (schema 63, migration 30, [THREAD_MCP_ARGS](_dev/THREAD_MCP_ARGS.md)):**
  `launchArgs` takes the args because a spawn computes it before the row exists. Claude's
  `--mcp-config` replaces a same-named global entry whole, so a global `env` refuses the
  launch. Pi's entry is `"disabled": true` so the adapter never runs an argument-less copy.
  A claude or codex server that cannot start is silent (exit 0); pi headless exits 1.
  `integrations/pi` loads from `~/mysetup/sesh`, so a worktree's `mcp.ts` is not what runs.
- **MCP review fold (schema 64, migration 31):** Pi's `mcp.ts` follows `tools/list_changed`, retries a
  failed start, has no call limit unless `timeout` is set, and sends `notifications/cancelled`; a headless
  turn waits 45s for tools, then fails. A failed headless turn is stored as `threads.turn_error`, so `await`
  and `thread wait` exit non-zero and `status`/`info` show it. codex 0.166 ignores `tools/list_changed`
  (honest Skip). A failed claude/codex server stays silent: sesh does not read claude's `system/init`.
  Soliton's own `[key]` redaction rewrites any text containing a stub's one-letter key.
- **Conformance traps (2026-11-04):** `go test -run 'TestMatrix/(a/b|c/d)'` splits on `/` and runs ZERO
  cells; run a cell as `-run '^TestMatrix$/^a/b$'`. Moving mise's node `lts` (installing a newer node) breaks
  every pi/codex shim at once, and the suite shows ~240 sub-second failures, not a verdict. Cross-host tests
  use the partner's `~/.local/bin/sesh-v2`, NOT its `sesh`: refresh it or a PASS proves an old binary.
  `TestRealCrossHostHTTP` cannot pass as written: it spawns `--headless`, then waits for `Headful`.
  Pre-existing reds, also on b7acffc: `schedule.lifecycle`, `ticket.list-all/remote` (archive gate on a live
  pane), `thread.model/pi` (no google key). Intermittent on both: `model.list/pi`, `schedule.spawn/codex`.
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

Fleet on **2026-11-05 ~11:15Z**: mymain, ideapad, macstudio, macbook and termux run binary AND daemon
`410c4f7` (wire schema 64, store schema 31, env file on the launch line) on myrig `775b04c` (`.zshenv` sources
`$SESH_THREAD_ENV_FILE`) with refreshed skill copies, and every tmux session had the variable scrubbed from its
session env. **pocket4 was unreachable and stays on `0745362` (schema 63)**: pull sesh AND myrig, build, back up
(`VACUUM INTO`, it migrates), restart, run `tmux -L sesh set-environment -t <session> -u SESH_THREAD_ENV_FILE` on
each session; it runs codex 0.145 / pi 1.3, which fail the control cells, and its pi model list fails (privatemode's
podman proxy outlasts the 12s timeout). mymain's myrig keeps someone's `.gitconfig` edit and a stray `already` file.
macbook's `sesh-v2` is c75b837, the June build is beside it as `sesh-v2.e49b1e1`. `TestDoctorWarnsOnlyForTheMissingHarness`
fails wherever `mypi` is installed (ideapad), with or without this work. Re-check the fleet rather than repeating
any recorded status, here or in an archive.

Keep this file small. Put detailed findings in dated `_dev/` topic/history docs,
linked from the index; do not eagerly import those docs. This memory and its
archives are tracked; never move unrelated private notes into tracked docs.
