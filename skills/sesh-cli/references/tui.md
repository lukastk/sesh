# TUI scope, commands, keys, tickets, and columns

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules. Dated incidents/version notes describe
the measured builds, not current fleet deployment.

<!-- BEGIN PRESERVED TOPIC -->
## The TUI (`sesh tui`)

`sesh tui` opens the live cross-machine thread grid (`--all-machines` to fan out). It is a
thin client — it **emits** actions by driving the CLI verbs, never reimplementing them.

Launch with a directory boundary when a caller needs a project-local grid:

```bash
sesh tui --cwd . --view all          # exact stored CWD only
sesh tui --cwd-tree . --view all     # that CWD plus path descendants
```

The boundary is **launch-time scope**, independent of both the active built-in/custom
view and `/` fuzzy filtering: Tab still lists every configured view, but no view can
escape the CWD boundary. `--cwd-tree` uses path containment (`/work/app2` is not under
`/work/app`). A directory inside the invoking user's home is compared through each
owner-stamped `cwd_rel`, so `~/mysetup/sesh` matches across Linux and macOS even though
their absolute home paths differ. `--view <name>` chooses only the initial view and
does not remove the others. `--cwd` and `--cwd-tree` are mutually exclusive, and neither
combines with `--cursor`.

### Commands, the palette, and the keymap

Every action the grid can perform is a named **command** (`flag`, `archive`,
`set-parent`, `new-divider`, …). There are two ways to run one:

- **`p` — the COMMAND PALETTE.** A full-screen fuzzy search over every command:
  type part of its description or its id, `↑/↓` (or `^k/^j`) move, **enter runs it
  on the selected thread**, esc cancels. A mouse click on a row runs it; the wheel
  moves the selection. Each row shows the command's current key, so the palette
  doubles as a discoverable keymap.
- **A key**, for the frequent commands only. The key set is deliberately small —
  everything else is palette-only.

`?` shows the whole keymap in a scrollable popup (one binding per line, keyless
commands included). The bottom line carries only a dim **`? keys · p commands`** hint.

`q`/`esc` quit as they always have. **`ctrl+c` also always quits** and — unlike
every other binding — cannot be rebound or unbound, so no config can leave the TUI
with no way out. In **sidebar mode** the keymap binds `q`/`esc` to `dismiss`
instead (clearing the ✗ error / note lines), because a persistent cockpit pane
must not die to a stray keystroke; a `?` popup inside a sidebar shows that. `quit`
chosen explicitly from the palette still quits, even there.

Keymap (normal mode) — the commands that carry a default key:

