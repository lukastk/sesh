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
const (
	meshSyncTick     = 1 * time.Second
	meshFetchTimeout = 8 * time.Second
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

	// inflight tracks peers with a fetch currently running, so a tick launches at
	// most one fetch per peer: a hanging peer's next fetch waits for its previous
	// one to time out, while every OTHER peer keeps its 1s cadence. wg tracks the
	// fetch goroutines for stopAndWait.
	imu      sync.Mutex
	inflight map[string]bool
	wg       sync.WaitGroup

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
		inflight:     map[string]bool{},
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
	t := time.NewTicker(meshSyncTick)
	defer t.Stop()
	s.tick()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.tick()
		}
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
func (s *meshSync) syncPeer(p peers.Peer) {
	threads, err := s.fetchPeerSnapshot(p)
	if err != nil {
		// A fetch aborted by shutdown says nothing about the peer — don't flip it
		// stale on the way out.
		if s.ctx.Err() != nil {
			return
		}
		s.d.store.MarkPeerUnreachable(p.Machine) //nolint:errcheck — next tick retries
		return
	}
	payload, err := json.Marshal(threads)
	if err != nil {
		s.d.store.MarkPeerUnreachable(p.Machine) //nolint:errcheck
		return
	}
	s.d.store.UpsertPeerSnapshot(p.Machine, time.Now().Unix(), string(payload)) //nolint:errcheck — next tick retries
}

// fetchPeerSnapshot pulls a peer's maintained snapshot over the peer's CONFIGURED
// transport (peers.Peer.Transport): http for a peer with a TCP API, ssh otherwise.
// Either way the result is the peer's MachineSnapshot threads — an O(1) read of ITS
// maintainer. A transport failure is returned LOUDLY (the caller marks the peer
// unreachable); there is no silent ssh fallback for an http peer.
func (s *meshSync) fetchPeerSnapshot(p peers.Peer) ([]api.ThreadSnapshot, error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.fetchTimeout)
	defer cancel()
	if p.Transport() == "http" {
		return s.fetchPeerSnapshotHTTP(ctx, p)
	}
	return s.fetchPeerSnapshotSSH(ctx, p)
}

// fetchPeerSnapshotHTTP talks directly to the peer daemon's TCP API (GET
// /v1/snapshot with a bearer token) — no remote process spawn, hits the peer's
// already-running maintainer from memory.
func (s *meshSync) fetchPeerSnapshotHTTP(ctx context.Context, p peers.Peer) ([]api.ThreadSnapshot, error) {
	token, err := p.ResolveAPIToken()
	if err != nil {
		return nil, err
	}
	snap, err := s.remoteClient(p.ApiAddr, token).Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.Threads, nil
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
