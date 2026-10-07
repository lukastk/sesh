# Tickets, reporting, and prompt files

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules. Dated incidents/version notes describe
the measured builds, not current fleet deployment.

<!-- BEGIN PRESERVED TOPIC -->
- **Tickets** are work items (a name + a prompt) optionally bound to a thread (`needs-input`
  derives from the thread's axes). Tickets are **per-daemon**: a ticket lives on the machine of
  its bound thread (or where it was created), and ticket commands act on the local daemon, or
  on `--machine <m>`. `SESH_TICKET_OWNER`, if set, instead routes every ticket command to one
  owner machine — it is **unset** on Lukas's fleet. To find a ticket whose machine you don't
  know, use `ticket find`. CLI:

  ```bash
  sesh ticket create --name <name> [--prompt <text>]      # starts in triage
  sesh ticket list [--thread <id>] [--current] [--all-machines] [--local]   # --current = calling pane's thread; --all-machines fans out across the mesh (emits machine + thread name per ticket)
  sesh ticket get --id <id> [--field prompt] [--json]     # --field: id|name|prompt|status|thread|created|closed|notes (raw)
  sesh ticket find --id <id> [--json]                     # MESH-WIDE lookup: fans out across peers; returns the
                                                          #   ticket + its owning machine + bound-thread context
  sesh ticket set --id <id> [--name <t>] [--prompt <t>] [--notes <t>|--append-note <t>]   # partial text-field update
  sesh ticket set-status --id <id> --status <s> [--thread <id>] [--note <t>]   # active requires --thread; --note appends
  sesh ticket unbind --id <id>                            # detach from the thread (active→ready); "remove from thread"
  sesh ticket send-prompt --id <id> [--no-prepend]        # deliver the prompt to the bound thread's pane
  sesh ticket needs-input --id <id>                       # derived: active && thread headful·idle
  sesh ticket delete --id <id>
  ```

  `ticket get/list/set-status/...` are **local/owner-routed** (they act on one daemon). To
  locate a ticket **without knowing which machine owns it**, `ticket find` fans out across the
  whole mesh and returns the record plus its owning machine and bound-thread `{id,name,parent}`
  in one call — the mechanism behind an API client (e.g. the Obsidian ticket note) that tracks
  a ticket from anywhere. A ticket found nowhere is `found=false` (exit 0), a legitimate state.
  A terminal ticket carries `closed_at_unix` (the done/dropped timestamp; `--field closed`).

  A ticket has a free-text **`notes`** field (the done/scrapped scratchpad — primarily where an
  agent records what it did and which commit closed it). `set --notes` REPLACES it, `set
  --append-note` appends (blank-line separated), and `set-status --note` appends as part of a
  status change — the ergonomic "close AND record what was done" path. Read with `get --field
  notes`. Surfaced (and rendered as **markdown**) in the Obsidian ticket-note top panel — so
  **write notes in markdown** (headings, lists, fenced code, links) for legible consolidation.

  **`send-prompt`** delivers multi-line prompts intact: literal RPC steering for headed
  Pi, guarded bracketed paste for other headed agents (newlines are preserved, not
  submitted line-by-line). It defaults to **prepending the ticket's name + id** so the
  agent knows which ticket it is on. Toggle the default in `<SESH_HOME>/config.toml`
  (`[ticket]\nsend_prepend = false`); override per call with `--prepend` / `--no-prepend`.

  `ticket list --current` is the agent self-check ("what am I assigned?") using ordinary
  current-thread resolution; the [identity gate](identity.md) still applies. In scripts,
  prefer `ticket list --thread "$TID"` after verification. **Subscriptions** deliver one
  thread's completed turns into another.

  **Status model**: `triage` (unattached, prompt not final) · `ready` (unattached, prompt
  final) · `active` (**attached to a thread** — the only attached state) · `done`/`dropped`
  (terminal). Only `active` requires a binding; `unbind` (or any non-active status) detaches.

  **A ticket lives on the same daemon as its bound thread** (the live `needs-input`/`TKT`
  join is computed per-daemon). To bind a ticket to a thread on **another machine**, the
  ticket is *relocated* to that thread's machine first by **`sesh ticket move`** (which also
  carries the prompt's blobs — see below):

  ```bash
  sesh ticket move --id <id> --to <machine> [--from <machine>]   # default --from: this machine
  ```

  `ticket move` is **daemon-coordinated**: the daemon you invoke it on pulls the record (and
  every `@blob()` its prompt references) from `--from` and pushes them to `--to`, then deletes
  the source — over its own peer transport, so only the invoked machine must reach both ends.
  myrig's `mt-`/`mmt-` ticket commands do this automatically on a cross-machine bind.

## Blobs & files in prompts (`sesh blob`)

A prompt (a ticket prompt, a `thread send`, a headless turn) is **text**, so a file — an
image, a log, anything — is **referenced** by a token and expanded to a real path on
delivery. The store is content-addressed under `<SESH_HOME>/blobs`.

```bash
sesh blob add ~/shot.png            # store a file → prints the token  @blob(9f3ac1b2d4e5)
pngpaste - | sesh blob add --stdin --name shot.png   # store piped bytes (clipboard)
sesh blob ls | get | rm | path      # housekeeping (manual GC via rm; get = raw bytes to stdout)
sesh blob expand                    # stdin→stdout: replace every @blob(<hex>) with its path
```

Paste the printed **`@blob(<hex>)`** token anywhere in a prompt. On **send** (`ticket
send-prompt`, `thread send`, `send-headless`) and on **copy** (the cockpit's copy-prompt),
sesh expands each token to the blob's absolute path on the thread's machine — the agent then
reads the file (image → vision, etc.). A token referencing **no blob is a LOUD error**, never
sent verbatim. Escape a literal with `@@blob(…)`. Every `blob` op takes `--machine` like
tickets; `ticket move` carries a prompt's referenced blobs to the destination automatically.
