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

Last recorded open leads (**2026-10-05**, H121; not reverified): Pi symlink-CWD
transcript lookup (`/tmp` vs `/private/tmp`), two nonreproducing Codex full-run
failures, and a development SDK bundled audit finding. H121's five-machine deploy
left pocket4 pending **then**; re-check rather than repeating that status.

Keep this file small. Put detailed findings in dated `_dev/` topic/history docs,
linked from the index; do not eagerly import those docs. This memory and its
archives are tracked; never move unrelated private notes into tracked docs.
