# sesh v2 — design specification

*Status: design. Informed by the mysetup consolidation review (see `REPORT.md`). This is the "what we are building" document. The "how we build and track it" lives in `_dev/PLAN.md`; the agent honesty/dev rules live in `AGENTS.md`.*

---

## 0. Thesis

sesh v2 is the single owner of the hard, performance-sensitive infrastructure for managing coding-agent work across machines. It absorbs three things that today live in separate places — the **tmux orchestration** (master-tmux in `myrig`), **session/thread management** (sesh v1), and **tickets** — into one Go binary + per-machine daemon.

The governing split:

- **`sesh` = mechanism + contracts + the TUI.** It owns the fiddly tmux nesting, the cross-machine state, the data model, and anything performance-sensitive. It exposes explicit, stable, machine-readable contracts.
- **`myrig` shell + config = policy + UX.** Keybindings, fzf pickers, popup menus, and the exact ergonomics are thin shell wrappers around the `sesh` CLI. They may **only** call `sesh` + `fzf` + `tmux`; they must never re-encode logic that `sesh` owns (naming, resolution, routing).

This is deliberately the inverse of the v1 failure mode, where a 1,524-line `sesh.sh` glue layer re-absorbed all the coupling the Go binary was built to escape.

---

## 1. The layers

Four layers, bottom to top: **tmux → thread → ticket**, all served by a per-machine **daemon/API**.

```
ticket layer   unit of work: name, prompt, status, optional thread binding
   │            owner = a single canonical (always-on) node; read-cached elsewhere
thread layer   durable handle to an agent, pinned to (machine, session-name)
   │            owner = the machine it runs on; mesh-replicated for listing
tmux layer     addressing + manipulating runtime: (machine, socket, session, window, pane)
   │            never persisted; re-derived live
daemon/API     per-machine; single-writer-per-record + read replicas; HTTP+JSON for clients
```

### Ownership model (the one consistent rule)

**Every record has a single authoritative writer; other machines hold read replicas.** The two ownership flavors are not two architectures — they differ only in whether a record is *born with* an owner:

| Data | Born with an owner? | Owner | Why |
|---|---|---|---|
| tmux runtime | n/a (not stored) | the machine it's on, polled live | intrinsically local; wiped on reboot |
| **thread** | yes — the host machine | the machine it runs on; mesh-replicated for listing | a thread is unreachable when its machine is offline anyway, so local ownership loses nothing |
| **ticket** | no — created anywhere, reassigned across machines, multi-writer over its life | a **single designated always-on node**; read-cached everywhere | a ticket has no natural host; one writer = trivially consistent, kills the v1 vault-sync race, and is always reachable (incl. mobile) |

---

## 2. tmux layer

### Model

- On each machine, the agent/thread tmux server runs on socket **`mytmux`** (renamed from v1's `mysystem`). `mytmux` is *just a regular tmux server* — it carries no semantics of its own; sesh imposes meaning, not the socket name.
- A separate socket **`mymastertmux`** can be started on any one machine. It holds the "master" view: **one window per machine**, each window SSH'd into that machine's `mytmux` server (exactly as the current master-tmux does). This is a *view*, not a registry.
- The full address of any running process is **`(machine, socket, session, window, pane)`**.
- **No session persistence.** When a machine reboots, all tmux sessions disappear, and that's fine. Thread *records* persist; their runtime (the session) is always re-derived. A thread record pointing at a session that no longer exists is not an error — it's a dead thread.

### Commands (the contracts)

- `sesh tmux current` → resolve the locator of the calling terminal: machine, socket, session, window, pane, and (if present) the owning thread id.
- `sesh tmux info` → JSONL of all tmux sessions across all machines, with their windows, panes, and what is running in each. Filterable with `--machine` and `--session`. This is the cross-machine "walker"; it replaces v1's hand-rolled SSH poller + `~/.cache/mms/*.tsv`.
- `sesh tmux create-session` / `sesh tmux create-pane` → explicit primitives for building runtime structure.
- `sesh tmux nav --to <machine>:<session>` → **the navigation primitive.** From `mymastertmux`, move to an exact session on another machine. This is the single fiddliest operation in the system and the reason this layer must be tested Go, not shell. It does, atomically:
  1. switch the **outer** (`mymastertmux`) client to machine M's window, **and**
  2. drive machine M's **inner** `mytmux` server (over that window's SSH) to `switch-client -t <session>`, **and**
  3. handle the **detached-pane case**: if the target session has no attached client to switch, perform the "bare-shell kick" (attach/select so a client exists, then switch).

