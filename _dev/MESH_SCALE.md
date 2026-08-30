# sesh v2 — the mesh scale pass (design)

*Status: designed 2026-08-29 with Lukas, from the termux resource diagnosis (AGENTS.local.md
H99). Builds on `MESH.md` (which stays the mesh's base design record — this doc supersedes
its "per-machine JSON blob" storage section and the eventer's poll-the-world loop).*

---

## 1. The problem, measured

Archived threads are supposed to be pure history: kept forever, searchable, transferred
once. The **wire** already treats them that way (delta sync, H44). Every layer on either
side of the wire does not — each re-processes the *whole* thread set per tick, so cost
scales with TOTAL threads, not LIVE threads. Measured 2026-08-29 with mymain at 1,810
threads (1,763 archived, 47 live; snapshot payload 1.38 MB; whole-mesh ~1,990 threads,
1.48 MB):

| # | O(total) cost | where | measured |
|---|---|---|---|
| 1 | eventer decodes EVERY peer blob from SQLite **every 1s tick** and diffs a full prev-map | observer, `eventer.merged()` | **147 ms/s + 7.3 MB/s alloc on termux** = the phone's 15–18 % daemon CPU; 22 ms/s on mymain; macbook (hooks-pinned) 6.6 % on battery |
| 2 | `applyDelta` re-marshals the whole working set and rewrites the full blob per changed round | observer, meshsync | 44 ms + **1.4 MB flash write per active-cadence round** (88 KB/s sustained with the TUI open on the phone) |
| 3 | maintainer sweeps ALL records every 300 ms (`ListThreads(true)` + refreshThread + `DeepEqual` each) | owner | mymain: 1,810 rows read + ~6,000 DeepEquals/s forever |
| 4 | one-bit readers load the full blobs (mastermaint reachable 5s, fan-out offline set, subscriptions owner lookup) | observer | 1.4 MB SQLite read per question |
| 5 | `/v1/mesh` decodes all blobs per request, re-encodes ~1.5 MB, TUI decodes again | observer + client | with the TUI open the phone daemon doubles to 32 % |

At Lukas's stated design target — **tens of thousands of archived threads** — #1 alone
pegs the phone's daemon (~750 ms/tick at 10k). The standing rule constrains the fix: **an
optimization must never change what sesh shows** (H44; full replication stays — every
machine keeps every peer's complete set locally, archived included).

## 2. The design in one sentence

**Make "settled" first-class: store the peer cache as per-thread rows, feed every consumer
from the diffs the sync already computes, and sweep only live threads — so steady-state
work is O(live + Δ) and an archived thread costs a disk row and one slot in a single
in-memory view, nothing per-tick, on every machine.**

## 3. The changes

### C1 — per-thread peer cache rows (replaces the per-machine JSON blob)

Store migration (append-only, #23):

```
peer_threads ( machine TEXT NOT NULL, id TEXT NOT NULL,
               snapshot TEXT NOT NULL,          -- api.ThreadSnapshot JSON
               PRIMARY KEY (machine, id) )
peer_meta    ( machine TEXT PRIMARY KEY,
               synced_at_unix INTEGER NOT NULL, reachable INTEGER NOT NULL )
```

The migration converts existing `peer_snapshots` blobs via JSON1
(`json_each(payload)` — verified present in modernc.org/sqlite against a real fleet DB),
guarded by `json_valid` (a corrupt cache row is skipped, matching today's
skip-undecodable-blob behavior — the cache is derived data, resync heals it), then
**drops `peer_snapshots`**. Dropping is deliberate: a rolled-back binary then fails LOUDLY
("no such table") instead of silently serving a stale frozen blob — degrade toward loud,
never toward wrong. Rollback recipe (cache is disposable): recreate the empty table
(`CREATE TABLE peer_snapshots (machine TEXT PRIMARY KEY, synced_at_unix INTEGER NOT NULL,
reachable INTEGER NOT NULL, payload TEXT NOT NULL)`), old binary resyncs in seconds.
NB migration 12 rebuilt `threads` by copy-rename, which would silently DROP triggers on it
— C3's triggers live in this migration, and any FUTURE threads-table rebuild must recreate
them (comment at both sites).

A row change now costs one row upsert; the 1.4 MB-per-change rewrite (#2) and the
full-blob loads (#4) cease to exist. Writes go rows-first, view second (a crash between
leaves the store ahead; boot reseeds from the store — consistent).

### C2 — one shared view + diff-fed events (kills #1, #4, #5's decode half)

`internal/daemon/meshview.go`: ONE in-memory decoded copy of every peer's threads +
per-machine `{synced_at, reachable}` meta, seeded from `peer_threads` at boot (the single
remaining full decode — once per daemon start), updated by exactly the transitions
meshsync already performs: `replaceAll` (full fetch), `applyDelta`, `touch` (304/empty
delta), `markUnreachable`, `deleteMachine`. Each content transition:

1. persists the changed rows (store, first),
2. patches the view,
3. **emits `(old, new)` snapshot pairs** to the eventer.

The eventer loses its 1s poll-the-world ticker entirely and becomes a pure consumer of
pairs: field-compare, fire hooks, record attachment flips, trigger `deliverSubscriptions`
on busy→idle — zero work when nothing changed, O(changed rows) when something did. The
LOCAL half comes from the maintainer: `publish()` already computes "changed"
(`DeepEqual`-gated for delta generations) — it now hands the pair over too, with the
first full sweep after daemon start emission-suppressed (the baseline, exactly today's
first-tick-absorbs semantics; peer rows present at boot seed the view silently the same
way). `merged()`/`prev` die; the eventer's only state is the attachFlip map.

Semantic refinement, deliberate: today's 1s sampling could MISS an edge pair shorter than
its tick (a sub-second busy dip = a coalesced non-event); diff-fed emission never misses
an edge — the exact property H44's hooks-pinning existed to protect, now held by
construction. Remote granularity is unchanged (the wire delta is still the peer's
snapshot at fetch time); local granularity = the maintainer's 300 ms publish, as today.

`/v1/mesh` serves from the view (no per-request decode; response marshal stays — its
elimination is the follow-up below). mastermaint/fan-out read the view's meta;
`peerMachineOf` reads the view. `doctor` reads meta. Nothing reads blobs anywhere.

Freshness meta becomes view-authoritative: `synced_at`/`reachable` update in the view per
round and persist on content transitions + a periodic meta flush (60 s) + clean shutdown
— a crash can only UNDER-claim boot freshness (safe direction; first sync round corrects
it ~1 s later). `markUnreachable` stays eagerly persisted (rare, tiny, keeps boot
reachability honest).

Memory, stated honestly: the view keeps every replicated thread decoded in RAM — ONE copy
(today there are three-plus: meshsync `working`, eventer `prev` + per-tick `cur`), ~2–4 MB
at today's ~2k threads, ~15–20 MB at 10k. Serving `/v1/mesh` from rows + a marshal cache
(RAM O(live)) is the 100k-scale follow-up, alongside the client-delta protocol
(`/v1/mesh?since=`) — ticketed separately, not built here.

### C3 — O(live) maintainer sweep (kills #3)

A **settled** thread — no pane, no shell session, no in-flight headless turn, no authority
entry — can only change through the daemon's own store writes, or by a hold deadline
passing. So:

- Migration #23 also adds `revs(name TEXT PRIMARY KEY, rev INTEGER)` + AFTER
  INSERT/UPDATE/DELETE **triggers** on `threads` and `tickets` bumping `revs('threads')`.
  Triggers, not per-callsite bumps: every future write path is covered structurally — the
  missed-dirty-path class (stale snapshot = plausible-but-wrong) is closed at the schema.
- Per tick the maintainer reads one integer (`revs`). Unchanged rev ⇒ it sweeps ONLY the
  unsettled set, derived fresh each tick from the runtime probes it already runs
  (`RuntimeIndex` panes + shell sessions, `hlInFlight`, authority entries) — so even a
  hand-stamped pane marker (no record write) is caught within a tick, exactly as today.
  The record list is cached between rev changes (no `ListThreads` per tick either).
- Changed rev, or `now >= nextHoldExpiry` (recomputed each full sweep by `nextHoldFlip`:
  the min future instant at which OnHold flips with NO record write — an effective hold
  lapsing, or, since schema 48, a hold RELEASE lapsing, which snaps an ancestor's hold
  back on) ⇒ one FULL sweep, as today. Record mutations are human-scale — full sweeps become rare instead of 3.3/s.
- Counters (`fullSweeps`, `threadsSwept`) make the property test-observable, the
  `probedPanes` precedent.

`m.st` (every local snapshot in RAM) stays — it serves `/v1/snapshot` and deltas; same
follow-up note as the view.

### C4 — periodic divergence reconcile (belt and braces, Lukas's addition)

Delta application errors are designed to degrade to full refetch, but a silent bug in
that chain would replicate wrongness quietly. Every `reconcileInterval` (1 h) per
http+cursor peer, meshsync computes the sha of its OWN view marshaled exactly as the
server does (`sortedSnapshotThreads` + `json.Marshal` — same struct type, deterministic,
map keys sorted by Go) and sends it as `If-None-Match` with no cursor: `304` = provably
byte-identical (~100 B); `200` = divergence — LOUD log naming the machine + the full
payload is right there in hand to heal with (replaceAll → diff → the missed events fire).
Zero API change — it's the existing conditional GET. ssh-transport peers full-fetch every
round and are inherently reconciled.

## 4. What deliberately does NOT change

- **What sesh shows.** Full replication, archived rows everywhere, offline browsing,
  `/v1/mesh` shape, every CLI/TUI surface, api schema 47 (no wire change; C4 reuses the
  ETag flow). Mixed fleet trivially safe — the store change is machine-local.
- The wire protocol (delta/ETag/cadence) and its cells.
- The ssh transport (full-fetch per round; O(N) per round by nature — it is the
  bootstrap/compat path; scaling is the http path's job).
- A removed peer's cached threads persisting in the mesh view (today's behavior;
  `deleteMachine` exists for a future peer-remove hookup).
- The rejected alternatives stay rejected: archived-slim (H44 — changes what sesh shows);
  write-behind blob checkpoints (the interim "fix 2" — mitigates #2's frequency but keeps
  every O(total) cost and adds crash-window event-replay semantics; per-thread rows make
  eager writes cheap and exact instead. Superseded before it was built).

## 5. Test plan (the honesty section)

Invariants, each with a test that fails when violated:

- **No event edge lost, ever** — unit: pairs through every view transition
  (full/delta/removal/reachable-flip) and the maintainer emission incl.
  baseline-suppression + restart-diff-against-seeded-rows; e2e: the real-agent
  `thread.notify` cells (local+remote) stay green. Anti-gaming: neuter the view's emit →
  the notify cell and the eventer units go red.
- **Replication completeness** — the existing `mesh.snapshot(.http)` full-replication
  guard + `mesh.delta-sync.http` counting proxy stay green untouched.
- **Offline browsing incl. cold boot** — NEW test: peer down + observer daemon
  RESTARTED ⇒ the peer's threads still listed from the row-seeded view (this is what the
  persisted cache exists for; write-behind meta must not break it). Anti-gaming: drop the
  boot seed → red.
- **Cursor/base coherence** — cond state and view/rows live under one component; the
  remove path (`deleteMachine`) clears them together (rewrite of
  TestMeshSyncMissingRowRefetches to drive that path honestly).
- **O(live+Δ) work** — counters: maintainer `threadsSwept` stays O(live) across ticks
  with hundreds of archived records present; store rows written per steady round = 0.
  Unit-level (the `probedPanes` pattern); the wire cell already pins O(Δ) transfer.
- **Migration** — unit on a synthetic blob DB (rows land, corrupt blob skipped, old
  table gone, version 23) + REHEARSAL against copies of real fleet DBs (termux's 1,987-
  and mymain's 1,810-thread stores) asserting row-for-row equivalence with the decoded
  blobs, before any live DB migrates.
- **Race** — `-race` across internal/daemon (new shared view) + the full TUI claims
  suite + blast-radius cells (`route.parity`, `daemon.mesh-read`, `api.tcp-parity`,
  hooks/notify, mesh rows both transports).
- **Staging mesh + A/B** — parallel `~/.sesh-staging` daemons fleet-wide (own sockets,
  port 7879, own token, own peers.json; never supervised), synthetic 10k–50k archived
  corpus + churn driver; A/B CPU/write measurement on termux against the real corpus
  (read-only authenticated GETs), same /proc methodology as the H99 diagnosis.

## 6. Deploy

Store migration ⇒ daemon rebuild + supervised restart per machine (termux per its
recipe); no wire/schema change ⇒ any order, mixed fleet safe. The migration runs at first
start of the new binary (measured 1.2 s on the phone against a copy of its real
1,987-row cache).

**Deployed 2026-08-29 to all six machines (main 69db27c); live post-deploy numbers and the
per-machine verification are in AGENTS.local.md H99.**

**Follow-up deployed all six 2026-08-29 (main 1db958e): batched maintainer pane captures +
targeted Linux `/proc` walks, and migration 24's `peer_threads WITHOUT ROWID` / deferred meta
touch; pre/post history counts and live CPU measurements are in AGENTS.local.md H102.**

## 7. Measured (2026-08-29, A/B on termux against the real fleet corpus)

Two isolated staging daemons on the phone, one per binary, each syncing read-only from
the real five peers (1,988 threads), identical phases, identical conditions:

| | old (blob cache, schema 22) | new (schema 23) |
|---|---|---|
| idle CPU (background, nothing open) | 10.2 % of a core | **0.7 %** |
| active CPU (a TUI-shaped 3 s mesh poll) | 23.7 % | **3.2 %** |
| idle flash writes / 2 min | 4.4 MB | 287 KB |
| RSS | 62 MB | **36 MB** |

Active-phase writes stayed ~90–120 KB/s in BOTH runs because the full conformance
matrix was concurrently churning hundreds of real threads on mymain — genuinely
changed rows that must be written; the idle phase is the honest steady-state
comparison. Migration rehearsed row-for-row against copies of the real termux and
mymain stores (zero mismatches). Full matrix on the branch: 248/253, the 5 reds all
pre-existing (4 codex cells failing identically on the base commit under codex 0.151.0
— its headed-TUI sessions no longer resume, ticketed; 1 load flake passing serially).
