# Persistent and traveling sidebar

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules. Dated incidents/version notes describe
the measured builds, not current fleet deployment.

<!-- BEGIN PRESERVED TOPIC -->
**Sidebar mode** (`sesh tui --sidebar`): the persistent-pane variant for a cockpit — a
narrow NAME-only column preset (the state gutter carries the rest; `[tui] columns` and
`[[tui.column]]` moves don't apply, an explicit `--columns` wins), and **entering a
thread does not quit the TUI**: the nav happens and focus hands to the sibling pane in
the same tmux window, so the sidebar stays ambiently visible beside the agent. A
**single mouse click enters a thread** (the sidebar is a jump list — no
select-then-double-click; clicking the ▸/▾ marker still just folds). **Moving the
selection FOLLOWS immediately**: the cockpit previews the selected thread while focus
stays in the sidebar — Enter/click is what commits focus. A local preview costs ~a
tmux switch (one warm daemon call, no subprocess); while one is in flight further
moves coalesce into a single catch-up nav, so held arrows degrade gracefully.
An **Enter/click always beats an in-flight preview**: clicking while a follow is
still running holds the enter until that preview lands, so the thread you picked is
the last one the cockpit is told to show (previously the stale preview could land on
top of the click, so the click "didn't take" and only corrected itself a nav later).
A stalled preview can't swallow the click — past a short grace the enter goes out
anyway.
**esc/q never quit in sidebar mode** — the keymap binds them to `dismiss` there, so
they clear the ✗ error / note lines (which would otherwise persist forever in a pane
that never quits) instead of killing a pane the cockpit depends on. ctrl+c is the
deliberate kill; hide/show is the cockpit toggle's job. A successful nav or follow
also clears a stale error.
Entering a thread from `/` search exits search (query cleared, cursor on the entered
thread) — the sidebar returns to the whole ambient list. While in filter INPUT mode
the sidebar pane can wear a distinct tmux tint (`--sidebar-filter-style`, e.g. a dark
red) as an unmistakable "keystrokes go to the filter, not to actions" cue — restored
on filter exit. A **maximized** sidebar
(pane >= 80 cols — the cockpit zoom toggle) adaptively renders the FULL grid column
set (the same columns the normal grid shows) and swaps back to name-only on restore.
A maximized sidebar does not follow the selection (the preview pane is hidden and a
cross-machine follow would switch windows and drop the zoom) — browse the list, Enter
commits. Follow
crosses machines: the master window switches and the traveling sidebar rides along
(an intent option tells the swap hook to keep focus on the sidebar; an Enter's switch
focuses the attach pane instead). It previews only live headful threads (it never
revives a dead one — Enter still does); the sibling machine resolves live from the
tmux window name ($SESH_TUI_MASTER_MACHINE pins it for static spawners).

The traffic runs BOTH ways: the sidebar's cursor also **tracks the cockpit**, so a
thread switch made from the cockpit side — the cycle keys, the last-window toggle, a
picker, a command that creates a thread and jumps to it — moves the `>` onto that
thread too. It moves only when what the master window shows actually CHANGES, never
merely because the cockpit disagrees with the cursor: arrowing onto a row the follow
policy skips (a headless one) leaves the cockpit where it was, and a disagreement-driven
tracker would yank the cursor back and make browsing impossible. `sesh tmux nav` rings a
bell file (`<home>/nav-bell`) after every successful nav, which the sidebar reads on a
cheap 250ms timer and answers with one authoritative resolve, so a cockpit keypress
moves the cursor immediately; a 3s backstop catches moves sesh never saw (a native
prefix+n switch, a pane selected by hand). The sidebar's OWN navs never feed back into
it: while a preview is still landing the tracker waits, and a resolve that raced one of
the sidebar's navs is discarded — so arrowing faster than the previews land never drags
the cursor back onto a row you already passed. A thread the current view does not contain —
on hold, archived while you are on `active`, or dropped by an active filter — leaves the
cursor alone: no jump and no view switch, unlike `goto-uuid`, which is a command you
typed rather than an ambient tracker. Every other key/view/action works exactly as in
the normal grid.
