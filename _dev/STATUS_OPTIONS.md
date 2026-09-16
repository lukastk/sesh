# sesh v2 — status options: the work server's thread row without a shell per redraw (design)

*Status: designed and built 2026-09-16 from the termux ssh-agent leak (AGENTS.local.md H108
follow-up 3; BACKLOG item 7). Mechanism in sesh, rendering policy in myrig.*

---

## 1. The problem, measured

The work tmux server's top status row shows the thread owning the focused pane:
`sesh: <name> [id8] · <agent> · [tags]  🗄 archived  ⌁ auto-flag off  ⚑ FLAGGED`. It was a
tmux `#()` job — `#(zsh -lc 'sesh-current-status #{pane_id} #{socket_path}')` in myrig's
`tmux.work.conf` — i.e. a **login zsh sourcing the whole rig, two `sesh` CLI calls and two
`jq`s, per run**.

How often it ran was the surprise. `status-interval` is its 15 s default on every server and
H98's follow-up had recorded the row as "re-run only on that beat". Measured 2026-09-15 by
sampling the job's child processes every 0.1 s for 20 s:

| server | tmux | attached clients | spawns in 20 s |
|---|---|---|---|
| termux work server | 3.7c | 1 | **22** |
| mymain work server | 3.6b | 4 | **12** |

About one login shell per second per attached client, whenever a cockpit window is attached.
Why tmux re-runs a `#()` far below `status-interval` was not established; the number is
measured, not explained. On termux the consequence was catastrophic: that shell sourced
`termux.sh`, which started an ssh-agent whenever `SSH_AUTH_SOCK` was empty, and the
daemon-created work server had no agent in its global environment — ~2,000 leaked agents,
~1.9 GB of RAM at oom_score_adj 0, invisible from inside Termux (H108 follow-up 3). myrig
332a403 fixed the leak; this design removes the fork that carried it, everywhere.

## 2. The design in one sentence

**The owning daemon stamps the record fields the row renders onto each marked pane as tmux
user options, kept current within a maintainer tick, and the status row becomes a pure tmux
format over those options — zero forks per redraw, and the rendering stays myrig's.**

## 3. The contract

Every pane carrying a `@sesh-thread-id` marker whose thread has a record on this daemon
carries six PANE user options (`internal/tmux/status.go`):

| option | value |
|---|---|
| `@sesh-name` | the thread's name |
| `@sesh-agent` | agent kind (`claude`/`codex`/`pi`/…) |
| `@sesh-tags` | tags comma-joined; `""` when none |
| `@sesh-archived` | `"1"` or `""` |
| `@sesh-flagged` | `"1"` or `""` |
| `@sesh-flag-disabled` | `"1"` or `""` |

The thread id itself is the existing marker: `#{=8:@sesh-thread-id}` is the eight-character
short id. A format renders the row with lookups alone — this exact string is pinned by the
`tmux.status-options` cells and carried by myrig's `tmux.work.conf` inside its colours:

```
#{?@sesh-name,sesh: #{@sesh-name} [#{=8:@sesh-thread-id}] · #{@sesh-agent}#{?@sesh-tags, · [#{@sesh-tags}],}#{?@sesh-archived,  🗄 archived,}#{?@sesh-flag-disabled,  #[fg=colour244]⌁ auto-flag off,}#{?@sesh-flagged,  #[fg=red#,bold]⚑ FLAGGED,},}
```

Properties, all measured on tmux 3.6b/3.7c before relying on them: `#{?@opt,…}` treats an
empty option as false, so booleans are `"1"`/`""` and an unset name renders the row empty (an
unmarked pane, the scratch shell); a comma inside a VALUE is safe (the conditional is split
before values are substituted); a literal comma inside a branch is spelled `#,`; the `=8:`
length modifier applies to user options. A `#[…]` style sequence inside a thread name styles
the line — exactly what happened through the shell job; values are not escaped.

Scope is PANE only, never window or session: tmux resolves user options with inheritance
during format expansion (pane → window → session → global), so a coarser scope would make an
unmarked pane in the same session render its neighbour's thread — the trap `ShellIDOption`
documents in `_dev/SHELL.md`. Shell threads (session-marked, no pane marker) therefore carry
no status options, as today's row showed nothing for them either.

## 4. The mechanism

`internal/daemon/statusoptions.go`, called once per maintainer tick after the sweep:

