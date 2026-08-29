# sesh v2 — design specification

*Status: design. Informed by the mysetup consolidation review (see `REPORT.md`). This is the "what we are building" document. The "how we build and track it" lives in `_dev/PLAN.md`; the agent honesty/dev rules live in `AGENTS.md`.*

---

## 0. Thesis

sesh v2 is the single owner of the hard, performance-sensitive infrastructure for managing coding-agent work across machines. It absorbs three things that today live in separate places — the **tmux orchestration** (the cockpit, then `master-tmux.sh` in `myrig`), **session/thread management** (sesh v1), and **tickets** — into one Go binary + per-machine daemon.

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
- A separate socket **`mymastertmux`** can be started on any one machine. It holds **mycockpit** — "the cockpit": **one window per machine**, each window SSH'd into that machine's `mytmux` server (exactly as the pre-v2 cockpit did). This is a *view*, not a registry.
- The full address of any running process is **`(machine, socket, session, window, pane)`**.
- **No session persistence; records persist, runtime is re-derived.** This split is load-bearing, so state it plainly:
  - **Thread *records* persist** — they live in the daemon's SQLite store and survive reboots.
  - **Only the tmux *runtime* is ephemeral** — when a machine reboots, every tmux session disappears, and that is expected. The runtime (session/window/pane, liveness) is never stored; it is always re-derived live.
  - **A record whose session is gone is reported `headless·idle`, and is NEVER auto-deleted.** Pointing at a vanished session is not an error — it is just an idle thread (the unified no-runtime state), which can be revived on demand (`resume`/`headful`, a pane) or sent a headless turn (§3), or explicitly dropped. Garbage-collecting idle records is always a deliberate user action (`stop` then `delete`), never automatic.

### Commands (the contracts)

- `sesh tmux current` → resolve the locator of the calling terminal: machine, socket, session, window, pane, and (if present) the owning thread id.
- `sesh tmux info` → JSONL of all tmux sessions across all machines, with their windows, panes, and what is running in each. Filterable with `--machine` and `--session`. This is the cross-machine "walker"; it replaces v1's hand-rolled SSH poller + `~/.cache/mms/*.tsv`.
- `sesh tmux create-session` / `sesh tmux create-pane` → explicit primitives for building runtime structure.
- `sesh tmux nav --to <machine>:<session>` → **the navigation primitive.** From the cockpit, move to an exact session on another machine. This is the single fiddliest operation in the system and the reason this layer must be tested Go, not shell. It does, atomically:
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

