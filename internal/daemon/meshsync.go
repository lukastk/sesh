package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/peers"
)

// The mesh sync (L2 in _dev/MESH.md) keeps a LOCAL cache of every peer's snapshot
// fresh in the background, so the cross-machine view (GET /v1/mesh) is a local read
// — instant, offline-capable, and independent of per-query ssh latency.
//
// Cadence is DEMAND-DRIVEN (issue #1: a fixed 1 Hz full-snapshot poll burned
// ~450 MB/hr of mobile data on the termux leaf): full 1 Hz while something is
// consuming the mesh view — a GET /v1/mesh read or an all-machines fan-out within
// meshActiveWindow — or when [[hooks]] are configured (the eventer diffs REMOTE
// snapshots continuously; at a slow cadence a remote turn shorter than the
// interval would produce NO busy edge and a notify hook would silently never
// fire). Otherwise one round per idleInterval, snapping back instantly on the
// next demand via kick() so the first mesh read after an idle stretch is fresh
// within ~an RTT, not one interval later.
const (
	meshSyncTick     = 1 * time.Second
	meshFetchTimeout = 8 * time.Second
	// meshActiveWindow is how long after the last mesh-view demand the full
	// cadence persists. Any live consumer (the TUI polls ~3s, sesh-ui similar)
	// re-bumps demand well inside it.
	meshActiveWindow = 60 * time.Second
	// meshKickDebounce: a demand bump only kicks an immediate round when the
	// previous demand is at least this old — an active consumer's steady polling
	// rides the 1 Hz ticker instead of stacking extra rounds.
	meshKickDebounce = 2 * time.Second
	// metaFlushInterval: how often the view's accumulated freshness meta (the
	// touch path — 304s/empty deltas) is persisted. Content transitions persist
	// their meta in-transaction; this only bounds how stale the BOOT-time
	// freshness can be after a hard crash (an under-claim — the safe direction).
	metaFlushInterval = 60 * time.Second
	// meshReconcileInterval: how often a quiet, cursor-synced peer is VERIFIED
	// against a content hash (_dev/MESH_SCALE.md C4) — belt and braces against a
	// silent delta-application bug replicating wrongness quietly. The check
	// rides the existing conditional GET (If-None-Match), costs ~100 B when
	// consistent, and heals with the full payload it gets back when not.
	meshReconcileInterval = time.Hour
)

type meshSync struct {
	d       *Daemon
	mu      sync.Mutex
	started bool
	stop    chan struct{}
	done    chan struct{}

	// ctx is canceled at stop so in-flight fetches abort promptly instead of
	// holding shutdown for up to fetchTimeout.
	ctx    context.Context
	cancel context.CancelFunc

	// fetchTimeout bounds one peer fetch (meshFetchTimeout; injectable in tests).
	fetchTimeout time.Duration
	// tickInterval is the loop's base tick (meshSyncTick; injectable in tests).
	tickInterval time.Duration

	// idleInterval is the cadence while nothing consumes the mesh view (from
	// [mesh] idle_interval; daemon.New wires it). The zero value means NEVER idle
	// — always full cadence — so a hand-built test daemon behaves exactly as
	// before this knob existed.
	idleInterval time.Duration
	// hooksPinned pins full cadence: [[hooks]] are configured, so the eventer is
	// a standing consumer of remote state (see the cadence comment above).
	hooksPinned bool
	// kickCh wakes the run loop for an immediate round when demand arrives while
	// idling (buffered 1; kick() never blocks).
	kickCh chan struct{}

	// inflight tracks peers with a fetch currently running, so a tick launches at
	// most one fetch per peer: a hanging peer's next fetch waits for its previous
	// one to time out, while every OTHER peer keeps its 1s cadence. wg tracks the
	// fetch goroutines for stopAndWait.
	imu      sync.Mutex
	inflight map[string]bool
	wg       sync.WaitGroup

	// Per-peer conditional-fetch state (all under emu; in-memory only — a daemon
	// restart just refetches full once). Invariant: a peer's remembered state
	// always corresponds to its STORED payload (updated only after a successful
	// upsert), so a 304/empty-delta always means "the cache is current".
	//   - cursors: the peer's delta-sync Generation (schema 41). While set, the
	//     fetch asks for a DELTA, applied onto the mesh VIEW (the in-memory
	//     decoded authority — _dev/MESH_SCALE.md; its per-thread rows are the
	//     durable base).
	//   - etags: the full-payload ETag — the fallback conditional for peers whose
	//     daemon predates delta sync (no Generation in its responses).
	//   - reconciledAt: when each peer's cached content was last PROVEN
	//     hash-identical to the owner (a full fetch counts; see reconcilePeer).
	emu          sync.Mutex
	etags        map[string]string
	cursors      map[string]string
	reconciledAt map[string]time.Time

	// Reused HTTP clients for peers on the http transport, keyed by addr+token, so
	// the ~1s sync keeps connections alive across ticks (the whole point of HTTP
	// over ssh-exec: hit the peer's running daemon, don't reconnect every tick).
	cmu     sync.Mutex
	clients map[string]*client.Client
}

