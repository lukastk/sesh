# Holds, releases, pinning, archives, and offline views

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules. Dated incidents/version notes describe
the measured builds, not current fleet deployment.

<!-- BEGIN PRESERVED TOPIC -->
**Hold** parks a thread you're not working on today. It sets the thread's
`on_hold_until` to an absolute instant and the owning daemon derives a live "on hold"
flag against its clock, so a hold **auto-expires** — `h` defaults to the start of
*tomorrow*, so a parked thread reappears in the default view the next day with no action.
The default `active` view hides on-hold threads; the **`on hold`** view (in the `tab`
cycle) shows the parked ones. The CLI verb is `sesh thread hold`; examples are in
[thread lifecycle](threads.md).

Hold is **inherited down the tree**: a thread's effective hold is `max(its own hold,
its ancestors' holds)`, so holding a parent parks its whole subtree (the children show
`↑<date>` in the HOLD column — an inherited hold). Inheritance is resolved per machine
(a cross-machine parent's hold is not inherited), and an **archived** thread is detached
from it: a hold parks *active* work temporarily, archiving is the permanent kind and
already hides the thread everywhere, so an archived thread neither inherits an
ancestor's hold nor passes one down to its descendants. Its OWN hold still applies and
still reaches its children — what stops at an archived node is only what flows from
above it. (Un-archiving returns the thread to inheriting: this is about what is parked
*while* archived, not a permanent exemption.)

**Releasing a child from its parent's hold.** Because the effective hold is a `max`,
clearing a child's own hold cannot undercut its parent's — so a thread is in exactly
one of three states, and `sesh thread hold` writes them:

| state | how | effect |
|---|---|---|
| **held** until T | `--until <date>` / `--until-unix <n>` | parked; its subtree inherits |
| **released** until T | `--release [--until <date>]` (default: tomorrow) | ancestors' holds do not apply — to it *or* its own subtree |
| neither | `--clear` | inherits from its ancestors again |

A release is a **dated** statement like a hold, so it auto-expires: tomorrow's parking
round parks the thread again like everything else, and no thread is ever silently
exempt forever. Setting either state clears the other — a thread is never both. In the
TUI, `h` on a thread parked by an ancestor issues the release for you (and the HOLD
column shows `~<date>` while a release is in force, while the leading gutter cell
shows `⧗` own / `⧖` inherited so you can see which rows need which); `h` on a released thread holds it,
which clears the release. `--clear` and `--release` **fail loudly** if the thread is
still on hold afterwards, naming the ancestor responsible — an un-hold that silently
left the thread parked is exactly the bug this replaced.

**Manual ordering (pinning + dividers).** Threads are otherwise auto-sorted, but you can
**pin** top-level threads to a manually-ordered block that renders **above** the
auto-sorted list. The `pin` command (palette) pins the selected thread to the top of the
block; `u` unpins it (it rejoins the auto block). Pinned rows carry **no marker glyph** — their position above the
auto-sorted block is the signal. `m` enters **move mode** — ↑/↓
reposition the pinned row within the block, enter/esc exit (a still-unpinned top-level row
is pinned first). Only **top-level** threads can be pinned; a thread loses its pin when
**archived** or **reparented under another thread**. `new-divider` spawns a **divider** — a
horizontal rule (with an optional label) you place between pinned threads to group them;
dividers live in the pinned block, are repositioned like any pinned row (`m`), and are
removed with the `delete` command, not archived/unpinned. Pinning is a real thread property
(`pin_order`), synced across the mesh, so the order is the same viewed from any machine.
The CLI verbs are `sesh thread pin` / `sesh thread unpin` / `sesh thread new --divider`
(see [thread lifecycle](threads.md)).

`delete` opens a **y/n confirmation** — `y` confirms, any other key cancels.
**Archiving is instant** (no confirm): `a` parks the thread immediately and notes
"`U` to undo"; `U` un-archives the most recently archived thread (a LIFO stack of
this session's archives, so repeated `U` walks back through them; an entry whose
owner machine is offline refuses loudly and stays undoable). Move mode shows its own
ambient legend in place of the `? keys · p commands` hint.

**Offline machines.** A machine's threads keep showing in the mesh view (for offline
browsing) even after it disconnects, but every action on them routes to the *owning*
daemon — which is unreachable — so entering/archiving/holding one would hang on the
routing timeout (~6–15 s) and then fail. So the TUI **hides an OFFLINE machine's
last-known threads by default**, and if you're pointed at one, an owner-routed key
(enter, archive, hold, stop, rename, tag, set-parent, tickets, …) **refuses instantly**
with a loud `<machine> is offline …` message instead of freezing — from the command
palette as well as from a key. The OFFLINE footer line still shows the machine (and how
many threads are hidden); run **`toggle-offline`** from the palette to reveal/re-hide
them (e.g. to browse a powered-off machine). Default the reveal on with `[tui]
show_offline = true` or `--show-offline`. Reachability comes from the mesh sync, so it
can lag a real disconnect by a sync tick or two.

The **`archived`** view (in the `tab` cycle) orders by **most recently archived first**
(the daemon stamps `archived_at` on each archive; un-archiving clears it, so re-archiving
re-stamps a fresh time). An opt-in **`archived`** column shows that timestamp, and the
gutter marks any archived row with `⊘` (so archived-but-headful threads are recognisable
in the default view too).