- A thread's **runtime identity is its PANE** (the `@sesh-thread-id` marker, below), not its session. `session_name` is descriptive — recorded so the thread knows where it lives, but **not unique**: a tmux session may host MANY threads (their own windows, or splits in one window). *(Revised H13, 2026-06-14: the original "pinned to `(machine, session-name)`, one session per thread" had `UNIQUE(session_name)` and made `stop` kill the whole session; identity has since fully migrated to the pane marker — every runtime read already resolves `FindPaneByThreadID` — so the constraint was dropped, `stop` kills only the thread's pane, and placement modes let threads share a session. See `thread.placement*` / `thread.stop-shared` cells.)* Placement at spawn: default = the thread's own new session; `--into-session <name>` = a new window of an existing session; `--into-window <target>` = a split beside a pane; `--into-pane <pane>` = register-then-exec, binding the thread to an existing shell pane and running the agent in place.
- A thread stores its **`cwd`** (absolute path) — *not* a boxyard box id. Threads are frequently started outside the box yard; "is this cwd a box?" is an optional derived lookup that myrig can decorate the UI with, never a dependency.
- A **headed** thread (agent runs in a visible pane) has its pane **resolved at runtime, never stored**. At spawn, the pane is stamped with a marker — a tmux **pane user-option `@sesh-thread-id`** (invisible, survives pane moves/resizes, queryable in one `tmux list-panes -F` pass). Resolution = look up the pane bearing the thread's id. This same marker lets `sesh tmux current` answer "which thread am I in."
- **Headless/headful is NOT a stored mode — it is inferred runtime** (the unified model, 2026-06-10): a thread is a durable conversation; at any moment its runtime is a live pane, a headless turn in flight, or nothing (`idle`). `thread new --headless` just means "don't open a pane now"; an idle thread accepts EITHER revival verb — `resume`/`headful` (a pane) or `send-headless` (one stateless turn, `--resume`-continued).
- The thread carries an **`agent_kind`**: `claude | codex | pi`.
- **Runtime state is two *orthogonal* axes**, resolved by polling (never stored as truth) — *revised in Phase 3b after finding the original single enum conflated independent signals*:
  - **TWO orthogonal state axes (never a fused enum): `head = headful | headless` and `busy = busy | idle`.** `head` is the runtime FORM: `headful` = a live pane runs the agent; `headless` = no pane. `busy` is execution: `busy` = a turn is executing (a changing pane via content-diff, or a headless turn process via the registry); `idle` = quiet. The four states: headful·busy (pane mid-turn), headful·idle (agent at its prompt — needs-input), headless·busy (turn in flight — nothing to enter), headless·idle (no runtime — revive or send a turn). The old single `activity` enum fused these (waiting ≡ headful·idle, the old idle ≡ headless·idle) and `working` erased the head axis. `working`/`waiting` come from a **pane content-diff probe**: sample the pane's visible bytes across a short window; a pane that changes is mid-turn (`working`), a byte-stable pane is idle (`waiting`). This is **agent-agnostic** and observable. It is reliable because all three agent TUIs animate a live timer/spinner while a turn runs, so the pane is never byte-stable while working — even during a silent tool-run. (The earlier transcript-marker idea fails here: pi batch-writes its JSONL only at turn *end*, so a headed TUI turn is never observably "busy" that way. A per-agent transcript path is kept noted only as a *future fallback* for any agent later found to have genuinely non-animating silent turns.)
  - **`attachment` = `attached | detached`**, from `tmux list-clients`: is anyone currently viewing the session.
  - The axes are **independent**: a `detached` agent can still be `working`, and an idle agent still needs input whether or not it is being watched. Crucially, **ticket `needs-input` (§4) derives from `activity == waiting` *regardless of attachment*** — a not-currently-viewed idle agent still needs input; you just are not looking at it.
  - This is the same signal the TUI needs for status glyphs. The `working/waiting` distinction is the v1 codex-detection failure region; its test asserts both directions of the real transition (and that `working` is detected while `detached`).
- At spawn, **`SESH_THREAD_ID` is injected into the pane environment** so an agent can identify itself (`sesh ticket list --thread $SESH_THREAD_ID`).

### Stored schema (persistent)

`thread { id, machine, session_name, cwd, agent_kind, name, tags[], archived, agent_session_id }`
Runtime-resolved (not stored): `pane`, `runtime_state`, liveness.

- **`agent_session_id`** is the agent's *own* conversation id, captured at/after spawn (pi/claude are launched with a sesh-assigned id; codex mints its own on the first turn and is discovered after). It is what makes `resume` possible.
- **The record is the durable thing; it persists in SQLite and is never auto-deleted** (see §2). A record outliving its tmux session is a `headless·idle` thread, not garbage.

### Operations

start (headed/headless), send a message (headful → live pane; headless → a turn), list, **rename**, **tag**, and the lifecycle verbs that act on the *record* and/or the *runtime*:

These are **orthogonal primitives** over two independent axes — the *record*
(exists until `delete`) and the *runtime* (`stop` ↔ `resume`). `kill` is NOT a
primitive here: it is the composite `stop && delete`, which belongs in a myrig
wrapper, not in sesh (mechanism, not UX).

| verb | record | runtime (agent + tmux session) | use |
|---|---|---|---|
| **stop** | kept (becomes `headless·idle`) | ended | free the agent/pane now, keep the thread to revive later |
| **delete** | dropped | left untouched (refuses a LIVE thread unless `--force` — deleting a live thread orphans its agent) | forget a (usually already-dead) record |
| **archive** | kept, hidden from the active list | untouched | park it — a keepable state, distinct from `idle` and from `deleted` |
| **resume** | kept | **revived**: recreate the tmux session and relaunch the agent with `--resume <agent_session_id>` so the conversation continues | bring any **idle** thread back into a pane on demand (`headful` is the same operation) |

