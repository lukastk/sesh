# Persistent thread sidebar (issue #8) — design

Decided with Lukas 2026-07-24 (design-discussion-first, per the issue). Inspired by
herdr's ambient sidebar (spaces + agents panel beside the live pane); assessment in
the myvault pad "Herdr vs sesh - migration assessment".

## Decisions (locked)

1. **Substrate: tmux-layered.** No native PTY multiplexer. sesh ships the render
   mode (`sesh tui --sidebar`); myrig's master cockpit owns the layout. The
   which-client law, nav, adopt, clipboard relay and the phone cockpit all stay.
2. **Placement: ONE TRAVELING sidebar over fixed slots** (revised 2026-07-25 —
   per-window sidebars felt awkward: four copies of the same list, cursor/view
   state not traveling). The cockpit keeps its window-per-machine shape; every
   master window keeps a PERMANENT 38-col left slot. The single real sidebar
   occupies the active window's slot; the other windows hold an inert blank
   placeholder pane. An `after-select-window` tmux hook `swap-pane`s the sidebar
   with the newly-active window's placeholder — swap exchanges SAME-SIZED panes,
   so no pane ever resizes: no inner-session reflow, no agent redraw (the flaw
   that killed the join-pane variant). Same process = cursor/filter/view travel;
   the sibling machine is re-resolved LIVE per follow (followResolver — a swap
   raises no resize event to re-cache on). Worst-case artifact: a one-beat blank
   slot before a late hook lands. Escalation path if that blink annoys in
   practice: revert to per-window (hiccup-free, state diverges), or the
   single-window swap-pane cockpit (zero artifacts + single state, but rewrites
   master nav/markers/mastermaint — a deliberate later project).
3. **Refresh: accept active mesh cadence on desktop masters** (they run active or
   hooks-pinned anyway); the termux master simply doesn't spawn a sidebar (mobile
   data — the H44 rationing stays intact). Poll stays the TUI's normal 3s +
   post-action reconciles; schema 45's keypress optimism + nudge apply unchanged.
4. **Content: the plain thread list** (the existing tree, narrow name-only column
   preset — the state gutter already carries head/busy/flag/attachment). The
   existing Tab view cycle (active / on hold / archived / all / custom
   `[[tui.views]]`) is the "different tabs for different views" ask; a tickets tab
   is a possible follow-up, not v1.
5. The standalone popup (`prefix+s` / F12) is unchanged and kept.

## Mechanism (sesh, this branch)

`sesh tui --sidebar`:

- **Column preset**: when `--columns` is not given, the sidebar uses the NAME-only
  preset (`tui.SidebarColumns()`) instead of `[tui] columns` (which is tuned for
  the wide grid). An explicit `--columns` still wins.
- **Nav does not quit** (the core change): on a successful Enter/click nav, a
  normal TUI quits (`navDoneMsg` → `tea.Quit`) so the popup gets out of the way.
  In sidebar mode the TUI stays running and instead hands FOCUS to its sibling
  pane in the same window (`tmux select-pane` against the sidebar's own `$TMUX`
  socket — the attach pane, so the user lands typing at the agent). Cross-machine
  nav switches the master client to another window (which has its own sidebar);
  the handoff on our window is then a harmless no-op that leaves the attach pane
  active for the next visit. A single-pane window (standalone testing) is a
  no-op, not an error.
- **Selection FOLLOW** (Lukas, testing v1; latency reworked same session):
  moving the selection (arrows/j/k, ^j/^k, wheel) PREVIEWS the thread
  IMMEDIATELY — no debounce: the nav fires on the move; while one is in flight
  further moves are swallowed and the COMPLETION re-arms for wherever the
  cursor is then (fire-immediately + coalesce — single moves preview at nav
  cost, held arrows degrade to previewing the rows each nav catches up to,
  never a queued nav per row; a failed target is recorded so it can't retry-
  loop while still selected). The LOCAL same-window preview is ONE warm daemon
  call (client.TmuxNav — no `sesh` subprocess, no master-window select), ~a
  tmux switch; other cases take the full subprocess master nav. Focus STAYS in
  the sidebar; Enter (or a single click) is what commits focus to the thread
  pane.
  Follow policy (deliberate no-op skips, never errors): live HEADFUL threads
  only (a preview must never revive a dead thread), reachable owner, deduped
  against the thread already shown. Follow CROSSES machines (revised — with the
  traveling sidebar the window switch brings the sidebar along, so the original
  same-machine guard became obsolete): before a window-switching nav the TUI
  declares an INTENT (`@sesh-sidebar-intent` = follow|enter, a global option on
  the master server) that the swap hook consumes — "follow" keeps focus ON the
  sidebar after the swap (the user is mid-arrowing), "enter" focuses the attach
  pane, no intent (a manual prefix+N switch) leaves focus alone. The sibling
  machine comes from `$SESH_TUI_MASTER_MACHINE` (spawner-baked) or the
  sidebar's own WINDOW NAME resolved live; unresolvable = follow disabled,
  Enter-only.
