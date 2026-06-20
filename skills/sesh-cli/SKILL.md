---
name: sesh-cli
description: Use the `sesh` CLI/TUI to list, find, enter, resume, create, tag, archive, rename, reparent, capture, send-to, delegate, or inspect coding-agent threads across machines. Use when the user asks about sesh command usage, the thread TUI, entering/resuming a thread, cross-machine thread state, the sesh daemon, peers, the master-tmux cockpit, or tickets.
---

# sesh

Use this skill when the user wants to **use** `sesh` — the multi-machine coding-agent
session manager — not develop it. `sesh` is one Go binary plus a per-machine daemon.

Mental model: each machine runs a **daemon** that owns a local SQLite store, drives a
tmux "work" server, and maintains a background probe of every local thread's live state.
A thread's runtime identity is its **pane** (a `@sesh-thread-id` marker), so a tmux
session may host many threads (their own windows, or splits) — by default a new thread
gets its own session, but see `--into-session`/`--into-window`/`--into-pane`. Daemons are linked into a **mesh** (peers, over ssh or an
HTTP API) so any machine can see and route to threads on any other. The CLI/TUI is a
thin client over the local daemon's HTTP+JSON surface; `--machine <m>` routes a command
to machine `m`. **`sesh` is mechanism, not UX** — it is explicit and machine-readable
(`--json` everywhere); ergonomic shell glue lives in the user's dotfiles.

Run `sesh help` for the command list and `sesh <command> --help` (or `sesh help <command>
<sub>`) for any command — every command and flag is documented there. Prefer reading
`--help` over guessing. `sesh help-tree` prints the entire command surface (every command
and subcommand, each with a one-line summary) as one indented tree — the fastest way to see
everything at a glance. Invoking a command group with no subcommand (e.g. `sesh thread`)
prints that group's full `--help` (not a partial usage line).

## Thread ids and id-prefixes

