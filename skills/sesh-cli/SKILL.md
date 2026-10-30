---
name: sesh-cli
description: Use the `sesh` CLI/TUI to list, find, enter, resume, create, tag, archive, rename, reparent, capture, send-to, delegate, or inspect coding-agent threads across machines. Use when the user asks about sesh command usage, the thread TUI, entering/resuming a thread, cross-machine thread state, the sesh daemon, peers, mycockpit (the cross-machine tmux cockpit), or tickets.
---

# sesh

Use sesh, the multi-machine thread manager: one Go binary + daemon per machine.
Agent identity is a **pane marker**, not a tmux session (sessions may host siblings).
Records survive runtime loss. `sesh` owns mechanism; myrig owns policy/shell UX.
Prefer `sesh help-tree` and `sesh <group> <verb> --help` to guessing; use `--json`
for scripts. Paths below resolve from **this installed skill directory**, not CWD.
Read only the reference for the task, not the whole manual.

## Mandatory authority and safety

- **Verify yourself:** `TID=$(sesh whoami) || exit 1`. Daemon-launched headless or
  scheduled workers use `TID=$("$SESH_BIN" whoami) || exit 1`. This gate accepts
  pane/turn or corroborated harness provenance; inherited `SESH_THREAD_ID` alone
  is **unverified** and can name unrelated work. `info` is diagnostic, not proof.
  Never discard stderr or use `--allow-unverified` to bypass a self-action gate.
  If refused, read its lead and corroborate via `info` + pane capture; unresolved
  does **not** mean “not a thread”. Never borrow an id for signing/attaching work.
- **Explicit targets:** scripts should name their intended thread. `stop` and
  `delete` require explicit nonempty `--id`; other verbs may infer only if the
  selector is omitted, never explicitly empty. Prefixes must be unambiguous.
  `stop` ends runtime but keeps the conversation; `delete` drops the record,
  promotes children, and refuses live runtime unless `--force` (which orphans it).
  Shell-thread stop kills the **whole session** and requires force if agents live
  inside. Archive parks a record without stopping work. Think before all mutations.
