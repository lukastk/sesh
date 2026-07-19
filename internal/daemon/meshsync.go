package daemon

import (
	"context"
	"encoding/json"
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

	// etags remembers, per peer, the ETag of the payload currently in the cache,
	// so the next http fetch is conditional (an unchanged snapshot costs a
	// bodyless 304, not the whole payload). In-memory only: a daemon restart just
	// refetches full once. Invariant: a peer's remembered ETag always corresponds
	// to its STORED payload (set only after a successful upsert).
	emu   sync.Mutex
	etags map[string]string

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
}

func (s *meshSync) run() {
	defer close(s.done)
	t := time.NewTicker(s.tickInterval)
	defer t.Stop()
	s.tick()
	lastRound := time.Now()
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
		s.imu.Lock()
		if s.inflight[p.Machine] {
			s.imu.Unlock()
			continue
		}
		s.inflight[p.Machine] = true
		s.imu.Unlock()
		s.wg.Add(1)
		go func(p peers.Peer) {
			defer s.wg.Done()
			defer func() {
				s.imu.Lock()
				delete(s.inflight, p.Machine)
				s.imu.Unlock()
			}()
			s.syncPeer(p)
		}(p)
	}
}

// syncPeer fetches one peer's snapshot and records the outcome. The SQLite writes
// serialize in the store (SetMaxOpenConns(1)), so concurrent completions are safe.
// The http fetch is CONDITIONAL on the peer's remembered ETag: an unchanged
// snapshot answers 304 and only the cache row's freshness is touched.
func (s *meshSync) syncPeer(p peers.Peer) {
	threads, etag, notModified, err := s.fetchPeerSnapshot(p, s.etagOf(p.Machine))
	if err != nil {
		// A fetch aborted by shutdown says nothing about the peer — don't flip it
		// stale on the way out.
		if s.ctx.Err() != nil {
			return
		}
		s.d.store.MarkPeerUnreachable(p.Machine) //nolint:errcheck — next tick retries
		return
	}
	if notModified {
		touched, terr := s.d.store.TouchPeerSnapshot(p.Machine, time.Now().Unix())
		if terr != nil || touched {
			return
		}
		// 304 but no cache row to refresh: the peer's cache entry was removed
		// (registry remove + re-add) while the in-memory ETag survived. A
		// conditional fetch would leave this peer payload-less forever while
		// looking freshly synced — drop the ETag and refetch unconditionally.
		s.rememberETag(p.Machine, "")
		threads, etag, _, err = s.fetchPeerSnapshot(p, "")
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			s.d.store.MarkPeerUnreachable(p.Machine) //nolint:errcheck
			return
		}
	}
	payload, err := json.Marshal(threads)
	if err != nil {
		s.d.store.MarkPeerUnreachable(p.Machine) //nolint:errcheck
		return
	}
	if s.d.store.UpsertPeerSnapshot(p.Machine, time.Now().Unix(), string(payload)) == nil {
		// Remember the ETag only once its payload is the stored one (coherence:
		// a 304 must always mean "the CACHED payload is current").
		s.rememberETag(p.Machine, etag)
	}
}

func (s *meshSync) etagOf(machine string) string {
	s.emu.Lock()
	defer s.emu.Unlock()
	return s.etags[machine]
}

func (s *meshSync) rememberETag(machine, etag string) {
	s.emu.Lock()
	defer s.emu.Unlock()
	if etag == "" {
		delete(s.etags, machine)
		return
	}
	s.etags[machine] = etag
}

// fetchPeerSnapshot pulls a peer's maintained snapshot over the peer's CONFIGURED
// transport (peers.Peer.Transport): http for a peer with a TCP API, ssh otherwise.
// Either way the result is the peer's MachineSnapshot threads — an O(1) read of ITS
// maintainer. Only http supports the conditional fetch (etag; a pre-40 peer
// ignores it and serves the full 200); the ssh transport always returns the full
// payload with etag "". A transport failure is returned LOUDLY (the caller marks
// the peer unreachable); there is no silent ssh fallback for an http peer.
func (s *meshSync) fetchPeerSnapshot(p peers.Peer, etag string) (threads []api.ThreadSnapshot, newETag string, notModified bool, err error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.fetchTimeout)
	defer cancel()
	if p.Transport() == "http" {
		return s.fetchPeerSnapshotHTTP(ctx, p, etag)
	}
	threads, err = s.fetchPeerSnapshotSSH(ctx, p)
	return threads, "", false, err
}

// fetchPeerSnapshotHTTP talks directly to the peer daemon's TCP API (GET
// /v1/snapshot with a bearer token) — no remote process spawn, hits the peer's
// already-running maintainer from memory.
func (s *meshSync) fetchPeerSnapshotHTTP(ctx context.Context, p peers.Peer, etag string) ([]api.ThreadSnapshot, string, bool, error) {
	token, err := p.ResolveAPIToken()
	if err != nil {
		return nil, "", false, err
	}
	snap, newETag, notModified, err := s.remoteClient(p.ApiAddr, token).SnapshotConditional(ctx, etag)
	if err != nil {
		return nil, "", false, err
	}
	if notModified {
		return nil, etag, true, nil
	}
	return snap.Threads, newETag, false, nil
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

	cached, err := d.store.LoadPeerSnapshots()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, c := range cached {
		var threads []api.ThreadSnapshot
		if c.Payload != "" {
			json.Unmarshal([]byte(c.Payload), &threads) //nolint:errcheck — best-effort decode
		}
		resp.Machines = append(resp.Machines, api.MachineView{
			Machine:      c.Machine,
			Reachable:    c.Reachable,
			SyncedAtUnix: c.SyncedAtUnix,
			Threads:      threads,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