```
↑/↓ or j/k   move cursor          ^j / ^k    scroll viewport a half-page
←/→          fold / unfold tree    ^h / ^l    pan columns left/right (when clipped)
mouse wheel  move selection up/down; Shift+wheel (or wheel left/right) pans columns
mouse click  select the clicked row; DOUBLE-click enters it (= enter); click the ▸/▾
             fold marker to collapse/expand that thread's subtree
enter        nav: switch your tmux client to the thread (or attach from a plain shell;
             a headless thread is promoted, a dead one resumed first)
/            filter mode (fuzzy; ↑/↓ or ^k/^j move the selection; ^t cycles the search
             target; ^y EXCLUDES child threads — off by default, i.e. a query searches
             every thread, nested or not; esc applies)
tab          view PICKER: a popup listing every view (active / on hold / archived /
             all / custom [[tui.views]]) opening on the CURRENT one — tab/↑/↓ move
             (wrap), enter or a mouse click applies, esc cancels; the wheel moves
             the selection.
             A VIEW RING flips ONE key between the two or three views you actually
             live in instead of Tab-walking the picker: name them in `[tui]
             view_ring = ["active", "flagged"]` and bind the `view-ring` command
             to a key with `[[tui.key]]` (it has no default). It steps to the next
             ring entry, wrapping; from a view the ring does not mention it enters
             at the FIRST entry. In the cockpit, Shift+F12 focuses the traveling
             sidebar and presses that key for you.
             The default `active` view shows every non-archived thread PLUS archived
             threads that are still headful (a live pane, glyph `⊘`) or RUNNING, and
             hides on-hold threads — i.e.
             `(flagged OR not archived OR headful OR running) AND not on hold`.
             A FLAGGED thread overrides the archived-hiding (attention wins; unflagging
             re-hides it), but HOLD BEATS FLAG — and hold beats running: an on-hold
             thread never shows in active whatever its state, flagged or busy — its ⚑ is
             visible in the `on hold` view. So an archived thread stays visible while its
             agent is working — including a HEADLESS turn (`◌▶`, e.g. from `delegate` or
             `send --headless`) — and drops out once it is quiet. (`tui --cursor` / the
             cockpit prefix+a preselect the current
             thread; if it is hidden by the default view — e.g. a headless archived
             thread, or one on hold — the TUI opens on `all` so the cursor still lands on it)
p            COMMAND PALETTE (fuzzy-run any command — see above)
h            hold: park the thread until the start of tomorrow (it drops out of the
             default view and returns automatically tomorrow); on an already-held
             thread `h` un-holds it — clearing its own hold, or RELEASING it from an
             ancestor's hold (with its own subtree) when that is what parks it
r            rename (line prompt; ←/→ move the cursor, Home/End jump, edit in place)
f            toggle the flag (⚑; flagging a flag-disabled thread re-enables it)
ctrl+f       toggle auto-flagging for the thread (⌁ when disabled; also unflags)
n            toggle notify          i          toggle the ID column
w            toggle the column-width cap (off = every column grows to its content,
             so clipped text — a long name/cwd — becomes fully visible)
u            unpin (remove the manual ordering; the thread rejoins the auto block)
m            MOVE MODE: reposition the selected pinned row — ↑/↓ move it within the
             block, enter/esc commit-and-exit (an unpinned top-level row is pinned first)
I            thread details: a read-only popup of ALL of the selected thread's
             fields (id, agent, model, state axes, cwd, parent, tags, hold,
             tickets, schedules, session id, meta…); scrolls when the list is
             taller than the pane (↑/↓, j/k, ^j/^k half-page, or the wheel);
             esc/q closes
y            show full UUID (c copies)         R   force refresh
K            tickets view (the selected thread's tickets — see below)
S            SHELLS view — every live tmux session on every reachable machine,
             classified shell/agent/ghost/stale. Scrolls/filters/clicks like the
             grid (↑↓ j/k, ^j/^k half-page, / filter, wheel, click = select,
             double-click = enter). enter jumps to one, P promotes it to a tracked
             shell thread, x kills it (confirmed), R refreshes, esc closes (an
             active filter first). This is where sessions sesh did NOT create
             become visible.
x            stop      a  archive/unarchive (INSTANT)
U            undo the last archive (LIFO across this session's archives)
?            the keymap popup
q / esc      quit (in SIDEBAR mode: dismiss the ✗ error / note lines instead)
ctrl+c       quit (always available, never rebindable)
```

Palette-only commands (id — what it does):

```
goto-uuid        GO TO a thread by uuid (line prompt; the full uuid or the short
                 8-character form, empty = cancel) — see below
hold-until       hold until an explicit date (line prompt; YYYY-MM-DD, empty = un-hold)
tag-add          add a tag                tag-remove   remove a tag (picker)
set-parent       set parent by PICKING one from a list — see below
set-parent-uuid  set parent by pasting a uuid/prefix (empty = root; self/cycle/unknown
                 are refused with a persistent on-screen warning)
new-virtual      new VIRTUAL group (name prompt; empty cancels). Creates a root
                 grouping thread on the SELECTED row's machine (virtual parents only
                 group same-machine threads) and lands the cursor on it — then
                 `set-parent` children under it. No selection = the local machine.
pin              pin the selected top-level thread to the TOP of the manual-order block
                 (pinned threads render ABOVE the auto-sorted list — position is the
                 marker; there is no pin glyph)
new-divider      new DIVIDER (label prompt; empty = an unlabeled rule). A horizontal
                 line in the pinned block, on the SELECTED row's machine
fork             copy the selected thread into a new HEADLESS thread (same conversation,
                 branched; keeps the source name marked ` (fork)`). It doesn't start
                 anything — enter the copy to continue; the source is untouched.
delete           delete the record (asks y/n)
toggle-offline   show / hide the threads of OFFLINE mesh machines (hidden by default)
dismiss          clear the ✗ error / note lines (esc/q do this in sidebar mode)
```

