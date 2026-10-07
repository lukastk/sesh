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
- **Route for real:** `--machine <m>` uses SSH or peer HTTP, not a cosmetic field;
  HTTP routing needs the local daemon. Check owner/reachability with `mesh`/`info`.
  Quote owner-home paths: `--cwd '~/proj'` reaches the remote home; unquoted `~`
  expands in the caller's shell. Relative paths expand locally.
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
- **Tickets:** read assigned prompts, keep active until ready, then finish with
  `done` **and a markdown note** explaining changes and commit SHA. Abandoned work
  is `dropped` with a reason, never silently closed. Verify routing/binding.

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
| Cron/interval/one-shot messages or spawns, guards, run history | [scheduling](references/scheduling.md) |
| Mesh, peers, cockpit/daemon recovery, environment | [fleet](references/fleet.md) |
| Daemon/TUI config, hooks, separate sesh-ui preferences | [config](references/config.md) |
| Remote directory listing or plugins (bundled manifest example) | [integrations](references/integrations.md) |

Detailed examples retain their versioned incident context; check installed help
and observed state rather than assuming an old deployment claim is current.