func newMeshSync(d *Daemon) *meshSync {
	ctx, cancel := context.WithCancel(context.Background())
	return &meshSync{
		d: d, stop: make(chan struct{}), done: make(chan struct{}),
		ctx: ctx, cancel: cancel,
		fetchTimeout: meshFetchTimeout,
		tickInterval: meshSyncTick,
		kickCh:       make(chan struct{}, 1),
		inflight:     map[string]bool{},
		etags:        map[string]string{},
		cursors:      map[string]string{},
		reconciledAt: map[string]time.Time{},
		clients:      map[string]*client.Client{},
	}
}

func (s *meshSync) start() {
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	go s.run()
}

func (s *meshSync) stopAndWait() {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return
	}
	close(s.stop)
	s.cancel() // abort in-flight fetches instead of waiting out their timeouts
	<-s.done
	s.wg.Wait()
	s.d.view.flushMeta() // clean shutdown persists the touch-path freshness
}

func (s *meshSync) run() {
	defer close(s.done)
	t := time.NewTicker(s.tickInterval)
	defer t.Stop()
	s.tick()
	lastRound := time.Now()
	lastFlush := time.Now()
	for {
		select {
		case <-s.stop:
			return
		case <-s.kickCh:
			// Demand arrived while idling: sync NOW so the read that woke us is
			// stale for ~an RTT, not a whole idle interval.
			s.tick()
			lastRound = time.Now()
		case <-t.C:
			if time.Since(lastFlush) >= metaFlushInterval {
				s.d.view.flushMeta()
				lastFlush = time.Now()
			}
			if s.shouldSync(time.Since(lastRound)) {
				s.tick()
				lastRound = time.Now()
			}
		}
	}
}

// shouldSync decides whether this base tick performs a round: always at full
// cadence when idling is off (idleInterval 0), when [[hooks]] pin it, or while
// the mesh view is in demand; otherwise only once sinceLast reaches the idle
// interval.
func (s *meshSync) shouldSync(sinceLast time.Duration) bool {
	if s.idleInterval == 0 || s.hooksPinned {
		return true
	}
	if time.Since(s.d.lastMeshDemand()) <= meshActiveWindow {
		return true
	}
	return sinceLast >= s.idleInterval
}

// kick wakes the run loop for an immediate round (never blocks; coalesces).
func (s *meshSync) kick() {
	select {
	case s.kickCh <- struct{}{}:
	default:
	}
}

// cadence reports the current pace for observability (StatusResponse.MeshCadence).
func (s *meshSync) cadence() string {
	switch {
	case s.idleInterval == 0:
		return "always"
	case s.hooksPinned:
		return "hooks-pinned"
	case time.Since(s.d.lastMeshDemand()) <= meshActiveWindow:
		return "active"
	default:
		return "idle"
	}
}

// tick launches a fetch for every peer that doesn't already have one in flight.
// Each fetch writes its OWN result to the cache the moment it completes — there is
// deliberately no barrier here: gating all writes on the slowest peer meant one
// offline peer (a blackholed TCP dial hanging until fetchTimeout) stalled EVERY
// peer's cache update to ~8s, making the whole mesh view seconds stale whenever any
// machine was asleep. A hanging peer now only skips its own subsequent ticks (the
// in-flight guard); the others keep the 1s cadence. A failed fetch keeps the peer's
// last-known payload and only flips it stale (offline browsing).
func (s *meshSync) tick() {
	reg, err := peers.Load(s.d.cfg.PeersPath())
	if err != nil {
		return
	}
	for _, p := range reg.List() {
		s.launchSync(p)
	}
}