### Low-level primitives for myrig features (don't build the features here, enable them)

The current `prefix+P` image-paste (stage an image to the current terminal's machine, paste the path at the cursor) should be a ~5-line myrig shell function. sesh provides the substrate:

- `sesh tmux current` (above) — which machine/pane am I in.
- `sesh tmux stage-file --to <machine> <localpath>` → copy a local file to a temp location on machine M, print the staged remote path.
- `sesh tmux send-text <locator> <text>` → paste/send text into a pane at the cursor.

Rule of thumb: any time a myrig function would need to *know something sesh knows* (which machine am I on, how do I reach it, what's the staged path), that's a sesh primitive; the orchestration stays in myrig.

---

## 3. thread layer

("thread" = what v1 called a "sesh" — a managed agent.)

### Model

- A thread is **pinned to `(machine, session-name)`**, never to a pane. Panes move; sessions are the stable runtime anchor.
- A thread stores its **`cwd`** (absolute path) — *not* a boxyard box id. Threads are frequently started outside the box yard; "is this cwd a box?" is an optional derived lookup that myrig can decorate the UI with, never a dependency.
- A **headed** thread (agent runs in a visible pane) has its pane **resolved at runtime, never stored**. At spawn, the pane is stamped with a marker — a tmux **pane user-option `@sesh-thread-id`** (invisible, survives pane moves/resizes, queryable in one `tmux list-panes -F` pass). Resolution = look up the pane bearing the thread's id. This same marker lets `sesh tmux current` answer "which thread am I in."
- A **headless** thread (persistent child agent, no window) also lives in a session, just unattached.
- The thread carries an **`agent_kind`**: `claude | codex | pi`.
- A **runtime-state** enum, resolved by polling (never stored as truth): `working | waiting | dead | detached`. This is the same signal the TUI needs for status glyphs, and it is what makes the derived ticket states work (§4).
- At spawn, **`SESH_THREAD_ID` is injected into the pane environment** so an agent can identify itself (`sesh ticket list --thread $SESH_THREAD_ID`).

### Stored schema (persistent)

`thread { id, machine, session_name, cwd, agent_kind, name, tags[] }`
Runtime-resolved (not stored): `pane`, `runtime_state`, liveness.

### Operations

start (headed/headless), kill, send a message (headful: into the live pane; headless: as a turn), list, rename, tag. As v1, mutations route to the owning machine's daemon (single writer), which executes locally.

---

## 4. ticket layer

### Model

- A ticket is `{ id, name, description, prompt, status, thread_id? }`. `description` may be auto-generated.
- A ticket may be bound to a **thread**; a thread may have **multiple tickets**.
- **Statuses:** `triage → ready → active → done` (plus terminal `dropped`).
  - `triage` — exists; prompt not final; unattached.
  - `ready` — prompt final; deployable; unattached.
  - `active` — attached to a thread.
  - `done` — terminal (the agent may set this).
- **`needs-input` is not a stored state — it is a derived view:** `status == active AND thread.runtime_state == waiting`. (A `dead` thread on an undone ticket is "needs-restart", not "needs-input" — that's why the runtime enum distinguishes `waiting` from `dead`.)

### Responsibilities split

- sesh provides: create/read/update tickets; `sesh ticket send-prompt --to-thread` (deliver the ticket's prompt to its bound thread); `sesh ticket list --thread <id>` (what an agent is assigned); the agent may call `sesh ticket set-status done`.
- sesh does **not** track "was the prompt sent?" — there is no such state. Attaching a ticket to a thread and sending the prompt is **myrig's** job (an easy shell+keybinding flow). sesh just makes the underlying actions clean and explicit.

This is the v1 `tickets` thesis pushed all the way: *project a lifecycle onto threads; don't build a parallel world.* Folding tickets into sesh specifically **deletes `vaulthost.py`** — the SSH-re-exec vault-sync bridge existed only because "the note IS the database" collided with an eventually-consistent vault. With tickets as first-class single-writer sesh records, that whole class of bug (which lost a real ticket's binding mid-run) disappears. Obsidian integration becomes an **API client** (§5), not markdown-as-database.

---

## 5. daemon / API

- **Per-machine daemon**, as in v1: SQLite (WAL), owner is the single writer, clients hit only the local daemon socket; cross-machine reads via a peer mesh.
- **Client-facing API is HTTP + JSON** (plugin- and mobile-friendly). The CLI and TUI are themselves API clients. Inter-daemon mesh transport may stay gRPC if convenient, but the *client* surface is HTTP+JSON so an Obsidian plugin (desktop and mobile) can drive sesh by hitting a daemon's API directly (mobile → a remote always-on daemon).
- **Tickets** are owned by one designated always-on node (candidate: the always-on `hetzner-box` / a `myservers` host). Every machine keeps a read-through cache for offline browsing; writes go to the owner.
- **Threads + tmux** stay per-machine (host-owned), mesh-replicated for listing.

The TUI stays in sesh/Go because it is performance-sensitive (renders live cross-machine state).

---

## 6. CLI design principles

- **Explicit, no magic defaults.** Every CLI invocation specifies what it means; there are no behavior-changing defaults that could silently shift (v1's `--machine local` sentinel, which meant different things across versions, is the anti-pattern). A flag may default only when its value is a true invariant.
- **Output is a contract too:** structured (`--json`), stable, **versioned schema**.
- **The CLI is for machines and wrappers, not for ergonomic human typing.** Ergonomics live in myrig shell functions. This is *why* explicitness is acceptable: the wrappers supply the convenience.

### Example myrig wrappers (thin, illustrative — these live in myrig, not sesh)

- `sesh-enter-local-session` — fzf over local tmux sessions; enter the choice.
- `sesh-enter-session` — fzf over all sessions across all machines (columns show the machine); enter the choice in `mymastertmux` via `sesh tmux nav`.

---

## 7. What this deletes from the current setup (traceability to the review)

- The master-tmux SSH poller + all `~/.cache/mms/*.tsv` caches (R1) → replaced by `sesh tmux info` / the mesh.
- The v1 `sesh.sh` dead picker + `~/.sesh/cache/*.json` + `--machine local` path (R2).
- `spawn-agent-session` as a separate spawn door (R4) → `sesh` is the one spawn path.
- The session-naming-by-string-convention spread across 5–6 places (R3) → there is no name-encoded join anymore; box/note links are real fields/tags.
- `vaulthost.py` and the vault-sync race (tickets' only real fat) → single-writer ticket store.
- mysystem's `@flagged` tmux-option flagging (R9) → sesh tags.

---

## 8. Open decisions still owned by Lukas

1. **Which machine is the canonical ticket owner?** (Needs to be the most-always-on node; `hetzner-box` / a `myserver` is the obvious pick.) Everything else in the ticket layer follows from this.
2. **Box↔thread linkage:** confirmed as a stored `cwd` on the thread (not a boxyard dependency). Any richer box/note grouping stays in the vault as a projection.
3. **Does the agent write any ticket state beyond `done`?** Current design: agent may set `done`; everything else is human/myrig-driven; `needs-input` is derived. Confirm this stays minimal.
