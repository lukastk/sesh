# IDLE_STOP — stop threads that have sat idle too long

*Design, 2026-11-11; built the same day (schema 66, store migration 32) on top of
[issue #24](https://github.com/lukastk/sesh/issues/24) (background work counts as busy, schema 65).
Read before changing idle-stop or what resets a thread's idle clock. User-facing contract:
[skills/sesh-cli/references/threads.md](../skills/sesh-cli/references/threads.md), "Idle stop".*

## 1. What it does

A headful agent thread that has been idle for longer than a threshold is **stopped**: exactly what
`sesh thread stop` does, without `--force`. Its pane and agent process end; the record, the
conversation, flags, archive state, tags, holds and parentage are untouched. The thread becomes
headless·idle and comes back with `thread resume` (or a schedule's revive).

Idle-stop never archives, unflags or deletes anything.

## 2. Configuration

```toml
[idle_stop]
after = "24h"   # absent = idle-stop is off on this machine
```

- The key lives in the shared `config.toml`; a machine may override it in its
  `config.local.toml` overlay (e.g. a shorter limit on a low-memory machine).
- There is no built-in default: without the key nothing is ever stopped.

**Per-thread override** (stored on the thread record, so it needs an append-only store migration
and an API schema bump):

| Value | Meaning |
|---|---|
| unset | use the machine's `[idle_stop] after` |
| `never` | never idle-stop this thread |
| a duration | use this limit instead of the machine's |

CLI surface (`POST /v1/threads/idle-stop`, `GET /v1/idle-stop/preview`):

```
sesh idle-stop set (--never | --after <dur> | --inherit) [--id <id>]
sesh idle-stop preview [--json]   # what would be stopped now, and why each other thread is not
```

A per-thread duration applies even on a machine with no `[idle_stop] after`.

## 3. Who decides, and when

The **owning daemon** sweeps its own threads (every quarter of the machine limit, between 1s
and 30s; `idlestop.go`), reading the maintainer's published snapshots, and calls the same
internal stop path as `POST /v1/threads/stop` (`stopAgentPane`). Nothing crosses the mesh; a peer's threads
are that peer's business, under that peer's config.

## 4. The idle clock

A thread's last activity is the latest of:

1. `lastActive`: the last pane change or reported in-flight turn. With #24, background work keeps
   the thread busy, and a busy thread bumps `lastActive` every tick.
2. `AttachedActivityUnix`: the last **input** from an attached client. Mere attachment does not
   count: cockpit clients sit attached to sessions indefinitely.
3. When the sweep first saw the thread live. The maintainer counts its first capture of a pane
   as a baseline, not a change, so a pane static since then (an agent resting at its prompt
   since launch — codex reliably — or every pane after a daemon restart) publishes no
   `LastActiveUnix`. The first-seen time lives in the sweep (`idleSeen`), not in
   `LastActiveUnix`, because the browser shows that field as a row's "updated" time.
4. The last message delivered to the thread. A delivered paste changes the pane, so this is
   covered by (1) today; keep it explicit if delivery stops touching the pane.

`lastActive` is in-memory: a daemon restart resets every thread's clock to the restart. This can
only **delay** a stop, never cause an early one, so it is accepted rather than persisted.

## 5. Never stopped

| Case | Why |
|---|---|
| Busy (a turn in flight, or background work per #24) | Work is running. |
| Blocked on a question or permission prompt | Claude reports these as blocked mid-turn, so the thread is busy and its clock does not run. Codex and Pi/mypi do not report blocked (deferred / not applicable), so for them a pending prompt reads idle and is subject to the limit. |
| Headless | There is no runtime to stop. A headless turn in flight is busy. |
| Shell threads | Busy is pinned idle for shells, so a long build would look idle; and stopping a shell kills its whole session, including other threads' agent panes inside it (`thread stop` refuses that without `--force`). |
| Pending in-session wake-ups (`session_crons`, from #24) | These are held by the agent process and die with it. Each wake-up resets the clock, so this only matters for a wake interval longer than the limit, but stopping would silently cancel it. |
| Override `never` | The user said so. |

## 6. Visibility

- The thread record gains **why and when it was idle-stopped** (e.g. `idle-stopped after 24h`),
  cleared when the thread next starts. The TUI and `info` show it, so an automatic stop is never
  mistaken for a crash.
- Every stop is logged by the daemon with the thread id, the idle duration and the limit that
  applied.
- `sesh idle-stop preview` lists each live thread with its verdict and reason.

## 7. Known costs

- Text typed into an agent's prompt but not sent is lost when the pane dies.
- A claude thread that never took a turn has no conversation on disk, so `thread resume`
  fails after any stop ("No conversation found"); there was nothing in it to lose.
- For Codex and Pi/mypi, an unanswered prompt is lost as a live prompt (the conversation keeps
  it).

## 8. Testing

Register `thread.idle-stop` × (local, remote) × (claude, codex, pi). Each cell uses a real agent
with a limit of a few seconds and asserts:

- **Stopped:** the pane's agent process is dead, the record and its flags are intact, and the stop
  reason is recorded.
- **Not stopped:** a busy thread, a thread with recent attached input, and a thread with override
  `never` all survive the limit.
- **Recovery:** `thread resume` brings the conversation back.

Unit tests cover the eligibility table (§5) and the clock (§4).