// launchSync starts one guarded fetch goroutine for a peer (skipped if that
// peer already has one in flight — the tick/nudge shared discipline).
func (s *meshSync) launchSync(p peers.Peer) {
	s.imu.Lock()
	if s.inflight[p.Machine] {
		s.imu.Unlock()
		return
	}
	s.inflight[p.Machine] = true
	s.imu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.imu.Lock()
			delete(s.inflight, p.Machine)
			s.imu.Unlock()
		}()
		s.syncPeer(p)
	}()
}

// nudgePeer syncs ONE peer immediately (schema 45, POST /v1/mesh/nudge): a
// routed write to that machine just succeeded, so its cached snapshot is
// provably stale — refresh it now instead of at the next cadence tick. Returns
// a loud error for an unknown machine (a typo'd nudge must not silently no-op).
// If a fetch for that peer is already in flight its result may predate the
// write; the handler's demand bump keeps the cadence active so the next ~1s
// tick covers that race.
func (s *meshSync) nudgePeer(machine string) error {
	reg, err := peers.Load(s.d.cfg.PeersPath())
	if err != nil {
		return err
	}
	p, ok := reg.Get(machine)
	if !ok {
		return fmt.Errorf("unknown machine %q: no peer registered (see `sesh peer add`)", machine)
	}
	s.launchSync(p)
	return nil
}

// syncPeer fetches one peer's snapshot and records the outcome. The SQLite writes
// serialize in the store (SetMaxOpenConns(1)), so concurrent completions are safe.
// The http fetch is CONDITIONAL: with a delta-sync cursor only the peer's CHANGED
// rows transfer (applied onto the cached set); without one, the remembered ETag
// makes an unchanged snapshot a bodyless 304. Any degradation goes toward a FULL
// refetch — never toward stale-but-fresh-looking data.
func (s *meshSync) syncPeer(p peers.Peer) {
	etag, cursor := s.condStateOf(p.Machine)
	if cursor != "" {
		etag = "" // one conditional mode at a time; the cursor wins while held
	}
	snap, newETag, notModified, err := s.fetchPeerSnapshot(p, etag, cursor)
	if err != nil {
		// A fetch aborted by shutdown says nothing about the peer — don't flip it
		// stale on the way out. Cursor/ETag survive an outage: they still match
		// the retained payload, and a restarted peer daemon rejects the old
		// cursor (new epoch) into a full resync anyway.
		if s.ctx.Err() != nil {
			return
		}
		s.d.view.markUnreachable(p.Machine) // next tick retries
		return
	}

	now := time.Now().Unix()
	switch {
	case notModified:
		// 304: the full payload is byte-unchanged — refresh freshness only.
		if !s.d.view.touch(p.Machine, now) {
			s.syncPeerFull(p) // cache entry vanished under us — full refetch
			return
		}
		s.maybeReconcile(p)
	case snap.Delta:
		if len(snap.Threads) == 0 && len(snap.Removed) == 0 {
			// Empty delta: nothing changed since the cursor. Same cost class as a
			// 304 (~100 B), same handling.
			if !s.d.view.touch(p.Machine, now) {
				s.syncPeerFull(p)
				return
			}
			s.rememberCursor(p.Machine, snap.Generation)
			s.maybeReconcile(p)
			return
		}
		ok, err := s.d.view.applyDelta(p.Machine, now, snap.Threads, snap.Removed)
		if err != nil || !ok {
			s.clearCondState(p.Machine)
			s.syncPeerFull(p) // no view base / write failed — full resync
			return
		}
		s.rememberCursor(p.Machine, snap.Generation)
	default:
		s.storeFull(p.Machine, snap, newETag, now)
	}
}

