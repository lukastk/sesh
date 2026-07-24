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
(`sesh thread stop --id 1a2b3c4d`; an unknown/ambiguous prefix is a loud error — a FULL
well-formed uuid skips the prefix lookup entirely, so an unknown full uuid errors at the
verb itself via the daemon's 404 instead), and most
verbs infer the **current** thread when you omit `--id` (from the calling pane's live
`@sesh-thread-id` marker first, then `$SESH_THREAD_ID`, or a loud error if neither
resolves). The pane marker wins because it is re-stamped on adopt/reparent while
`$SESH_THREAD_ID` is frozen at launch and can drift stale; on disagreement the pane is
used and a drift note is printed to stderr. Inference happens **only when `--id` is
omitted entirely**: passing an *explicitly empty* `--id ""` (or an empty positional id,
e.g. from an unset shell variable) is a **loud error**, never silently treated as the
current thread — so a stray empty `$VAR` can't make a verb act on the wrong thread. The
same holds for the other selectors that default to "everything"/"the current thread"
(`backup`/`restore --id`, `hooks test --thread`). `delete` and `stop` go further still —
being destructive, they **never** infer at all (an omitted `--id` is also an error), so
they always need an explicit `--id`. The TUI shows the short 8-char form (`i` toggles the
ID column; `y` shows the full UUID, `c` copies it).

## Before running commands

**Read-only** (safe to run freely): `list`, `grid`, `info`, `status`, `pane`, `capture`,
`mesh`, `tail`, `transcript`, `subscriptions`, `peer list`, `daemon status`, `master
watchers`, `matrix`, `doctor`, `tmux current|info`, `cwd-label`, `meta get|list`, `hooks list`.

**Mutating** (think first): `new`, `stop`, `delete`, `resume`, `headful`, `send`,
`send-headless`, `rename`, `tag`, `reparent`, `archive`, `notify`, `meta set|unset`,
`adopt`, `subscribe`/`unsubscribe`, `delegate`, `backup`/`restore`/`copy`, `import`,
`ticket *`, `blob add|rm`, `tmux nav|send-text|stage-file|create-*|kill-session`,
`master up|down|ensure`, `peer add|remove`, `daemon start|stop|restart`,
`hooks enable|disable|test`.

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
  - **head**: `●` headful (a live pane) / `◌` headless (no pane) / `◇` **virtual**
    (a pure grouping node — no agent at all; see *Virtual threads* below).
  - **busy**: `▶` busy (mid-turn) / `·` idle.
  - **flag** (last gutter cell): `⚑` **flagged** — this thread needs your
    attention. Auto-set when a turn ends or the agent stalls on a
    question/approval while you weren't watching; NEVER auto-cleared (unflag
    with `f` or `thread flag --off`). `⌀` = auto-flagging **disabled** for
    this thread (e.g. children a parent thread monitors). A flagged child
    stays VISIBLE under a collapsed parent (fold-piercing) — a flag never
    hides inside a fold.
  So `●·` = headful & idle = **needs input** (waiting for you); `●▶` = working in a pane;
  `◌▶` = a headless turn in flight (wait); `◌·` = idle headless (revivable). A third
  marker shows **descendant activity** (`↓` = a descendant thread — child, grandchild,
  … — is running a turn; blank = none). The running-state glyphs (`▶` and `↓`) render
  **bright green** by default on non-selected rows so live activity pops out (the
  selected row's reverse video wins); tune or clear per glyph via `[[tui.glyph_color]]`
  (names `busy`, `descendant`). A fourth marker shows attachment (`*` = a tmux
  client is attached), and a fifth shows **archived** (`⊘` = the thread is archived —
  it appears in the default view only while still headful). The TUI's gutter header for
  the core three is `HBD` (head, busy, descendant).
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
  Deleting a thread **promotes its children** to the deleted thread's own parent
  (grandparent; root if it had none) — parent ids never dangle.
- **Virtual threads** (`thread new --virtual --name X`, or the `v` key in the TUI) are
  grouping nodes WITHOUT an
  agent: no pane, no conversation, `agent_kind` reads `virtual`, glyph `◇`. Use one to
  group threads under a parent that isn't (yet) real work: parent/reparent threads under
  it, tag/archive/hold it (a hold on the group parks the whole subtree via inheritance).
  Every agent verb (`send`, `send-headless`, `headful`/`resume`, `capture`, `transcript`,
  fork) refuses loudly; in the TUI, Enter shows a warning instead of entering. Convert it
  into a REAL thread in place with `thread realize --id <id> --agent claude|codex|pi
  [--cwd <dir>]` — the id (and children, tags, holds, ticket bindings) survive, and the
  result is a fresh never-started headless thread: enter it or `send-headless` to start
  the conversation. `--cwd` at realize defaults to the cwd stored at creation (creation
  cwd is optional; one is required by realize time).
- **Tickets** are work items (a name + a prompt) optionally bound to a thread (`needs-input`
  derives from the thread's axes). Single-owner: every ticket command auto-routes to the
  configured ticket owner. CLI:

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

## Plugins (`sesh plugins`) — daemon command-providers

A plugin manifest at `<SESH_HOME>/plugins/*.toml` declares commands the daemon runs **on
its own host** and how the sesh-ui app surfaces them. The app (especially mobile / a remote
daemon) has no shell on the target, so machine ops go via the daemon. Two capability kinds:

- **list** — a command whose JSON output is mapped to `{id,label,groups,path}` items
  (templated `id`/`label`/`path` over each item's fields; `groups` names a string-array
  field; `items` is a dotted path to the array, empty = root). E.g. boxyard boxes → the
  new-thread cwd picker **with groups**.
- **action** — a command with form `field`s; the values are substituted into the argv as
  **ARGV** (never a shell string → no injection) and the command runs. E.g. create-a-box.

```bash
sesh plugins list --json                                    # manifests + capabilities
sesh plugins run boxyard boxes --machine macbook --json     # a list capability → items
sesh plugins run boxyard create-box --field name=my-box     # an action; values as ARGV
```

Routes per `--machine` like `fs list`, so you drive whichever machine's plugins you need.
Commands come from the manifest **only**, never the client. Bad requests (unknown plugin or
capability, missing required field, nonzero command exit) fail **loudly**. The shipped
example is `examples/plugins/boxyard.toml` (drop it at `<SESH_HOME>/plugins/boxyard.toml` on
a machine with `boxyard` on the daemon's PATH).

## The TUI (`sesh tui`)

`sesh tui` opens the live cross-machine thread grid (`--all-machines` to fan out). It is a
thin client — it **emits** actions by driving the CLI verbs, never reimplementing them.

Keymap (normal mode):

```
↑/↓ or j/k   move cursor          ^j / ^k    scroll viewport a half-page
←/→          fold / unfold tree    ^h / ^l    pan columns left/right (when clipped)
mouse wheel  move selection up/down; Shift+wheel (or wheel left/right) pans columns
mouse click  select the clicked row; DOUBLE-click enters it (= enter); click the ▸/▾
             fold marker to collapse/expand that thread's subtree
enter        nav: switch your tmux client to the thread (or attach from a plain shell;
             a headless thread is promoted, a dead one resumed first)
/            filter mode (fuzzy; ↑/↓ or ^k/^j move the selection; ^t cycles the search
             target; ^y toggles searching child threads — off by default; esc applies)
tab          cycle views (active / on hold / archived / all / custom [[tui.views]]).
             The default `active` view shows every non-archived thread PLUS archived
             threads that are still headful (a live pane, glyph `⊘`), and hides on-hold
             threads — i.e. `(not archived OR headful) AND not on hold`. So an archived
             thread stays visible while its agent is running and drops out once it goes
             headless. (`tui --cursor` / the cockpit prefix+a preselect the current
             thread; if it is hidden by the default view — e.g. a headless archived
             thread, or one on hold — the TUI opens on `all` so the cursor still lands on it)
h            hold: park the thread until the start of tomorrow (it drops out of the
             default view and returns automatically tomorrow); on an already-held thread
             `h` releases it
H            hold until an explicit date (line prompt; YYYY-MM-DD, empty = clear)
r            rename (line prompt; ←/→ move the cursor, Home/End jump, edit in place)
t            add tag                T          remove tag (picker)
P            set parent (paste a parent uuid/prefix; empty = root; self/cycle/unknown
             are refused with a persistent on-screen warning)
v            new VIRTUAL group (name prompt; empty cancels). Creates a root
             grouping thread on the SELECTED row's machine (virtual parents only
             group same-machine threads) and lands the cursor on it — then `P`
             children under it. No selection = the local machine.
p            pin the selected top-level thread to the TOP of the manual-order block
             (pinned threads render ABOVE the auto-sorted list, marked `•`)
u            unpin (remove the manual ordering; the thread rejoins the auto block)
m            MOVE MODE: reposition the selected pinned row — ↑/↓ move it within the
             block, enter/esc commit-and-exit (an unpinned top-level row is pinned first)
D            new DIVIDER (label prompt; empty = an unlabeled rule). A horizontal line
             in the pinned block, on the SELECTED row's machine; reposition it with `m`
n            toggle notify          i          toggle the ID column
f            toggle the flag (⚑; flagging a flag-disabled thread re-enables it)
ctrl+f       toggle auto-flagging for the thread (⌀ when disabled; also unflags)
w            toggle the column-width cap (off = every column grows to its content,
             so clipped text — a long name/cwd — becomes fully visible)
I            thread details: a read-only popup of ALL of the selected thread's
             fields (id, agent, model, state axes, cwd, parent, tags, hold,
             tickets, session id, meta…); esc/q closes
o            show / hide the threads of OFFLINE mesh machines (hidden by default)
y            show full UUID (c copies)         R   force refresh
K            tickets view (the selected thread's tickets — see below)
F            fork: copy the selected thread into a new HEADLESS thread (same
             conversation, branched; keeps the source name marked ` (fork)`). It
             doesn't start anything — enter the copy to continue from where the
             source left off; the source is untouched.
x            stop      d  delete (asks y/n)    a  archive/unarchive (INSTANT)
U            undo the last archive (LIFO across this session's archives)
q / esc      quit
```

On a **virtual** row (`◇` — a grouping node with no agent), Enter and `f` show a
warning instead of acting; convert it first with `sesh thread realize`. Grouping keys
(`h`/`H`, `t`/`T`, `r`, `P`, `a`, `d`) work normally on it.

The selection is **anchored to the thread**, not the row position: when a background
refresh (the ~3s poll / mesh sync) makes a row appear or disappear above the cursor, the
cursor stays on the *same* thread rather than shifting onto whatever slid into its slot —
so `a`/`d`/`x` never hit the wrong thread. The exception is when your own action removes
the selected thread from the view (archive it, hold it, reparent it away): the cursor
then falls to the neighbour rather than chasing the vanished row.

**Hold** parks a thread you're not working on today. It sets the thread's
`on_hold_until` to an absolute instant and the owning daemon derives a live "on hold"
flag against its clock, so a hold **auto-expires** — `h` defaults to the start of
*tomorrow*, so a parked thread reappears in the default view the next day with no action.
The default `active` view hides on-hold threads; the **`on hold`** view (in the `tab`
cycle) shows the parked ones. The CLI verb is `sesh thread hold` (see below).

Hold is **inherited down the tree**: a thread's effective hold is `max(its own hold,
its ancestors' holds)`, so holding a parent parks its whole subtree (the children show
`↑<date>` in the HOLD column — an inherited hold). `h` manages a thread's OWN hold; a
child can't be un-held below its parent's hold (that's the `max`). Inheritance is
resolved per machine (a cross-machine parent's hold is not inherited).

**Manual ordering (pinning + dividers).** Threads are otherwise auto-sorted, but you can
**pin** top-level threads to a manually-ordered block that renders **above** the
auto-sorted list. `p` pins the selected thread to the top of the block (pinned rows show
a `•` marker); `u` unpins it (it rejoins the auto block). `m` enters **move mode** — ↑/↓
reposition the pinned row within the block, enter/esc exit (a still-unpinned top-level row
is pinned first). Only **top-level** threads can be pinned; a thread loses its pin when
**archived** or **reparented under another thread**. `D` spawns a **divider** — a
horizontal rule (with an optional label) you place between pinned threads to group them;
dividers live in the pinned block, are repositioned like any pinned row (`m`), and are
removed with `d` (delete), not archived/unpinned. Pinning is a real thread property
(`pin_order`), synced across the mesh, so the order is the same viewed from any machine.
The CLI verbs are `sesh thread pin` / `sesh thread unpin` / `sesh thread new --divider`
(see below).

`d` (delete) opens a **y/n confirmation** — `y` confirms, any other key cancels.
**Archiving is instant** (no confirm): `a` parks the thread immediately and notes
"`U` to undo"; `U` un-archives the most recently archived thread (a LIFO stack of
this session's archives, so repeated `U` walks back through them; an entry whose
owner machine is offline refuses loudly and stays undoable). The bottom line shows
only a dim **`? keys`** hint — press `?` for the full keymap in a popup, ONE BINDING
PER LINE; when the list overflows the terminal height it scrolls (↑/↓, j/k, or the
wheel; ▲/▼ show what's off-screen; esc/q/? closes). Move mode still shows its own
ambient legend.

**Offline machines.** A machine's threads keep showing in the mesh view (for offline
browsing) even after it disconnects, but every action on them routes to the *owning*
daemon — which is unreachable — so entering/archiving/holding one would hang on the
routing timeout (~6–15 s) and then fail. So the TUI **hides an OFFLINE machine's
last-known threads by default**, and if you're pointed at one, an owner-routed key
(`enter`, `a`, `h`, `x`, `r`, `t`, `P`, `K`, …) **refuses instantly** with a loud
`<machine> is offline …` message instead of freezing. The OFFLINE footer line still
shows the machine (and how many threads are hidden); press **`o`** to reveal/re-hide
them (e.g. to browse a powered-off machine). Default the reveal on with `[tui]
show_offline = true` or `--show-offline`. Reachability comes from the mesh sync, so it
can lag a real disconnect by a sync tick or two.

The **`archived`** view (in the `tab` cycle) orders by **most recently archived first**
(the daemon stamps `archived_at` on each archive; un-archiving clears it, so re-archiving
re-stamps a fresh time). An opt-in **`archived`** column shows that timestamp, and the
gutter marks any archived row with `⊘` (so archived-but-headful threads are recognisable
in the default view too).

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

Columns are configurable (`--columns a,b,c` or `[tui] columns`); NAME is blue, CWD
green, and the `ticket_input` `!` red by default (tunable via `[[tui.column_color]]`).

Each column is **capped at a max width by default** (full-width NAME/CWD/TKT-NAME at
40/40/30, fixed columns at their built-in width) so one long name/cwd can't blow out
the layout — a clipped cell ends in `…`. Press **`w`** to toggle the cap off and let
every column grow to its content (so you can read a clipped row in full). Configure it:

```toml
[tui]
max_column_widths = false   # disable the cap entirely (columns always grow to content)

[[tui.column_width]]        # raise/lower one column's cap (applied while the cap is on)
name = "name"
max  = 60
```

Wide grids clip and scroll
horizontally (`^h`/`^l`, **Shift+wheel**, or a native wheel-left/right); long grids scroll
vertically (`^j`/`^k` move the viewport a half-page; the mouse wheel moves the SELECTION,
viewport following, with `▲/▼` markers). Wheel **sensitivity** is configurable — how many
notches it takes to move one step (1 = every notch, higher = less sensitive):

```toml
[tui]
mouse_scroll_v = 3   # vertical: 3 notches per row (dampens fast trackpad scrolling)
mouse_scroll_h = 2   # horizontal: 2 notches per column
```

The mouse also **clicks**: a single left-click selects the row under the pointer, a
**double-click** enters it (the same as `enter` — a headless thread is promoted, a dead
one resumed; an offline machine's thread is refused loudly rather than hung on), and a
click on the `▸`/`▾` fold marker collapses/expands that thread's subtree.

The mouse works in any terminal that forwards mouse events (incl. the `prefix+s`
tmux popup); while the TUI is up it captures the mouse, so terminal-native drag-select
needs Shift. Horizontal-wheel events aren't emitted by every terminal — **Shift+wheel** is
the reliable cross-terminal pan. (On Termux, two-finger touch-scroll is captured by the
terminal app for its own scrollback — use a hardware mouse for wheel/click events there.)

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

sesh thread new --virtual --name "project X"                         # a VIRTUAL grouping node (no agent; cwd optional)
sesh thread realize --id <id> --agent claude --cwd ~/proj            # convert a virtual thread into a real one, in place

sesh thread stop --id <id>           # end runtime (kills the thread's PANE; a session shared with siblings survives), keep the record (revivable)
sesh thread resume --id <id>         # revive a dead thread into a fresh pane (restores convo)
sesh thread headful --id <id>        # promote a live HEADLESS thread into a pane
sesh thread delete --id <id>         # drop the record (refuses a live thread; stop first); children promote to the grandparent
sesh thread archive --id <id>        # park it; --unarchive to restore
sesh thread hold --id <id> --until 2026-07-01          # park until a date (hidden from the default view); auto-expires
sesh thread hold --id <id> --clear                     # release the hold now

# Manual ordering: pin a top-level thread ABOVE the auto-sorted list (default: top).
sesh thread pin --id <id>                              # pin to the top of the manual block
sesh thread pin --id <id> --after <other>             # or --before <other> / --bottom / --top / --order <f>
sesh thread unpin --id <id>                            # remove the manual ordering (rejoins the auto block)
sesh thread new --divider --name "today"              # a DIVIDER: a labeled rule in the pinned block (reposition with pin)
#   Only top-level threads can be pinned; archiving or reparenting-under-another clears it.
#   A divider takes no agent-shaped flags; delete it with `thread delete` (not archive/unpin).

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

# HEADLESS adopt: register an EXISTING conversation that is NOT running anywhere
# (e.g. a claude transcript on disk) as a durable headless thread. No pane is used,
# so --agent and --session-id are REQUIRED (nothing to detect them from); --cwd
# defaults to '.'. A later `send-headless` RESUMES that conversation:
sesh thread adopt --name corkboard --agent claude --session-id <conversation-uuid> --cwd ~/dev/corkboard

# Spawn on another machine (real cross-machine spawn over the mesh):
sesh thread new --agent claude --name x --cwd ~/proj --machine macbook
```

`enter`/nav is normally done from the TUI; the underlying primitive is `sesh tmux nav --to
<machine>:<session>` (the master-tmux cockpit + the inner client switch).

## Driving an agent, delegating, awaiting

```bash
sesh thread send --id <id> --text 'run the tests'          # inject into a LIVE pane
sesh thread send --id <id> --text 'fix it' --wait --timeout 5m  # ...and block until the turn SETTLES (idle/blocked);
                                                           # fails fast (~5s) if the input produces no state change
sesh thread wait --id <id> --until settled --timeout 5m    # block until a state: busy|idle|blocked|settled
                                                           # (settled = idle-or-blocked; loud error naming the last state on timeout)
sesh thread send-headless --id <id> --text 'summarize'     # run a stateless turn on an idle thread
sesh thread send-headless --id <id> --text 'quick check' --model anthropic/claude-haiku-4-5  # override the model for THIS turn only
sesh thread headless-reply --id <id> --json                # poll a headless turn's result
sesh await <id> --timeout 5m                               # block until a turn finishes (mesh-aware)
sesh delegate --agent pi 'summarize this repo'             # spawn worker → ask → reply → archive
sesh delegate --agent claude 'run CI' --cwd ~/proj --keep  # leave the worker active instead of archiving
sesh subscribe <subscribee> --from <subscriber>            # pipe one thread's turns into another
```

**State authority.** A headful thread's busy/idle normally comes from a pane
content-diff heuristic, but pi and claude threads carry an in-agent reporter
(a pi extension / claude hooks, installed via myagent/myrig) that reports turn
starts/ends EXACTLY — the snapshot's `state_authority` field says which
mechanism decided (`reported` or `heuristic`; absent for headless threads).
Reporters use `sesh thread report-state` — a mechanism verb you normally never
type: stale `--seq` values are refused, and authority is dropped automatically
when the thread's pane dies. codex threads stay heuristic for busy (no
turn-start surface), but their `notify` hook — wired into the codex config by
sesh at spawn — still reports turn ENDS for flagging.

**Flags (`sesh thread flag`).** The flag is the "look at this thread" marker:
the daemon auto-flags when a turn ends or the agent stalls on a question /
approval prompt (claude's AskUserQuestion flags with the question as the
reason) while the session is unattended; nothing ever auto-clears a flag.
`thread flag --off` clears; `--disable` suppresses auto-flagging for a thread
(parent-monitored children; also clears any current flag); `--enable`
re-allows it; `--on` flags manually AND re-enables a disabled thread (one
rule). Heuristic busy→idle edges flag only for agents opted in via `[flags]
heuristic_agents = ["codex"]` in config.toml (default: none — reporter edges
are exact, the heuristic can mistake your own typing-settle for a turn end).

## Mesh, peers, master cockpit, daemon

```bash
sesh peer list                                             # registered machines + transport
sesh peer add --machine macbook --ssh lukas@macbook --home /Users/lukas/.sesh \
  --api-addr 100.x.y.z:7070 --api-token-file ~/.sesh/api-token   # http peer (ssh otherwise)
sesh master up --tmux-conf ~/.sesh/myrig/tmux.master.conf  # build the per-machine window cockpit
sesh master attach        # attach to it      sesh master watchers   # who's watching this machine
sesh daemon status        # machine, pid, version, uptime, db, socket, schema, mesh_cadence
sesh daemon restart       # bounce the daemon (e.g. after a binary update)
sesh doctor               # diagnose the install (binary, config, SESH_MACHINE, daemon checks)
```

**Mesh sync cadence (demand-driven).** The background peer sync runs at full pace (~1s)
only while something is consuming the mesh view — a `sesh tui`/sesh-ui poll or an
`--all-machines` read — or when `[[hooks]]` are configured (hooks observe remote threads
through the cache, so they pin full pace). Otherwise it idles to `[mesh] idle_interval`
(default 60s; `"0s"` = never idle) and snaps back instantly on the next read, so opening
the TUI after an idle stretch is fresh within ~a round trip. `sesh peer list` showing
"synced 45s ago" on a quiet daemon is therefore deliberate idling, not degraded sync —
`sesh daemon status` reports the pace as `mesh_cadence` (active / idle / hooks-pinned /
always). Between schema-41 daemons each sync round transfers only the rows that CHANGED
since the last one (delta sync; an unchanged round is ~100 bytes), and against older
daemons an unchanged snapshot is a bodyless 304 (ETag). What the views SHOW is unchanged
by any of this — every machine's full thread set, archived included, still replicates
across the mesh.

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
show_offline = true              # show OFFLINE machines' threads by default (else hidden; `o` toggles)
[[tui.column_color]]             # NAME blue / CWD green / ticket_input red by default; override here
name = "cwd"
color = "green"
[[tui.glyph_color]]              # gutter attention glyphs: busy ▶ / descendant ↓ (bright green by default)
name = "busy"
color = "2"                      # a name, a 0-255 number, or #rrggbb; empty clears the tint
[[tui.views]]                    # custom Tab-cycle views over the predicate language
name = "ticketed"
filter = "ticketed and not archived"   # keywords incl. headful/headless/busy/idle/archived/onhold/flagged/flagdisabled/ticketed

[defaults]
notifications = true

[mesh]
idle_interval = "60s"            # peer-sync pace while nothing reads the mesh view ("0s" = never idle)

[spawn]                          # default launch policy (yolo bypasses permission prompts)
mode = "yolo"

[[hooks]]                        # event hooks: fire a command on an observed state edge
name = "notify-idle"
event = "busy_changed"
from = "busy"
to = "idle"
command = "~/.mybin/sesh-notify"
```

A hook command runs through `$SHELL -c` with the event described in env vars:
`SESH_EVENT` (+`SESH_EVENT_FROM`/`SESH_EVENT_TO` on edges), `SESH_THREAD_ID`,
`SESH_THREAD_NAME`, `SESH_AGENT`, `SESH_MACHINE`, `SESH_CWD`, `SESH_SESSION`,
`SESH_TAGS` (comma-joined), `SESH_HEAD`, `SESH_BUSY`, `SESH_ATTACHMENT`
(`attached`/`detached`), `SESH_ATTACHED_ACTIVITY_AGO` (seconds since the last
INPUT on a client attached to the thread's session; absent when detached or
unknown), `SESH_ATTACHMENT_CHANGED_AGO` (seconds since the observing daemon saw
the attachment axis flip — the "just navigated onto it" signal; absent if no
flip observed since daemon start), `SESH_NOTIFY` (the per-thread gate as
`1`/`0` — the hook fires regardless; honoring the gate is the hook's job),
`SESH_FLAGGED` (`1`/`0` — the needs-attention flag), `SESH_FLAG_REASON`
(present only when an auto-flag carries one, e.g. the question the agent
asked), and `SESH_STATE_AUTHORITY` (`reported`/`heuristic` — which mechanism
decided busy; absent when unknown). The event vocabulary includes
`flag_changed` (from/to `flagged`/`unflagged`) — to=flagged is THE toast
edge: the daemon flags exactly when a turn ends or the agent stalls on a
question/approval while nobody watches (and on manual flags). The activity/flip ages exist because a HEURISTIC busy→idle edge alone
can't tell a finished turn from the user pausing: typing into a pane or
navigating onto it latches the content-diff busy probe like agent output
would, while raw attachment over-suppresses (cockpit clients park on
sessions) — a notify hook should skip only when attached AND (recent input OR
a recent attachment flip), failing open when the vars are absent. Under
`SESH_STATE_AUTHORITY=reported` the edge is exact (a real turn boundary).

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