Threads are identified by a UUID. Every `--id` accepts an **unambiguous prefix**
(`sesh thread stop --id 1a2b3c4d`; an unknown/ambiguous prefix is a loud error), and most
verbs infer the **current** thread when you omit `--id` (from `$SESH_THREAD_ID`, the
calling pane's marker, or a loud error if ambiguous). `delete` is the exception to
inference only — it accepts a prefix but never infers the current thread (deleting an
ambient thread is a footgun), so it always needs an explicit `--id`. The TUI shows the
short 8-char form (`i` toggles the ID column; `y` shows the full UUID, `c` copies it).

## Before running commands

**Read-only** (safe to run freely): `list`, `grid`, `info`, `status`, `pane`, `capture`,
`mesh`, `tail`, `transcript`, `subscriptions`, `peer list`, `daemon status`, `master
watchers`, `matrix`, `doctor`, `tmux current|info`, `cwd-label`, `meta get|list`.

**Mutating** (think first): `new`, `stop`, `delete`, `resume`, `headful`, `send`,
`send-headless`, `rename`, `tag`, `reparent`, `archive`, `notify`, `meta set|unset`,
`adopt`, `subscribe`/`unsubscribe`, `delegate`, `backup`/`restore`/`copy`, `import`,
`ticket *`, `blob add|rm`, `tmux nav|send-text|stage-file|create-*|kill-session`,
`master up|down|ensure`, `peer add|remove`, `daemon start|stop|restart`.

`sesh tmux kill-session --target <name> [--machine <m>]` kills one work-server session by
exact name (routes cross-machine; a non-existent session is a loud error) — the mechanism
behind myrig's "kill empty sessions" cleanup.

Extra care: `delete` (drops a record; refuses a live thread unless `--force`, which
orphans the agent — `stop` first), `send`/`send-headless` (injects into a real agent's
conversation), `master down` (tears the cockpit), `peer remove`, `import`.

## Core concepts

- **Thread** = one coding-agent conversation. A *headed* thread runs the agent live in a
  tmux pane; a *headless* thread is a durable conversation with no pane (turns run
  stateless via `--resume`). The two are not a stored mode — they're inferred at runtime.
- **Two orthogonal state axes** (what the glyphs mean):
  - **head**: `●` headful (a live pane) / `◌` headless (no pane).
  - **busy**: `▶` busy (mid-turn) / `·` idle.
  So `●·` = headful & idle = **needs input** (waiting for you); `●▶` = working in a pane;
  `◌▶` = a headless turn in flight (wait); `◌·` = idle headless (revivable). A third
  marker shows **descendant activity** (`↓` = a descendant thread — child, grandchild,
  … — is running a turn; blank = none), and a fourth shows attachment (`*` = a tmux
  client is attached). The TUI's gutter header for these is `HBD` (head, busy, descendant).
- **Machine = origin + owner.** A thread lives on the machine that spawned it; mutations
  route to that owner (`--machine`, or auto for tickets). Cross-machine reads come from
  the mesh.
- **Archived** is orthogonal to liveness — a parked record, hidden from the active list,
  still resumable.
- **Agents**: `claude`, `codex`, `pi`. Spawn policy (yolo/default/sandbox) comes from
  `[spawn]` config or `--yolo`/`--sandbox`.
- **Parent/child** threads form a tree (a supervisor thread and its sub-agents); the TUI
  renders it collapsibly. **`thread new` defaults to childing the new thread to the current
  one** (see the ⚠️ note under *Creating* — pass `--no-parent` for a standalone/root thread).
- **Tickets** are work items (a name + a prompt) optionally bound to a thread (`needs-input`
  derives from the thread's axes). Single-owner: every ticket command auto-routes to the
  configured ticket owner. CLI:

  ```bash
  sesh ticket create --name <name> [--prompt <text>]      # starts in triage
  sesh ticket list [--thread <id>] [--current]            # --current = the calling pane's thread
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
  notes`. Surfaced in the Obsidian ticket-note top panel.

  **`send-prompt`** delivers multi-line prompts intact (bracketed paste — newlines are preserved,
  not submitted line-by-line) and by default **prepends the ticket's name + id** so the agent
  knows which ticket it is on. Toggle the default in `<SESH_HOME>/config.toml`
  (`[ticket]\nsend_prepend = false`); override per call with `--prepend` / `--no-prepend`.

  `ticket list --current` is the agent self-check ("what am I assigned?") — it resolves the
  current thread from `$SESH_THREAD_ID`/the pane marker. **Subscriptions** deliver one
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
  The `mt-`/`mmt-` ticket cockpit does this automatically on a cross-machine bind.

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

## Listing directories on a daemon (`sesh fs list`)

A generic, policy-free filesystem primitive the daemon serves over its API: the immediate
**subdirectories** of an allow-listed, **home-rooted** path on the daemon's host. Routes per
`--machine` like tickets, so you enumerate the machine you're targeting (works where the
caller has no local filesystem access — e.g. the Obsidian app on mobile filling its
box/mysetup cwd pickers).

```bash
sesh fs list --path ~/dev                       # box checkout dirs (name<TAB>~-relative path)
sesh fs list --path ~/mysetup --machine macbook --json
```

Dirs only (symlinks not followed). A path **outside the home dir** — or one escaping via
`../` — is refused **loudly** (403), never a silent empty listing.

## The TUI (`sesh tui`)

`sesh tui` opens the live cross-machine thread grid (`--all-machines` to fan out). It is a
thin client — it **emits** actions by driving the CLI verbs, never reimplementing them.

Keymap (normal mode):

```
↑/↓ or j/k   move cursor          ^j / ^k    scroll viewport a half-page
←/→          fold / unfold tree    h / l      pan columns left/right (when clipped)
mouse wheel  move selection up/down; Shift+wheel (or wheel left/right) pans columns
enter        nav: switch your tmux client to the thread (or attach from a plain shell;
             a headless thread is promoted, a dead one resumed first)
/            filter mode (fuzzy; ↑/↓ or ^k/^j move the selection; ^t cycles the search
             target; ^y toggles searching child threads — off by default; esc applies)
tab          cycle views (active / archived / all / custom [[tui.views]])
             (`tui --cursor` / the cockpit prefix+a preselect the current thread; if it
             is hidden by the default `active` view — e.g. archived — the TUI opens on
             `all` so the cursor still lands on it)
r            rename (line prompt; ←/→ move the cursor, Home/End jump, edit in place)
t            add tag                T          remove tag (picker)
P            set parent (paste a parent uuid/prefix; empty = root; self/cycle/unknown
             are refused with a persistent on-screen warning)
n            toggle notify          i          toggle the ID column
y            show full UUID (c copies)         R   force refresh
K            tickets view (the selected thread's tickets — see below)
x            stop      d  delete    a  archive/unarchive   (d and a ask y/n first)
q / esc      quit
```

`d` (delete) and `a` (archive/unarchive) open a **y/n confirmation** — `y` confirms, any
other key cancels. The keymap legend at the bottom **overflows (wraps)** to the terminal
width instead of clipping, so every binding stays visible on a narrow pane.

**Tickets view (`K`)** is a full-screen takeover listing the selected thread's tickets. It
defaults to showing **active** tickets; **`tab`** opens a status picker (triage/ready/active/
done/dropped/**all**) that narrows the list. Enter drills into one ticket: its fields (name, prompt) + a small action menu. Enter on
**name**/**prompt** edits it in your editor (suspend → save); **status** opens a picker
(triage/ready/active/done/dropped); **thread** opens an fzf-style picker to (re)bind the
ticket to another thread (type to filter by name or uuid); **send prompt to thread**
delivers the prompt to the thread's live pane; **delete ticket** asks y/n. In the list,
**`n`** creates a new ticket (type a name) bound to the thread. `↑/↓` move,
`enter`/`l` drill in, `h`/`esc` back, `q` back to the grid. The field editor is
`sesh tui --editor <cmd>`, else `[tui] editor`, else `$EDITOR` (a loud error if none).
Two opt-in columns surface ticket state per thread: **`ticket_name`** (the newest open
ticket's name, `+N` if more) and **`ticket_input`** (a `!` when an active ticket sits on a
headful·idle thread — i.e. it needs your input).

Columns are configurable (`--columns a,b,c` or `[tui] columns`); NAME is blue and CWD
green by default (tunable via `[[tui.column_color]]`). Wide grids clip and scroll
horizontally (`h`/`l`, **Shift+wheel**, or a native wheel-left/right); long grids scroll
vertically (`^j`/`^k` move the viewport a half-page; the mouse wheel moves the SELECTION,
viewport following, with `▲/▼` markers). Wheel **sensitivity** is configurable — how many
notches it takes to move one step (1 = every notch, higher = less sensitive):

```toml
[tui]
mouse_scroll_v = 3   # vertical: 3 notches per row (dampens fast trackpad scrolling)
mouse_scroll_h = 2   # horizontal: 2 notches per column
```

The mouse wheel works in any terminal that forwards wheel events (incl. the `prefix+s`
tmux popup); while the TUI is up it captures the mouse, so terminal-native drag-select
needs Shift. Horizontal-wheel events aren't emitted by every terminal — **Shift+wheel** is
the reliable cross-terminal pan. (On Termux, two-finger touch-scroll is captured by the
terminal app for its own scrollback — use a hardware mouse for wheel events there.)

## Entering, listing, inspecting

```bash
sesh tui --all-machines             # the live grid (enter to jump to a thread)
sesh thread list --all-machines      # flat list across the mesh (--json for scripts)
sesh thread grid --all-machines      # list + live head/busy/attachment per thread
sesh mesh                            # merged cross-machine view + per-peer freshness
sesh info <id>                       # one thread: record + both axes + tmux locator + tickets
sesh thread status --id <id> --json  # just the live runtime axes
sesh thread pane --id <id>           # the live pane locator (errors if dead)
sesh thread capture --id <id> --lines 80   # the live PANE TEXT — peek at what an agent
                                            # is showing (e.g. a child stuck on a prompt)
sesh tail <id> -n 50                 # last N transcript lines
sesh transcript <id>                 # whole transcript dump
```

`sesh thread capture` is the supervising-from-afar tool: a parent thread can read a
child's screen to see if it stalled on a multiple-choice prompt. It routes cross-machine
(`--machine`), resolving the pane on the owner.

## Creating, lifecycle, navigating

> ⚠️ **PARENT INFERENCE — read this before you create a thread.** `sesh thread new`
> defaults to making the new thread a **CHILD** of the thread you are running inside. With
> no `--parent` and no `--no-parent`, it infers a parent from your environment
> (`$SESH_THREAD_ID`, set in every sesh-managed pane, then the calling pane's
> `@sesh-thread-id` marker). **So an agent that spawns a thread will, by default, create a
> child of itself — silently.** This is correct only when you genuinely mean to delegate a
> sub-task.
>
> **If the thread is meant to stand alone (a top-level/independent thread), you MUST pass
> `--no-parent`.** Otherwise it will be a child. Be explicit:
> - `--parent <id>` — child of a specific thread.
> - *(neither flag)* — child of the **current** thread (inferred). Standalone only when run
>   from outside any thread.
> - `--no-parent` — force a **root** thread regardless of context.

`--cwd` accepts a **relative path** (expanded against the directory where you run the
command) or **`~` / `~/…`** and **defaults to the current dir (`.`)** when omitted. A
leading `~` is **resolved by the OWNING daemon against THAT machine's home**, not the
caller's — so a `~`-relative cwd is **portable across a `--machine` spawn** (e.g.
`--cwd ~/proj --machine macbook` lands in macbook's `~/proj`). A bare relative path is
only meaningful locally, so for a cross-machine spawn into a dir outside `~` pass an
absolute path.

```bash
sesh thread new --agent claude --name fix-bug --cwd ~/proj          # headed (live pane)
sesh thread new --agent pi --cwd ~/proj                              # --name is OPTIONAL (a nameless thread)
sesh thread new --agent pi --name notes --cwd . --headless           # headless; cwd = $PWD
sesh thread new --agent codex --name sub --cwd ./src --parent <id>   # a child of a specific thread
sesh thread new --agent claude --name solo --cwd ~/p --no-parent     # a ROOT thread (standalone; suppress inference)
sesh thread new --agent claude --name try --cwd ~/p --fork-from <id> # branch a conversation
sesh thread new --agent pi --name fast --cwd . --headless --model anthropic/claude-haiku-4-5  # pin an agent model

# --model pins an OPAQUE agent model on the thread (no curated list — a bad model fails
# LOUDLY at the agent), applied on spawn, resume, AND every headless turn. Empty = the
# agent's own default. Each agent takes its own spelling: claude `haiku|sonnet|opus|<id>`,
# codex `gpt-5.5|<id>`, pi `provider/id[:thinking]` (e.g. anthropic/claude-opus-4-8).

# Placement — a tmux session may host MANY threads (identity is the pane marker,
# not the session). Default = own new session; otherwise:
sesh thread new --agent pi --name win  --cwd . --into-session <name>   # a new WINDOW of an existing session
sesh thread new --agent pi --name beside --cwd . --into-window <pane>  # a SPLIT beside a pane (or session:window)
exec sesh thread new --agent claude --name here --into-pane "$TMUX_PANE" --exec  # run the agent IN the current shell pane
#   --into-pane is register-then-exec: sesh records the thread + marks the pane,
#   then (with --exec) replaces THIS process with the agent so it takes over the
#   pane. cwd defaults to the pane's. Without --exec it prints the launch command.

sesh thread stop --id <id>           # end runtime (kills the thread's PANE; a session shared with siblings survives), keep the record (revivable)
sesh thread resume --id <id>         # revive a dead thread into a fresh pane (restores convo)
sesh thread headful --id <id>        # promote a live HEADLESS thread into a pane
sesh thread delete --id <id>         # drop the record (refuses a live thread; stop first)
sesh thread archive --id <id>        # park it; --unarchive to restore
sesh thread rename --id <id> --name <new>
sesh thread tag --id <id> --add wip --remove stale     # repeatable --add/--remove
sesh thread reparent --id <id> --parent <p>            # or --root to detach
sesh thread notify --id <id> --off                     # mute this thread's notification hooks

# Adopt a manually-launched agent (a pane on sesh's WORK server) into a thread.
# The conversation id is auto-detected (claude from argv, pi from its RPC socket,
# codex from its rollout) — pass --session-id when it can't be (e.g. a claude
# started with a bare `-r`, which carries no id in its argv):
sesh thread adopt --name here                                  # current pane ($TMUX_PANE)
sesh thread adopt --name here --session-id <conversation-uuid> # explicit id

# Spawn on another machine (real cross-machine spawn over the mesh):
sesh thread new --agent claude --name x --cwd ~/proj --machine macbook
```

`enter`/nav is normally done from the TUI; the underlying primitive is `sesh tmux nav --to
<machine>:<session>` (the master-tmux cockpit + the inner client switch).

## Driving an agent, delegating, awaiting

```bash
sesh thread send --id <id> --text 'run the tests'          # inject into a LIVE pane
sesh thread send-headless --id <id> --text 'summarize'     # run a stateless turn on an idle thread
sesh thread send-headless --id <id> --text 'quick check' --model anthropic/claude-haiku-4-5  # override the model for THIS turn only
sesh thread headless-reply --id <id> --json                # poll a headless turn's result
sesh await <id> --timeout 5m                               # block until a turn finishes (mesh-aware)
sesh delegate --agent pi 'summarize this repo'             # spawn worker → ask → reply → delete
sesh delegate --agent claude 'run CI' --cwd ~/proj --keep  # keep the worker thread around
sesh subscribe <subscribee> --from <subscriber>            # pipe one thread's turns into another
```

## Mesh, peers, master cockpit, daemon

```bash
sesh peer list                                             # registered machines + transport
sesh peer add --machine macbook --ssh lukas@macbook --home /Users/lukas/.sesh \
  --api-addr 100.x.y.z:7070 --api-token-file ~/.sesh/api-token   # http peer (ssh otherwise)
sesh master up --tmux-conf ~/.sesh/myrig/tmux.master.conf  # build the per-machine window cockpit
sesh master attach        # attach to it      sesh master watchers   # who's watching this machine
sesh daemon status        # machine, pid, version, uptime, db, socket, schema
sesh daemon restart       # bounce the daemon (e.g. after a binary update)
sesh doctor               # diagnose the install (binary, config, SESH_MACHINE, daemon checks)
```

## Config (`~/.sesh/config.toml`)

```toml
[[session_name]]                 # name the tmux session from the cwd (first match wins)
match = '^~/dev/.+$'
name  = '{path} ({tid8})'

[[cwd_label]]                    # the TUI CWD column's display transform
match = '^~/mysetup/(?P<rel>.+)$'
label = 'mysetup/{rel}'

[tui]
columns = ["machine","agent","name","cwd","tags","notify"]
all_machines = true              # default `sesh tui` to the cross-machine view (= --all-machines)
[[tui.column_color]]             # NAME blue / CWD green by default; override here
name = "cwd"
color = "green"
[[tui.views]]                    # custom Tab-cycle views over the predicate language
name = "ticketed"
filter = "ticketed and not archived"

[defaults]
notifications = true

[spawn]                          # default launch policy (yolo bypasses permission prompts)
mode = "yolo"

[[hooks]]                        # event hooks: fire a command on an observed state edge
name = "notify-idle"
event = "busy_changed"
from = "busy"
to = "idle"
command = "~/.mybin/sesh-notify"
```

## Common flags & environment

- `--json` — machine-readable output (use it when scripting).
- `--machine <m>` — route to a peer (real ssh hop or its HTTP API; not for peer/matrix/master).
- `--all-machines` — fan a read out across the mesh.
- `SESH_HOME` (default `~/.sesh`), `SESH_MACHINE` (this machine's identity — the daemon
  refuses to run without it), `SESH_THREAD_ID` (the current thread, for inference),
  `SESH_REMOTE`/`SESH_API_TOKEN` (target a remote daemon's TCP API directly).

Errors are loud by design (an unimplemented or impossible request fails explicitly rather
than degrading to a plausible-but-wrong result) — read the error; it usually tells you the
exact precondition that failed (e.g. a 409 "thread has no live pane" on `send` to a dead
thread).