// maybeReconcile runs the C4 divergence check when due — and ONLY off a
// provably-quiet round (a 304 / empty delta), so a hash mismatch cannot be
// ordinary staleness: we are up to the peer's cursor and it reports no change,
// therefore our content hash must equal its ETag. A mismatch means the cached
// content drifted from what the owner serves (a delta-application bug, a
// corrupted row) — logged LOUDLY and healed with the full payload in hand.
// A never-quiet peer defers verification; any full fetch also counts as
// reconciled (it IS the ground truth).
func (s *meshSync) maybeReconcile(p peers.Peer) {
	s.emu.Lock()
	last := s.reconciledAt[p.Machine]
	s.emu.Unlock()
	if time.Since(last) < meshReconcileInterval {
		return
	}
	ths, ok := s.d.view.threadsSorted(p.Machine)
	if !ok {
		return // no cached content — nothing to verify
	}
	localHash := snapshotETag(ths)
	if localHash == "" {
		return // marshal failure: skip, never a false verdict
	}
	snap, newETag, notModified, err := s.fetchPeerSnapshot(p, localHash, "")
	if err != nil {
		if s.ctx.Err() != nil {
			return
		}
		s.d.view.markUnreachable(p.Machine)
		return
	}
	now := time.Now().Unix()
	if notModified {
		s.d.view.touch(p.Machine, now)
		s.markReconciled(p.Machine)
		return
	}
	log.Printf("mesh reconcile: cached content for %s does NOT match its owner's hash right after a quiet round — healing with the full payload (possible delta-sync drift; investigate if recurring)", p.Machine)
	s.storeFull(p.Machine, snap, newETag, now)
}

func (s *meshSync) markReconciled(machine string) {
	s.emu.Lock()
	s.reconciledAt[machine] = time.Now()
	s.emu.Unlock()
}

// syncPeerFull drops the peer's conditional state and refetches the whole
// snapshot in the same round — the recovery path whenever incremental state
// can't be trusted (missing cache row, missing working base, failed write).
func (s *meshSync) syncPeerFull(p peers.Peer) {
	s.clearCondState(p.Machine)
	snap, newETag, _, err := s.fetchPeerSnapshot(p, "", "")
	if err != nil {
		if s.ctx.Err() != nil {
			return
		}
		s.d.view.markUnreachable(p.Machine)
		return
	}
	s.storeFull(p.Machine, snap, newETag, time.Now().Unix())
}

// storeFull records a full snapshot response into the view (rows + diff +
// events) and (re)establishes the conditional state: the cursor when the peer
// speaks delta sync (schema 41+), else the ETag. State updates only after the
// payload is safely stored — and a full payload is ground truth, so it also
// counts as reconciled.
func (s *meshSync) storeFull(machine string, snap api.MachineSnapshot, etag string, now int64) {
	if err := s.d.view.replaceAll(machine, now, snap.Threads); err != nil {
		log.Printf("mesh sync: store full snapshot of %s: %v", machine, err)
		return // next round retries; old conditional state still matches the old content
	}
	s.markReconciled(machine)
	s.emu.Lock()
	defer s.emu.Unlock()
	if snap.Generation != "" {
		s.cursors[machine] = snap.Generation
		delete(s.etags, machine)
		return
	}
	delete(s.cursors, machine)
	if etag == "" {
		delete(s.etags, machine)
	} else {
		s.etags[machine] = etag
	}
}

func (s *meshSync) condStateOf(machine string) (etag, cursor string) {
	s.emu.Lock()
	defer s.emu.Unlock()
	return s.etags[machine], s.cursors[machine]
}

func (s *meshSync) rememberCursor(machine, cursor string) {
	s.emu.Lock()
	defer s.emu.Unlock()
	if cursor == "" {
		delete(s.cursors, machine)
		return
	}
	s.cursors[machine] = cursor
}

func (s *meshSync) clearCondState(machine string) {
	s.emu.Lock()
	defer s.emu.Unlock()
	delete(s.etags, machine)
	delete(s.cursors, machine)
}

// fetchPeerSnapshot pulls a peer's maintained snapshot over the peer's CONFIGURED
// transport (peers.Peer.Transport): http for a peer with a TCP API, ssh otherwise.
// Either way the result is the peer's MachineSnapshot — an O(1) read of ITS
// maintainer. Only http supports the conditional forms (delta cursor / etag; an
// older peer ignores them and serves the full 200); the ssh transport always
// returns the full payload. A transport failure is returned LOUDLY (the caller
// marks the peer unreachable); there is no silent ssh fallback for an http peer.
func (s *meshSync) fetchPeerSnapshot(p peers.Peer, etag, since string) (snap api.MachineSnapshot, newETag string, notModified bool, err error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.fetchTimeout)
	defer cancel()
	if p.Transport() == "http" {
		return s.fetchPeerSnapshotHTTP(ctx, p, etag, since)
	}
	threads, err := s.fetchPeerSnapshotSSH(ctx, p)
	return api.MachineSnapshot{Threads: threads}, "", false, err
}