- **Parenting is deliberate:** `thread new` normally infers a child of the caller.
  A requested child must use your verified id as `--parent`; standalone work MUST
  use `--no-parent`. Parenting requires the same owner machine; do not pretend a
  routed root is a cross-machine child. For ordinary `thread new`, **omit `--agent`
  unless the user requested a harness**; the owner applies `[defaults] agent
  (unset fails loudly; `--fork-from` inherits the source harness).
- **Schedules:** **archive one you are finished with** (`schedule archive`), do not
  leave it paused — `paused` means "coming back" and stays in the default listing,
  so a paused pile buries the live schedules. Archiving keeps the record and its
  run history; `remove`/`prune` throw them away. This applies to the one-shot
  backstops a thread sets for itself: when the work lands early, archive the
  backstop. Archiving a THREAD archives the active message schedules aimed at it.
- **Route for real:** `--machine <m>` uses SSH or peer HTTP, not a cosmetic field;
  HTTP routing needs the local daemon. Check owner/reachability with `mesh`/`info`.
  The TCP API never exposes generic `/v1/route`. Cross-peer browser operations require
  explicit `[browser_hub]` opt-in plus a trusted HTTP-peer allowlist; otherwise browser
  endpoints are local-owner-only. Browser Fetch access additionally requires an exact
  `[browser_api].allowed_origins` entry; it defaults empty and grants CORS only to fixed
  browser/ticket routes. Preflight does not weaken bearer authentication; generic, Unix-only,
  and runtime routes never receive CORS headers. Quote owner-home paths: `--cwd '~/proj'` reaches
  the remote home; unquoted `~` expands in the caller's shell. Relative paths expand locally.
  Browser terminal tickets are exact URLs: do not append dimensions. Open at 120×32 and
  send the authenticated terminal-v1 resize control frame immediately after connection.
  Stopped Pi semantic history is owner-routed through Pi's metadata-only history protocol;
  browser callers never provide session paths, and older Pi fails explicitly with no raw fallback.
  Stopped Pi artifact metadata and inline bytes use separate owner-routed native artifact
  CLIs: metadata remains distinct from history v1, and whole bytes are buffered and verified
  before HTTP success. Browser callers provide only thread/publication IDs, never paths.
- Canonical stopped input media is separate again: browser thread pages advertise
	`browser.stored-input-media-v1`, presentation and metadata preserve Pi's own protocols and
	cursors, and stopped GET/HEAD bytes use only the native session-file CLI. The distinct
	`browser.stored-media-presentation-v2` token gates `GET /v1/browser/threads/media-presentation`,
	which invokes only `pi.session-presentation` version 2 with the fixed native version flag and
	returns canonical input plus direct tool-result output references. The input-only presentation
	route remains version 1. Unsupported or malformed v2 owners fail explicitly; Sesh never retries
	v1, revives the agent, or switches to a live source. Output bytes remain on the existing verified
	artifact metadata/byte and managed-download routes. The additive `browser.live-input-media-v1`
	token means the input-image byte URL can route an active Pi runtime only through its native-owned
	`.input.sock` bridge. Sesh resolves the exact atomic marker target plus `input_image_reader`
	listener generation, sends neither to the browser, buffers and independently verifies the whole
	private response, and revalidates owner/session/runtime/target/generation before success. Active
	reads never fall back to stopped files; stopped reads never use the bridge. All media responses
	are private/no-store; input bytes reject ranges and preserve legacy computed-only digest semantics.
	`pi.session-history` v1 remains unchanged, and socket image submission stays disabled until joined activation.
  Browser attachments use bounded owner-routed HTTP chunks and exact-target opaque Pi leases;
  attachment names are UTF-8 basenames and reject literal `.` and `..`. Transfer-capable Sesh
  launches replace the all-or-none `SESH_PI_TRANSFER_THREAD_ID`, `SESH_PI_TRANSFER_INBOX`, and
  `SESH_PI_TRANSFER_API_SOCKET` triple from one authoritative thread UUID rather than inherited
  `SESH_THREAD_ID`. They never reuse legacy blob/stage semantics, browser paths, or WebSocket
  binary frames. Managed output downloads require explicit Unix-local artifact publication,
  never path scraping.
  Owner and hub GET/HEAD/range responses impose exact `Cache-Control: private, no-store`; this
  download policy does not depend on an `Origin` header and never trusts a peer cache directive.
  Native output publications use a separate Sesh-owned durable mapping: Unix-local producers
  resolve one authoritative `(thread, native session, publication)` key, upload or fail it, and
  authenticated browsers batch-query only those IDs through the owner. Mapping state is monotonic;
  same-session runtime rotation may finish the original immutable artifact target, while an older
  session is refused. Current lifecycle operations never move one thread UUID between owners.
- **Delivery:** live Pi `thread send` is **literal RPC steering**, even `/compact`;
  it preserves drafts and waits for running tools. Commands use `thread command`.
  No paste fallback, automatic reload, or blind retry after uncertain ACKs.
  `sent`/resource `submitted` is not completion. Recover commands by request UUID.
  Claude/Codex/shell pastes use the viewer-typing guard (60s default): defer/wait/
  skip, bounded, never paste on expiry. `--respect-typing 0` intentionally bypasses
  it; do not casually disable it. Deferred queues are lost on daemon restart.
- **Live rig:** never hand-restart supervised production daemons; use
  `supervisorctl restart sesh-daemon` when authorized, preserving service env/API.
  Termux is the explicit-PID/fresh-login exception; read the fleet reference first.
  Tests must strip inherited `SESH_*` and isolate `SESH_HOME`, `SESH_TMUX_SOCKET`,
  `SESH_MASTER_SOCKET`, `SESH_CODEX_HOME`. Use absolute sandbox binary paths;
  myrig wrappers may point to production. No default live homes/sockets, broad
  process kills, or test viewers attached to user sessions (resize risk).
- **Tickets:** **never create one unless Lukas explicitly asked.** An unasked-for
  ticket lands in his vault as a note he must consolidate by hand; steer sub-threads
  with `thread send`/`delegate` and record findings where the work lives. Read
  assigned prompts, keep active until ready, then finish with `done` **and a markdown
  note** explaining changes and commit SHA. Abandoned work is `dropped` with a reason,
  never silently closed. Verify routing/binding.

## Common operations

Replace `<…>` placeholders; creation/sending/lifecycle commands mutate real work.

```sh
sesh thread list --all-machines --json
sesh info <id> --json
sesh thread capture --id <id> --lines 80
sesh tail <id> -n 50
sesh tui --all-machines
sesh thread new --name subtask --cwd . --parent "$TID"
sesh thread new --name independent --cwd '~/proj' --machine macbook --no-parent
sesh thread resume --id <id>
sesh thread send --id <id> --text 'Continue the approved task' --wait --timeout 5m
sesh thread send-headless --id <id> --text 'Summarize'   # idle thread, no pane
sesh thread command --id <pi-id> --text '/compact retain the plan' --timeout 5m
sesh thread command --id <pi-id> --request-id <uuid> --timeout 5m  # poll, no resend
sesh subscribe <child-id> --from "$TID"                # completed turns into parent
sesh ticket list --current                            # assigned ticket prompts are work
sesh ticket get --id <ticket-id> --field prompt
sesh ticket set-status --id <ticket-id> --status done --note 'Changes; closed by <sha>'
sesh doctor
sesh mesh
```

TUI: `p` command palette, `?` keys, `/` filter, Tab view picker, Enter navigate/revive,
`I` details, `K` tickets, `S` shells; `a` archives **instantly**, `U` undoes archive.
Sidebar q/Esc dismiss errors, not quit; Ctrl-C always quits. Headful/headless is
independent of busy/idle; flags require explicit clearing, holds hide even flagged
work from `active`. Offline rows are hidden by default, not deleted.

## Task → bundled reference

| Task | Read |
|---|---|
| Identity refusal, selectors, scheduled-worker provenance | [identity](references/identity.md) |
| State glyphs, shell/virtual threads, trust, ownership | [concepts](references/concepts.md) |
| Create/adopt/fork, lifecycle, held Claude sessions, navigation | [threads](references/threads.md) |
| Send/wait/delegate/subscribe, Pi commands, typing and state authority | [delivery](references/delivery.md) |
| Ticket lifecycle/routing/reporting, blob tokens/files | [tickets and blobs](references/tickets-and-blobs.md) |
| TUI scope, palette/keymap, parent picker, tickets/columns | [TUI](references/tui.md) |
| Persistent/traveling sidebar and focus tracking | [sidebar](references/sidebar.md) |
| Hold/release inheritance, pin/divider, archive/offline views | [views and holds](references/views-and-holds.md) |
| Cron/interval/one-shot messages or spawns, guards, run history, lifecycle | [scheduling](references/scheduling.md) |
| Mesh, peers, cockpit/daemon recovery, environment | [fleet](references/fleet.md) |
| Daemon/TUI config, hooks, separate sesh-ui preferences | [config](references/config.md) |
| Remote directory listing or plugins (bundled manifest example) | [integrations](references/integrations.md) |

Detailed examples retain their versioned incident context; check installed help
and observed state rather than assuming an old deployment claim is current.