- **A single mouse click enters** (focus handoff included) — the sidebar is a
  jump list, no select-then-double-click; clicking the ▸/▾ marker still folds.
- **Filter Enter leaves search** (sidebar only): entering a thread from `/`
  search navs the filtered selection, then EXITS filter mode with the query
  cleared and the cursor on the entered thread in the full list — the TUI
  persists, so staying narrowed to a stale query read as broken (Lukas). The
  popup grid is untouched (it quits on nav).
- **esc/q are no-ops in sidebar mode** (Lukas hit Esc and the pane vanished,
  taking the traveling slot with it): a persistent pane must not die to a stray
  keystroke. ctrl+c stays the deliberate kill. Hide/show is the cockpit
  TOGGLE's job (myrig binding; transient rig: prefix+b). The toggle's HIDE
  removes the sidebar AND every placeholder slot — full width everywhere, a
  deliberate one-time resize (leaving the blank slots behind read as "the pane
  is still there" — Lukas); SHOW rebuilds the 38-col slot in every master
  window and puts the sidebar in the current one, focused. Windows created
  AFTER a show have no slot until the next toggle cycle (the myrig phase's
  mastermaint self-heal owns that).
- **Filter-mode pane tint** (`--sidebar-filter-style`, e.g. `bg=#3a1620`): while
  the sidebar is in filter INPUT mode its pane wears a distinct tmux
  window-active-style (a dark red) so it's unmistakable that keystrokes go to
  the filter, not to the many action bindings (Lukas). Reuses the same
  pane-tint mechanism as the cockpit's focus tint: on filter enter the current
  active style is saved + swapped to the filter tint, on exit restored (so the
  focus tint the cockpit set survives). Transition detected centrally in Update
  (any handler that flips m.filtering); tmux-only, sidebar-only.
- **Maximize-adaptive columns**: a sidebar pane >= 80 cols (sidebarWideThreshold
  — the cockpit's prefix+z zoom, pinned to the sidebar by myrig sidebar-zoom.sh)
  renders the FULL grid column set (config-resolved exactly like the normal
  grid, moves included; WithSidebarWideColumns) and swaps back to name-only on
  restore. Zoom raises a resize, so no extra wiring. An explicit --columns pins
  the set and disables adaptation. A maximized sidebar also does NOT follow the
  selection (the sibling preview pane is hidden by the zoom, and a cross-machine
  follow would switch the master window — dropping the per-window tmux zoom and
  yanking you out of fullscreen, Lukas 2026-07-26); browse the cross-machine
  list, Enter commits (and naturally exits fullscreen into the thread).
- Everything else is the SAME TUI: filter, views, actions, keypress optimism,
  offline handling, `?` keymap. (Tab view picker opens on the CURRENT view; the
  default `active` view ALWAYS shows flagged threads — archived/on-hold
  included.)

Nav routing needs no new carrier: a sidebar pane is a real pane on the master
server, so `sesh tmux nav` takes the master path (marker-client based, H8/H10);
the in-client carve-out only applies when the TUI runs inside the WORK socket,
which also works (a work-server sidebar navs in place and stays open).

## Policy (myrig — LANDED, myrig a05b793, 2026-07-25)

- `home/.sesh/myrig/sidebar-swap.sh` (the after-select-window hook; lazily
  provisions a slot in windows born after the last toggle) +
  `sidebar-toggle.sh` (prefix+b; `ensure` mode used by mmt-start/mmt-ensure) +
  conf binds (`b` toggle, `v` = pane cycle, alias of prefix+o). Scripts no-op
  on termux. The sidebar spawns from `~/.local/bin/sesh` (the deployed binary).
- **PORT-TO-SESH DECISION (with Lukas): not now.** The machinery is ~60 lines
  of on-demand shell; a sesh port means a daemon-side mastermaint reconciler
  acting continuously on the live cockpit — worse risk/effort ratio than the
  benefits (self-heal ≈ one keypress a month; protocol locality; cells).
  Revisit if: the intent contract drifts and bites, the scripts accrete real
  logic, window churn makes slots annoying, or another surface wants a sidebar.
- **THE INTENT CONTRACT (cross-repo, breaking-change-sensitive):** sesh's TUI
  (internal/tui declareSidebarIntent) writes the master-server global option
  `@sesh-sidebar-intent` = `follow` | `enter` before a window-switching nav;
  myrig's sidebar-swap.sh consumes-and-clears it. Renaming/repurposing the
  option changes BOTH repos. The pane markers `@sesh-sidebar` /
  `@sesh-sidebar-slot` are myrig-internal (sesh never reads them).

## Testing this branch without touching the live setup

Build a separately-named binary and split it into a live master window manually:

    go build -o ~/.local/bin/sesh-sidebar ./cmd/sesh          # on the branch
    tmux -L sesh-master split-window -hb -l 38 'sesh-sidebar tui --sidebar'

(The daemon/API is untouched — the branch binary is a pure TUI client change, so
it runs against the deployed schema-45 daemons.)

## Relationship to sesh-ui

A peer surface of sesh-ui's persistent thread list over the same mesh API. No
daemon/API changes in this feature.
