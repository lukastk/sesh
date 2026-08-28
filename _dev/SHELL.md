# SHELL.md — shell threads (a tracked tmux session as a first-class thread)

Design record for the shell-thread feature. The full spec, including the decision
history and every rejected alternative, is the vault ticket
**`tkt/2026-08-23 sesh - new thread type.md`** (sesh ticket `4d4e8592-b9d7-43cd-8f5d-d3df37c5c9f0`).
This file is the in-repo version: what it is, why it is shaped this way, and the
traps that shaped it.

## The problem

sesh is centred entirely on agent threads, which obscures that everything in the
cockpit is really tmux sessions. The myrig commands (`mmt-enter-box`,
`mmt-enter-tmux-session`, `mmt-create-session`) can start and enter a plain
session in a box, but it has no record — so it is invisible to the TUI, dies with
the machine, and cannot be tagged, pinned, parented, held or archived.

The mechanism already exists. **What is missing is only a durable record.**

## The model — a shell thread's "conversation" is its working directory

A shell thread is the same shape as an agent thread, with the working directory
playing the role the transcript plays for an agent:

| | agent thread | shell thread |
|---|---|---|
| the durable thing | the conversation | the **working directory** |
| its runtime | a tmux **pane** running the agent | a tmux **session** rooted at that dir |
| runtime identity | `@sesh-thread-id` on the **pane** | `@sesh-shell-id` on the **session** |
| headful ● | pane exists, agent alive | session exists |
| headless ◌ | conversation on disk, no pane | a remembered place, no session |
| revive | `<agent> --resume <id>` | `new-session -c <cwd>` + stamp the marker |

So `head` and `revive` stay genuinely meaningful and the TUI needs no special
case for Enter. **Do not say "attached/detached"** for session-live-vs-not —
that is already sesh's third runtime axis (a tmux client is looking at it). The
words are headful/headless.

## Taxonomy: `nonAgentGate` must split in two