**Going to a thread by uuid (`goto-uuid`).** A line prompt takes a thread's uuid —
the full 36-character one, or the short prefix the ID column (`i`) shows — and the
CURSOR lands on that thread. It **locates, it does not enter**: `enter` is still what
navs into a thread. If the current view already shows the thread the cursor just
moves; otherwise the grid switches to the **first view in display order** (active →
on hold → archived → all → your `[[tui.views]]`) that shows it, and says so in the
note line — so an archived thread takes you to `archived`, a parked one to `on hold`.
A nested thread's ancestors are expanded so the cursor really lands on it. Every
other outcome is a **loud refusal that changes nothing**: a uuid matching no thread,
a prefix matching several (it names them — type more characters), input that isn't a
uuid at all, or a thread the grid is deliberately hiding — one on an **OFFLINE**
machine (run `toggle-offline`), one on a **peer** while the grid is self-only (start
with `--all-machines`), or one the **active filter** drops (clear the filter). It is
palette-only by default; bind it with `[[tui.key]]` if you want a key.

**Setting a parent interactively (`set-parent`).** Run it on the CHILD: a picker opens
listing the threads it could hang under — type to filter (fuzzy, by name or uuid),
`↑/↓` move, **enter applies**, esc cancels, a mouse click applies directly. The list is
narrowed to choices the daemon will actually accept: the **same machine only** (a
parent is validated against the owner's local store, so cross-machine parenting does
not exist), never the thread itself or any of its **descendants** (a cycle), never a
divider, and not its current parent. A thread that already has a parent also gets a
**`(root — no parent)`** entry at the top, which detaches it. `set-parent-uuid` is the
original paste-a-uuid form and is unchanged.

**Rebinding keys (`[[tui.key]]`).** Any command's key can be changed, added to, or
removed in `~/.sesh/config.toml`:

```toml
[[tui.key]]
command = "fork"          # a command id (as shown by `?` / the palette)
key     = "F"             # a bubbletea key string: "f", "F", "ctrl+f", "up", "alt+enter"

[[tui.key]]
command = "delete"
key     = ""              # unbound — reachable only from the palette
```

The **first** entry naming a command REPLACES its default keys (so this MOVES it
rather than adding a second binding); **further entries for the same command add**
more keys. A configured key WINS over a default that held it, and the displaced
command then renders as keyless — the `?` popup and the palette always show what the
keys actually do. An unknown command id, an unusable key name (a typo like
`ctlr+f`), two entries fighting over one key, or an attempt to rebind `ctrl+c` are all
**loud startup errors** — never a key that silently never fires.

On a **virtual** row (`≡` — a grouping node with no agent), Enter and `f` show a
warning instead of acting; convert it first with `sesh thread realize`. Grouping
commands (hold, tags, rename, set-parent, archive, delete) work normally on it.

The selection is **anchored to the thread**, not the row position: when a background
refresh (the ~3s poll / mesh sync) makes a row appear or disappear above the cursor, the
cursor stays on the *same* thread rather than shifting onto whatever slid into its slot —
so archive/delete/stop never hit the wrong thread. The exception is when your own action removes
the selected thread from the view (archive it, hold it, reparent it away): the cursor
then falls to the neighbour rather than chasing the vanished row.

**Tickets view (`K`)** is a full-screen takeover listing the selected thread's tickets. It
defaults to showing **active** tickets; **`tab`** opens a status picker (triage/ready/active/
done/dropped/**all**) that narrows the list. Enter drills into one ticket: its full **id** (the ticket's own uuid, shown read-only at the top — distinct from the truncated thread id) plus its fields (name, prompt) + a small action menu. Enter on
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