`resume` applies to headed threads and works after *any* death (each agent persists
its conversation incrementally) — **provided the daemon spawns the agent as a clean,
top-level session**: it scrubs inherited agent-harness env (`CLAUDECODE`,
`CLAUDE_CODE_*`, …) so a spawned claude does not behave as a nested session and stop
persisting its transcript. A **codex** thread that died *before its first turn* has no
`agent_session_id` (codex cannot pre-assign one), so it legitimately cannot be resumed
— a justified **N/A** edge, surfaced as an explicit error, never faked.

As v1, mutations route to the owning machine's daemon (single writer), which executes locally; the TUI/CLI reach remote machines via `--machine` routing.

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
- **`needs-input` is not a stored state — it is a derived view:** `status == active AND thread.activity == waiting` (the `activity` axis of §3, **regardless of `attachment`** — a detached idle agent still needs input). (A `headless·idle` thread on an undone ticket is "needs-revival", not "needs-input" = `headful·idle` — single-axis predicates, no decoding.)

### Responsibilities split

- sesh provides: create/read/update tickets; `sesh ticket send-prompt --to-thread` (deliver the ticket's prompt to its bound thread); `sesh ticket list --thread <id>` (what an agent is assigned); the agent may call `sesh ticket set-status done`.
- sesh does **not** track "was the prompt sent?" — there is no such state. Attaching a ticket to a thread and sending the prompt is **myrig's** job (an easy shell+keybinding flow). sesh just makes the underlying actions clean and explicit.

### Blobs — files & images in prompts (added 2026-06-15)

A prompt is text, so a **file** (image, log, anything) is referenced by a token and expanded to a real path on delivery; the agent reads the path (claude's Read tool / codex `-i` / pi `@path` all ingest a file/image from a path).

- **Store:** content-addressed, pure filesystem — `<SESH_HOME>/blobs/<sha256>/<name>`. The hash dir is the content address (identical bytes dedup); the file keeps its original name so the path has a real extension. No DB/schema. Exposed via daemon endpoints (routes per `--machine` like everything).
- **Token:** `@blob(<hex-prefix>)` — a prefix of the content hash (stable across machines: same bytes → same hash → same prefix), resolved by prefix. Escape a literal with `@@blob(…)`.
- **Expansion** (`blob expand`, and automatic on `ticket send-prompt` / `thread send` / `send-headless`, and on the cockpit's copy-prompt): each token → the blob's absolute path on the target daemon. A token resolving to **no blob is a loud error**, never sent verbatim.
- **`sesh ticket move --id --to [--from]`** is **daemon-coordinated**: the invoked daemon (the hub — only it must reach both ends) pulls the record *and every `@blob()` its prompt references* from the source and pushes them to the destination, then deletes the source (never before the push succeeds). This is what keeps a ticket co-located with its bound thread when a cross-machine bind relocates it — with its files carried along. (Cross-daemon data movement is the daemon's job, not a CLI script.)

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

- **Explicit, no magic defaults.** Every CLI invocation specifies what it means, either with a flag or an explicit user-configured policy. A behavior-changing omission is accepted only when the corresponding config value is present (for example, `thread new` may omit `--agent` only when `[defaults] agent` names `claude`, `codex`, or `pi`); otherwise it fails loudly. There are no built-in defaults that silently shift (v1's `--machine local` sentinel, which meant different things across versions, is the anti-pattern).
- **Output is a contract too:** structured (`--json`), stable, **versioned schema**.
- **The CLI is for machines and wrappers, not for ergonomic human typing.** Ergonomics live in myrig shell functions. Explicit config may supply user-owned policy, while flags always remain available as per-call overrides.

### Example myrig wrappers (thin, illustrative — these live in myrig, not sesh)

- `sesh-enter-local-session` — fzf over local tmux sessions; enter the choice.
- `sesh-enter-session` — fzf over all sessions across all machines (columns show the machine); enter the choice in the cockpit via `sesh tmux nav`.

---

## 7. What this deletes from the current setup (traceability to the review)

- The v1 `master-tmux.sh` SSH poller + all `~/.cache/mms/*.tsv` caches (R1) → replaced by `sesh tmux info` / the mesh.
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