`nonAgentGate` treats virtual and divider as one class ("no conversation AND no
runtime"). A shell thread has a runtime but no conversation, so the predicate
splits:

| kind | has runtime? | has conversation? |
|---|---|---|
| `claude` / `codex` / `pi` | ✔ | ✔ |
| `shell` | ✔ | ✘ |
| `virtual` / `divider` | ✘ | ✘ |

- **conversation gate** refuses `fork`, `transcript`, `send-headless`, `--model`,
  `adopt`, agent-session stamping, `report-state`.
- **runtime gate** refuses `enter`/nav, `stop`, `capture`.

Settle this before writing the record layer; keying a gate on the wrong axis
reproduces the silent-wrong-behaviour class this project exists to prevent.

## Ghosts: recognise without recording

Live sessions sesh has no record of are **ghosts**. They are enumerated live and
never persisted. Recording every session would mint a record per throwaway shell,
churn the mesh, and force sesh to auto-delete records — which it does nowhere
else. Promotion (still to build) is what turns a ghost into a record.

Ghosts must **never** enter the replicated mesh snapshot: their `attached` bit
flips constantly and would destroy delta sync's steady-state-empty property
(H44). They are fetched on demand, per machine, only while the viewer is open.

## Trap digest

1. **tmux user options INHERIT during format expansion** — pane → window →
   session → global, resolving from the deepest object in the format's context.
   Measured: with `@sesh-thread-id` set at SESSION scope, every *unmarked pane*
   in that session reports it from `list-panes -a -F '#{@sesh-thread-id}'`. That
   would silently corrupt `FindPaneByThreadID`, `ThreadIDOfPane`, the
   maintainer's pane map, `sesh tmux current`, adopt's ownership guard and nav's
   window resolution. **The session marker must be a distinct key
   (`@sesh-shell-id`), never `@sesh-thread-id` at session scope.**
2. **`list-sessions -F '#{@foo}'` resolves through the session's ACTIVE PANE**,
   not the session object — measured: a session-scoped value read back as the
   active pane's. A session-scoped read is only trustworthy for a key that is
   never set at pane scope. A second, independent reason the keys must differ.
3. **The `=exact` target prefix is NOT honored by `set-option`/`show-options`** —
   it errors (`no such session: =boxsess`) rather than failing silently the way
   the documented `display-message -t =session` trap does. Other verbs in this
   repo (`list-panes`, `list-clients`, `has-session`, `kill-session`) DO honor
   it, so copying a neighbouring idiom is exactly how this bites.
4. **`show-options -v` exits 1 on an unset option** ("invalid option"); `-qv`
   returns empty with rc=0 (already this repo's idiom — `internal/tui/model.go`
   reads `window-active-style` with `show-options -pqv`). Use `-qv` for a single
   session read, `list-sessions -F` for the maintainer's per-tick bulk sweep.
5. **A tmux session has no cwd of its own.** `#{session_path}` is its START
   directory and does NOT follow a `cd`; `#{pane_current_path}` does. The
   record's cwd stays authoritative for revival. Proven in the `tmux.info` cell.
6. **`requiresReachableOwner` is keyed by command ID with an exhaustive drift
   guard** (H88) — a command in neither list fails the test. Classify by whether
   the target IS the grid selection. `shells` is LOCAL for exactly that reason.
7. **New gutter glyphs need an H78 confusable-family entry**, and a family must
   never contain two live states or the guard is red on arrival.
8. **Conformance tests here create and kill real tmux sessions** — the widest
   blast radius of any test surface in this repo. Isolate `SESH_HOME`,
   `SESH_TMUX_SOCKET`, `SESH_MASTER_SOCKET`, strip inherited `SESH_*`, and use
   `shortSandboxHome` (a socket under a long `t.TempDir()` overruns `sun_path`).

## Status

**Stages 1–5 BUILT** (api schema 47, NO store migration — `agent_kind` is a TEXT
column and every field a shell thread needs already existed).

- **The record**: `agent_kind: "shell"`, `machine`/`session_name`/`cwd`/`name`/
  `tags`/`parent`/`meta`/`archived`/`on_hold_until`/`pin_order`/`flagged`/`notify`.
- **The marker**: session-scoped `@sesh-shell-id` (`tmux.ShellIDOption`), read
  alongside the pane marker from ONE server walk (`Server.RuntimeIndex`).
- **The taxonomy split**: `conversationGate` (fork, transcript, send-headless)
  vs `runtimeGate` (enter/nav, send, capture, revive), replacing `nonAgentGate`.
- **Both state resolvers branch on kind before the pane lookup** — the maintainer
  (`refreshThread`) AND the on-demand `thread status` path. They must agree; the
  conformance cells caught the on-demand one being missed.
- **CLI**: `sesh shell new|enter|here|promote|sessions|info|panes`, plus
  `thread send --pane/--window`, `thread stop --force`, `thread new --parent-shell`.
- **TUI**: `S` shells viewer (classified live sessions; `enter` jump, `P` promote,
  `x` kill with a confirmation naming the agent threads it would take down), the
  `❯`/`›` head glyph (a shell prompt) and a blank busy cell.
- **myrig**: `mt-promote-session-here` → `sesh shell here`, the deliberate way to
  start tracking the session you are in.
  - `_mt_enter_box_session` (the shared tail of enter-box / create-box /
    create-null / enter-mysetup / create-session) briefly called `sesh shell enter`
    so every box entered by hand became tracked. **Reverted** (myrig d7146f8):
    auto-creating a record on every box entry is exactly the record-every-session
    behaviour this design rejects — it mints a record per entry, throwaway ones
    included, when the premise is *recognise without recording*. Boxes are plain
    sessions again; they show in the `S` view as ghosts and are promoted
    deliberately. `sesh shell enter` remains available for anyone who wants the
    automatic behaviour explicitly.

**Conformance**: `shell.lifecycle`, `shell.promote`, `shell.gates` × (local,
remote) = 6 cells green over real tmux and a real ssh hop; the `tmux.info` cells
prove `session_path`; the `shells-view` TUI claim drives the real viewer through
promote and kill. Unit coverage for classification, the send-target resolver, the
auto-parent rules, and `TestMarkerScopesDoNotCollide` (real tmux) for the
inheritance trap.

**NOT built, deliberately: `realize` from a shell thread.** The spec listed it as
an optional "I was poking around by hand, now put claude on it". It is left out
because its semantics are genuinely unsettled: realize converts a record IN PLACE,
but a shell thread's session would then be left carrying a marker for a record
that is no longer a shell (a `stale` session), and refusing while the session is
live — the safe reading — makes the very flow it exists for awkward. The natural
flow already exists and is unambiguous: `thread new --into-session <the shell's
session> --cwd <dir>`, optionally with `--parent-shell` so the agent becomes the
shell thread's child. Revisit only with a decision about what happens to the live
session.
