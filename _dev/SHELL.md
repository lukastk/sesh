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

**Stage 1 — BUILT.** `#{session_path}` carried through `tmux info` (api 47), and
the `shells` viewer (`S`, or the palette): every live tmux session on every
REACHABLE machine, classified `ghost` (no sesh identity) or `agent` (hosts a
thread-marked pane), with enter-to-nav and a confirmed kill. Enumerated live,
nothing recorded. Covered by `TestClassifySession`, `TestShellViewerKeys`, the
extended `tmux.info` matrix cells (both localities), and the `shells-view` TUI
claim (real sessions, real kill).

**Not yet built:** the shell-thread record itself (`agent_kind: "shell"`), the
`@sesh-shell-id` session marker, the taxonomy split, `sesh shell
new|enter|here|info|panes`, promotion of a ghost, the `shell`/`stale` classes,
the `▮`/`▯` head glyph, and the myrig `_mt_enter_box` substitution. See the
ticket's §13 for the staging.
