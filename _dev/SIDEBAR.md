# Persistent thread sidebar (issue #8) — design

Decided with Lukas 2026-07-24 (design-discussion-first, per the issue). Inspired by
herdr's ambient sidebar (spaces + agents panel beside the live pane); assessment in
the myvault pad "Herdr vs sesh - migration assessment".

## Decisions (locked)

1. **Substrate: tmux-layered.** No native PTY multiplexer. sesh ships the render
   mode (`sesh tui --sidebar`); myrig's master cockpit owns the layout. The
   which-client law, nav, adopt, clipboard relay and the phone cockpit all stay.
2. **Placement: one sidebar pane per MASTER WINDOW** (the cockpit keeps its
   window-per-machine shape). A narrow left pane (~35 cols) beside the ssh/attach
   pane, spawned/healed by the myrig master layer. No cockpit restructure.
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
  only (a preview must never revive a dead thread), the sibling window's OWN
  machine only (a preview must never switch master windows — Enter still does
  the full cross-machine switch), reachable owner, deduped against the thread
  already shown. The sibling machine comes from `$SESH_TUI_MASTER_MACHINE`
  (spawner-baked) or the sidebar's own WINDOW NAME (cockpit windows are named
  by machine); unresolvable = follow disabled, Enter-only.
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
