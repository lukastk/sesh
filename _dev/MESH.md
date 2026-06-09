# sesh v2 — mesh-replicated live state (design)

The killer feature: `sesh tui` shows **every thread across every machine, with live
state** (headful/headless, idle/busy/dead, attached/detached), refreshes near-instantly,
and is **navigable** — select any thread on any machine and jump to it. It must stay
responsive even when a machine is offline or slow.

This doc pins down the architecture before code. It extends `_dev/SPEC.md` §2/§3
("threads are host-owned, **mesh-replicated for listing**") and §8 (read-through caches,
mobile → a remote always-on daemon). Build it in phases (A → B → C); each phase lands as
registered, real-tested matrix cells (AGENTS.md honesty rules apply unchanged).

---

## 1. The core idea: decouple *compute*, *replicate*, and *serve*

The only expensive signal is **activity** (busy/idle) for headed threads — derived from a
pane content-diff. Everything else is cheap metadata. So the whole design follows from two
rules:

1. **Never compute state on the hot path.** Each daemon maintains its own threads' state
   continuously in the background; a query is an O(1) read of the last-computed snapshot.
2. **Replicate, don't fan-out-per-query.** Each daemon keeps a local cache of every peer's
   snapshot, refreshed in the background. The TUI reads the merged local cache — instant,
   offline-capable, peer-latency-independent.

Three independent loops, three independent cadences (none blocks another):

| loop | where | cadence (default, tunable) | job |
|---|---|---|---|
| **L1 local maintainer** | every daemon | ~300ms tick | keep *own* threads' full live state fresh (rolling probe) |
| **L2 mesh sync** | every daemon | ~1s per peer | pull each peer's snapshot into the local cache |
| **L3 client render** | TUI/CLI/mobile | ~500ms–1s | poll the local daemon's *merged* snapshot (O(1) read) |

Probing happens **once per machine in L1** — never per-query, never per-machine-per-query.
A remote thread's busy/idle is at most ~L1+L2 (~1.3s) stale; a local thread ~L1 (~300ms).

---

## 2. The snapshot: what one thread carries

