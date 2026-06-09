package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/api"
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
}

func newMeshSync(d *Daemon) *meshSync {
	return &meshSync{d: d, stop: make(chan struct{}), done: make(chan struct{})}
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
	<-s.done
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

// tick fetches every peer's snapshot CONCURRENTLY (a slow/offline peer cannot stall
// the others — each fetch has its own timeout), then writes the results to the
// cache SEQUENTIALLY (one SQLite writer). A failed fetch keeps the peer's last-known
// payload and only flips it stale (offline browsing).
func (s *meshSync) tick() {
	reg, err := peers.Load(s.d.cfg.PeersPath())
	if err != nil {
		return
	}
	list := reg.List()
	if len(list) == 0 {
		return
	}

	type result struct {
		machine string
		ok      bool
		payload string
	}
	results := make([]result, len(list))
	var wg sync.WaitGroup
	for i, p := range list {
		i, p := i, p
		wg.Add(1)
		go func() {
			defer wg.Done()
			threads, err := s.fetchPeerSnapshot(p)
			if err != nil {
				results[i] = result{machine: p.Machine, ok: false}
				return
			}
			payload, err := json.Marshal(threads)
			if err != nil {
				results[i] = result{machine: p.Machine, ok: false}
				return
			}
			results[i] = result{machine: p.Machine, ok: true, payload: string(payload)}
		}()
	}
	wg.Wait()

	now := time.Now().Unix()
	for _, r := range results {
		if r.ok {
			s.d.store.UpsertPeerSnapshot(r.machine, now, r.payload) //nolint:errcheck — next tick retries
		} else {
			s.d.store.MarkPeerUnreachable(r.machine) //nolint:errcheck
		}
	}
}

// fetchPeerSnapshot pulls a peer's maintained snapshot over a real ssh hop (with
// connection multiplexing so a 1s cadence reuses one persistent connection). The
// peer's `thread snapshot --json` is an O(1) read of ITS maintainer.
func (s *meshSync) fetchPeerSnapshot(p peers.Peer) ([]api.ThreadSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
	defer cancel()

	remote := strings.Join([]string{
		"env",
		"SESH_HOME=" + shQuote(p.Home),
		"SESH_MACHINE=" + shQuote(p.Machine),
		shQuote(p.Binary),
		"thread", "snapshot", "--json",
	}, " ")

	args := append(sshMultiplexArgs(), p.SSH, remote)
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

// sshMultiplexArgs returns ssh options that reuse one persistent connection per
// peer (ControlMaster/ControlPersist) — so the 1s mesh sync pays a handshake once,
// not every tick. ControlPath uses %C (a fixed-length hash) to stay within the unix
// socket path limit regardless of how long $SESH_HOME is.
func sshMultiplexArgs() []string {
	dir := filepath.Join(os.TempDir(), "sesh-ssh-cm")
	os.MkdirAll(dir, 0o700) //nolint:errcheck — best-effort; ssh falls back to no mux
	return []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=6",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(dir, "%C"),
		"-o", "ControlPersist=60s",
	}
}

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
