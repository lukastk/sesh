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
- **Selection FOLLOW** (Lukas, testing v1): moving the selection (arrows/j/k,
  ^j/^k, wheel) PREVIEWS the thread — after the cursor rests (300ms debounce)
  the sibling pane navs to it while focus STAYS in the sidebar, so you can keep
  arrowing; Enter (or a single click) is what commits focus to the thread pane.
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
- Everything else is the SAME TUI: filter, views, actions, keypress optimism,
  offline handling, `?` keymap.

Nav routing needs no new carrier: a sidebar pane is a real pane on the master
server, so `sesh tmux nav` takes the master path (marker-client based, H8/H10);
the in-client carve-out only applies when the TUI runs inside the WORK socket,
which also works (a work-server sidebar navs in place and stays open).

## Policy (myrig, after Lukas tests — NOT in this branch)

- Master conf/mastermaint: spawn `sesh tui --sidebar` in a left split of each
  master window; self-heal like the attach panes; a toggle binding (show/hide =
  kill-pane/split); skip on termux. Optionally bake `SESH_TUI_MASTER_MACHINE`
  for the start-cursor preselect.

## Testing this branch without touching the live setup

Build a separately-named binary and split it into a live master window manually:

    go build -o ~/.local/bin/sesh-sidebar ./cmd/sesh          # on the branch
    tmux -L sesh-master split-window -hb -l 38 'sesh-sidebar tui --sidebar'

(The daemon/API is untouched — the branch binary is a pure TUI client change, so
it runs against the deployed schema-45 daemons.)

## Relationship to sesh-ui

A peer surface of sesh-ui's persistent thread list over the same mesh API. No
daemon/API changes in this feature.