The unit of replication is a self-contained row — the TUI renders it with no extra
round-trip. (Superset of today's `api.ThreadRow`.)

```
ThreadSnapshot {
  // identity (from the record)
  id            string
  machine       string        // owning machine
  name          string
  agent_kind    string        // claude | codex | pi
  headless      bool          // headful vs headless
  session_name  string        // for nav
  cwd           string
  tags          []string
  archived      bool

  // live state (maintained by L1)
  activity      string        // working | waiting | dead
  attachment    string        // attached | detached   (headless: always detached)
  agent_running bool
  last_active_unix int64       // last pane change / last turn completion — for "idle 5m" + sort
}
```

A machine's snapshot is `MachineSnapshot { machine, generated_at_unix, threads[] }`.

The merged view the TUI consumes:

```
MeshSnapshot {
  machines []MachineView {
    machine        string
    reachable      bool        // is the cache entry fresh / was the last sync ok
    synced_at_unix int64       // when this machine's data was last refreshed (staleness)
    self           bool        // is this the local machine (always fresh)
    threads        []ThreadSnapshot
  }
}
```

Staleness is **explicit and per-machine** — the UI shows "macbook · 2s" (fresh) or
"macbook · ⚠ 5m (offline)" (stale, last-known). It never silently drops an offline
machine's threads, and never claims data is fresher than it is.

---

## 3. L1 — the local state maintainer (Phase A)

Replaces the current **on-demand** `paneChanging` (10 samples × 300ms = a 3s *blocking*
probe per status query) with a **continuous rolling probe**, so reads are instant.

Each ~300ms tick, for the machine's own threads:
- **headed, live pane:** `capture-pane` once; if the content hash changed since last tick,
  push a change event into a small per-thread ring buffer (timestamps of recent changes).
  - `activity = working` iff **≥2 changes within the last ~2s** (preserves today's
    "sustained change, not a one-off blip" robustness — rejects claude's rotating hints /
    MCP-startup flickers, catches codex's slow ~1s thinking timer), else `waiting`.
  - no marked pane / no agent of the right kind under it → `dead`.
  - `last_active_unix` = timestamp of the most recent change.
- **headless:** `working` iff a turn is in flight (the in-memory registry — instant, no
  capture), else `waiting`.
- **attachment:** one `list-clients` for the whole server per tick, matched to sessions in
  Go (not per-thread).

The maintained snapshot lives in memory (rebuilt on daemon start from records + first
probe). Cost: N `capture-pane` calls (~1–5ms each) per tick; trivial for realistic N. If N
ever gets large, throttle (stagger captures across ticks) — note it in `log`, never
silently sample a subset.

New endpoint: **`GET /v1/snapshot`** → `MachineSnapshot`, a pure read of the maintained
state (no probing). The local TUI and the mesh sync both consume this.

> Phase A already pays off with zero mesh/transport work: the *local* grid/TUI goes from a
> ~3s on-demand probe to a sub-second always-fresh read.

---

## 4. L2 — mesh sync + the replicated cache (Phase B)

Each daemon runs a background sync that, per peer (concurrently, with a per-peer timeout so
one slow/hung peer never stalls the loop), fetches `GET /v1/snapshot` and stores it.

**Cache (SQLite-backed, survives restart → instant cold start):**

```
peer_snapshots (
  machine        TEXT PRIMARY KEY,
  synced_at_unix INTEGER NOT NULL,   -- last SUCCESSFUL sync
  reachable      INTEGER NOT NULL,   -- was the most recent attempt ok
  payload        TEXT NOT NULL       -- the peer's MachineSnapshot JSON
)
```

Storing the snapshot as a per-machine JSON blob (not normalized rows) keeps it dead simple:
the merged view is assembled in memory by decoding each blob. A failed sync updates
`reachable=0` but **keeps the last good `payload`** (that is what makes offline browsing
work).

`GET /v1/threads/grid?all-machines` (and a new `GET /v1/mesh` returning `MeshSnapshot`)
read **local snapshot ∪ cached peers** — no ssh at query time. `sesh tui --all-machines`
points here.

### Reads from cache, writes to the owner (CQRS)

Threads are single-writer / host-owned, so replicas are read-only copies — **no conflict
resolution, no consensus**. Display comes from the cache; every *mutation* (`nav`, `stop`,
`send`, `resume`, `delete`, …) routes **live to the owning daemon** (the existing
`--machine` path), which is authoritative. Acting on an offline thread fails loudly. The
cache is strictly a display optimization.

### Transport: multiplexed ssh now, tailscale TCP later

Phase B uses the **ssh transport that already exists** (the `--machine` mesh) but turns on
**ssh connection multiplexing** for the sync's ssh-exec calls:
`-o ControlMaster=auto -o ControlPath=<run-dir>/ssh-%r@%h:%p -o ControlPersist=60s`. The
first connection to a peer sets up a master; subsequent ~1s fetches reuse it (~10ms instead
of a ~200ms handshake). Near-HTTP performance with **no new listener, no new auth, no new
network exposure** — it is just ssh. (Phase C adds the TCP transport; the snapshot schema
is identical, so nothing is wasted.)

---

## 5. L3 — the client (TUI)

The TUI becomes a pure renderer of `MeshSnapshot` from its local daemon:
- one row per thread, grouped/sortable by machine, with a status glyph (◐ working, ● waiting
  / needs-input, ✗ dead), a headful/headless marker, attachment, agent, name, tags;
- per-machine staleness badge (fresh age / offline last-seen);
- `enter` navigates to the selected thread (the existing cross-machine nav: master-window
  select + inner ssh `switch-client`); `x` stop, `d` delete, `a` archive route to the owner.

It polls `GET /v1/mesh` every ~500ms–1s — an O(1) local read — so refresh feels instant
regardless of how many machines or threads exist, and an offline machine just shows stale.

---

## 6. Topology

- **Desktops/laptops: P2P pull** over multiplexed ssh. N is tiny (4 machines); resilient,
  no single point of failure, low latency on tailscale.
- **Always-on hub (Phase C, with mobile): the ticket-owner node** (`SPEC §8`, the always-on
  `hetzner-box`/server) also aggregates the mesh snapshot. Daemons sync to it; **mobile
  queries the hub** and sees the whole rig even when your laptop is closed. Same snapshot
  schema; the hub is just another `MeshSnapshot` source.

---

## 7. Phase C — network exposure (only when mobile is wanted)

Currently the daemon listens on a **unix-domain socket only** — local clients only; nothing
remote, no mobile. The HTTP+JSON *handlers* all exist (CLI/TUI are already HTTP clients), so
this is exposure, not re-architecture:

- Add a **TCP listener bound to the tailscale interface**, behind a **bearer token** (config
  `SESH_API_TOKEN` / a file), serving the same router.
- Unlocks **mobile / Obsidian → a remote always-on daemon** (`SPEC §5`), and becomes the
  nicer mesh-sync transport (persistent HTTP over tailscale instead of ssh-exec).
- Security surface (auth, exposure) is real — this is the one decision gated on Lukas; it is
  deliberately **separable** so Phases A/B ship the killer desktop feature without it.

---

## 8. Failure modes (designed-for)

- **Slow/hung peer** — per-peer timeout + concurrent fetches; one peer never stalls L2.
- **Offline peer** — `reachable=0`, last-good `payload` retained → still listed, shown stale.
- **Daemon restart** — L1 rebuilds from records + first probe; L2 cache reloads from SQLite
  (instant), refreshes on next tick.
- **Clock skew across machines** — staleness uses each row's `synced_at` measured by the
  *consuming* daemon's clock (when it fetched), not the peer's, so skew can't fake freshness.
- **Stale action** — mutations hit the live owner, which 404s/conflicts if the thread is
  gone; the TUI surfaces it and re-syncs.

---

## 9. Matrix cells per phase

Each phase registers real e2e cells (honesty rules unchanged):

- **Phase A**
  - `thread.snapshot` (agentic, L) — `/v1/snapshot` reflects REAL state and FLIPS
    waiting→working across a real turn, served without an on-demand probe (assert latency:
    a second read during a turn is fast, not a 3s block).
  - `thread.runtime-state` continues to pass (the maintainer is the new source).
- **Phase B**
  - `mesh.snapshot` (R) — the merged view includes a peer's threads with correct state +
    owning machine, read from cache (no per-query ssh).
  - `mesh.offline-listing` (R) — a peer's daemon goes down → its threads are STILL listed,
    marked stale/unreachable (offline browsing), and a recovered peer refreshes.
  - `mesh.staleness` (R) — `synced_at`/`reachable` reflect reality (kill sync → goes stale;
    resume → fresh).
- **Phase C** (when built)
  - `api.tcp-auth` — the daemon serves over TCP with a token (rejects no/!wrong token,
    accepts correct); a remote client reads the mesh over it.

The TUI conformance track gains a `mesh-render` claim (the rendered grid reflects the real
merged snapshot incl. a stale/offline machine), reality-anchored as today.

---

## 10. Build order

1. **Phase A** — L1 maintainer + `/v1/snapshot`; repoint local grid/TUI; `thread.snapshot`
   green. (No mesh, no transport — immediate local win.)
2. **Phase B** — L2 sync + `peer_snapshots` cache + `/v1/mesh`; multiplexed ssh; repoint
   `tui --all-machines`; `mesh.*` cells green. **This is the killer feature.**
3. **Phase C** — tailscale TCP listener + token auth (+ optional hub) for mobile. Gated on
   the security decision; separable.