- **Desired state** is a pure function of the record index and the pane index the tick
  already has: every marked pane with a record → its six fields. A hand-stamped marker with
  no record carries nothing.
- **Diff, then write.** Per pane one struct compare against `paneStatus` (what was last
  written). Only differing panes go to tmux, in ONE invocation (`tmux.ApplyPaneOptions`) —
  a command list of `set-option -p` sub-commands cut under an 8 KiB argv budget (the whole
  list travels as one 16 KiB imsg, H90's cap), followed by `refresh-client -S -t <client>`
  for every attached client so a rename or flag repaints at once rather than on the next
  15 s beat. The client names come from the same `list-clients` call that already feeds the
  attachment axis (`AttachedClients`), so the repaint costs no extra enumeration.
- **Clearing.** A cached pane no longer marked is cleared (`set-option -u` on the six names)
  if it still exists; a pane that is gone took its options with it and is just forgotten. The
  set of all live pane ids falls out of the `RuntimeIndex` walk the tick already runs.
- **Zero-thread machines pay nothing.** The idle early-out (no local records) skips tmux
  entirely — except for ONE clearing walk while something is still cached (the last
  record's pane after a force-delete), after which the cache is empty and stays skipped.
- **Restart.** `paneStatus` is in-memory; the first sweep after a daemon start rewrites every
  marked pane. Options left by a dead daemon are harmless meanwhile: they describe the pane's
  thread, and the pane's marker outlives daemons the same way.
- **Two tmux abort semantics**, both measured and both handled in `ApplyPaneOptions`: a pane
  that vanished between enumeration and write stops the command list at its sub-command with
  `no such pane: %N` (the panes BEFORE it landed; it is dropped; the rest is retried — the
  `CapturePanes` pattern, but note the message differs from capture-pane's `can't find
  pane`); a client that detached fails only the trailing `refresh-client` with `can't find
  client` (every write precedes the refreshes, so they all landed; the next beat repaints
  the rest). Any other failure is loud, and whatever was not applied stays out of
  `paneStatus` so the next tick retries exactly that.

Per-tick cost when nothing changed: one map walk and a struct compare per marked pane. No
tmux call.

## 5. What deliberately does NOT change

- **What the row shows.** Same fields, same glyphs, same colours, same "nothing for an
  unmanaged pane". The rendering (order, text, colours) stays in myrig's conf; sesh publishes
  data.
- No API, wire, schema or CLI change. A pre-change binary simply does not stamp options and a
  format-only conf renders an empty row on that machine until it is rebuilt — loud enough,
  never wrong.
- The personal (non-sesh) tmux server's `.tmux.base.conf` still uses the shell function; its
  panes are not daemon-managed. Its cost is CPU only (no per-shell agent start outside
  termux). Out of scope here.

## 6. Tests

- `internal/tmux`: `ApplyPaneOptions` writes/empties/unsets on a real server, round-trips
  the one value tmux's argv would misread (`;` as `\;`), skips a vanished pane in the middle
  of a batch and still applies the panes after it, survives a detached client in the refresh
  list, and chunks under the argv budget; `AttachedClients` names a real nested client.
- `internal/daemon`: the real maintainer against a real store and pane — stamped after one
  tick, NOT rewritten while unchanged (`statusWrites` is the observable), every mutation
  (rename to `;`, tags, flag, archive, flag-disable) landing within a tick, cleared on
  unstamp, restored on re-stamp, cleared by the zero-thread early-out after the last delete
  and never touched again.
- Matrix row **`tmux.status-options`** (agent-agnostic × local/remote): a REAL pi thread in a
  real pane; the options appear; the contract format renders the exact row through tmux
  alone (`display-message -p -F`); rename/tag/flag/archive through the real verbs (over the
  real ssh hop for the remote cell) change the rendered row within a tick; unstamping the
  pane empties it. The format is asserted to contain no `#(`.
- Anti-gaming: neutering the maintainer's sync turns the cells red at "never stamped".

## 7. Deploy

Daemon change ⇒ rebuild + supervised restart per machine (termux per its recipe), then the
myrig conf: `tmux.work.conf`'s `status-format[0]` becomes the format above, and a running
work server needs `source-file` to pick it up (a `set -g` server option). myrig's
`_mt_refresh_status_on` (H98's keypress repaint) is retired: the daemon repaints on change.