// fetchPeerSnapshotHTTP talks directly to the peer daemon's TCP API (GET
// /v1/snapshot with a bearer token) — no remote process spawn, hits the peer's
// already-running maintainer from memory.
func (s *meshSync) fetchPeerSnapshotHTTP(ctx context.Context, p peers.Peer, etag, since string) (api.MachineSnapshot, string, bool, error) {
	token, err := p.ResolveAPIToken()
	if err != nil {
		return api.MachineSnapshot{}, "", false, err
	}
	return s.remoteClient(p.ApiAddr, token).SnapshotConditional(ctx, etag, since)
}

// remoteClient returns a reused HTTP client for (addr, token), so keep-alive holds
// the TCP connection across the 1s sync ticks.
func (s *meshSync) remoteClient(addr, token string) *client.Client {
	key := addr + "\x00" + token
	s.cmu.Lock()
	defer s.cmu.Unlock()
	c, ok := s.clients[key]
	if !ok {
		c = client.NewRemote(addr, token)
		s.clients[key] = c
	}
	return c
}

// fetchPeerSnapshotSSH pulls a peer's snapshot over a real ssh hop (with connection
// multiplexing so a 1s cadence reuses one persistent connection). The peer's
// `thread snapshot --json` is an O(1) read of ITS maintainer.
func (s *meshSync) fetchPeerSnapshotSSH(ctx context.Context, p peers.Peer) ([]api.ThreadSnapshot, error) {
	remote := strings.Join([]string{
		"env",
		"SESH_HOME=" + shQuote(p.Home),
		"SESH_MACHINE=" + shQuote(p.Machine),
		shQuote(p.Binary),
		"thread", "snapshot", "--json",
	}, " ")

	args := append(sshMultiplexArgs(), p.SSHArgs()...)
	args = append(args, p.SSH, remote)
	out, err := exec.CommandContext(ctx, "ssh", args...).Output()
	if err != nil {
		return nil, err
	}
	var threads []api.ThreadSnapshot
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var th api.ThreadSnapshot
		if err := json.Unmarshal([]byte(line), &th); err != nil {
			return nil, err
		}
		threads = append(threads, th)
	}
	return threads, nil
}

// sshMultiplexArgs reuses one persistent connection per peer — see
// peers.SSHMultiplexArgs. Kept as a thin alias so the daemon and the CLI share the
// exact same ControlPath (the warm connection is reused across both).
func sshMultiplexArgs() []string { return peers.SSHMultiplexArgs() }

// handleMeshNudge serves POST /v1/mesh/nudge (schema 45): refresh the cached
// snapshot of ONE peer now, because a routed write to it just succeeded. The
// sync launches in the background — this returns immediately (the caller is a
// CLI process about to exit; blocking it on the fetch would re-add the very
// latency the nudge removes). Also counts as mesh demand, so the cadence goes
// active for the follow-up window (which covers the nudge racing an already
// in-flight pre-write fetch). Self / empty / unknown machine are loud.
func (d *Daemon) handleMeshNudge(w http.ResponseWriter, r *http.Request) {
	var req api.MeshNudgeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Machine == "" {
		writeError(w, http.StatusBadRequest, "machine is required")
		return
	}
	if req.Machine == d.cfg.Machine {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%q is this machine — local threads are live, nothing to nudge", req.Machine))
		return
	}
	d.noteMeshDemand()
	if err := d.mesh.nudgePeer(req.Machine); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "ok": true})
}

// handleMesh serves GET /v1/mesh: the merged cross-machine view — this machine's
// live snapshot (always fresh) plus every cached peer (with staleness), read
// locally. The TUI's data source.
func (d *Daemon) handleMesh(w http.ResponseWriter, r *http.Request) {
	d.noteMeshDemand() // a mesh read is what the demand-driven cadence keys on
	resp := api.MeshSnapshot{Schema: api.SchemaVersion}

	self := d.maint.snapshot()
	resp.Machines = append(resp.Machines, api.MachineView{
		Machine:      d.cfg.Machine,
		Self:         true,
		Reachable:    true,
		SyncedAtUnix: self.GeneratedAtUnix,
		Threads:      self.Threads,
	})

	resp.Machines = append(resp.Machines, d.view.machineViews()...)
	writeJSON(w, http.StatusOK, resp)
}
