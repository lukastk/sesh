// Package daemon is the per-machine sesh daemon: a single-writer process that
// owns this machine's SQLite store and serves the client-facing HTTP+JSON API
// over a unix domain socket. Clients (CLI/TUI) talk only to their local
// daemon's socket; cross-machine reads (later phases) go via a peer mesh.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// Version is the daemon/binary version string.
const Version = "0.1.0-dev"

// Daemon is a running per-machine daemon.
type Daemon struct {
	cfg     config.Config
	store   *store.Store
	tmux    *tmux.Server
	srv     *http.Server
	ln      net.Listener
	started time.Time

	// headless turn tracking: a stateless-per-turn headless thread is "working"
	// exactly while a turn process is in flight (this daemon spawned it). The last
	// reply is kept for retrieval. In-memory: a daemon restart loses in-flight
	// state, which is acceptable for the stateless model (the turn is orphaned but
	// its reply lands on disk in the agent's session).
	hlMu       sync.Mutex
	hlInFlight map[string]bool
	hlReply    map[string]string

	// maint is the L1 state maintainer: it keeps every local thread's live state
	// continuously fresh so reads are O(1) (see _dev/MESH.md).
	maint *maintainer
	// mesh is the L2 sync: it keeps a local cache of every peer's snapshot fresh so
	// the cross-machine view (GET /v1/mesh) is a local read.
	mesh *meshSync
	// naming is the user's [[session_name]] policy (nil = default sesh_<name>).
	naming *config.Naming
	// hooks + evt: the [[hooks]] runner and the mesh-wide observer loop that
	// feeds it (see eventer.go / hookrunner.go).
	hooks *hookRunner
	evt   *eventer
	// mmaint converges the master cockpit (one window per connected machine);
	// nil when SESH_MASTER_SELFHEAL=off.
	mmaint *masterMaint

	// apiSrv is the optional TCP API server (the network surface for remote clients /
	// mobile) — the SAME full router behind a bearer token. nil unless SESH_API_ADDR
	// is set.
	apiSrv *http.Server
}

// New opens the store and prepares (but does not start) the daemon. It refuses
// to start if a live daemon already owns the socket — two writers on one store
// is exactly the kind of silent corruption sesh must avoid.
func New(cfg config.Config) (*Daemon, error) {
	// The daemon must spawn agents as clean, TOP-LEVEL sessions. If it was started
	// from inside an agent harness (e.g. under Claude Code, or by the conformance
	// suite), inherited markers like CLAUDECODE=1 / CLAUDE_CODE_SESSION_ID make a
	// spawned claude behave as a nested session and silently stop persisting its
	// transcript — which breaks `resume`. Scrub them before anything is spawned.
	agents.ScrubHarnessEnv()

	if err := cfg.EnsureHome(); err != nil {
		return nil, fmt.Errorf("daemon: ensure home: %w", err)
	}

	if alive, _ := socketAlive(cfg.SocketPath()); alive {
		return nil, fmt.Errorf("daemon: a live daemon already listens on %s", cfg.SocketPath())
	}
	// A stale socket file (no live daemon) blocks bind; remove it.
	if _, err := os.Stat(cfg.SocketPath()); err == nil {
		if err := os.Remove(cfg.SocketPath()); err != nil {
			return nil, fmt.Errorf("daemon: remove stale socket: %w", err)
		}
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, err
	}
	if err := st.Ping(); err != nil {
		st.Close()
		return nil, err
	}

	naming, err := config.LoadNaming(cfg.Home)
	if err != nil {
		st.Close() //nolint:errcheck
		return nil, err
	}
	d := &Daemon{
		cfg:        cfg,
		naming:     naming,
		store:      st,
		tmux:       tmux.NewServerWithConf(cfg.TmuxSocket, cfg.TmuxConf),
		hlInFlight: map[string]bool{},
		hlReply:    map[string]string{},
	}
	d.maint = newMaintainer(d)
	d.mesh = newMeshSync(d)
	hooks, err := config.LoadHooks(cfg.Home)
	if err != nil {
		return nil, err // a broken [[hooks]] refuses the daemon loudly
	}
	muted, err := st.HookMutes()
	if err != nil {
		return nil, err
	}
	d.hooks = newHookRunner(hooks, muted)
	d.evt = newEventer(d, d.hooks)
	if cfg.MasterSelfheal {
		d.mmaint = newMasterMaint(d)
	}
	d.srv = &http.Server{Handler: d.routes()}
	return d, nil
}

// Serve binds the unix socket, writes the pid file, and serves until Shutdown
// (or a POST /v1/shutdown) is requested. It blocks.
func (d *Daemon) Serve() error {
	ln, err := net.Listen("unix", d.cfg.SocketPath())
	if err != nil {
		return fmt.Errorf("daemon: listen %s: %w", d.cfg.SocketPath(), err)
	}
	d.ln = ln
	d.started = time.Now()
	d.maint.start() // begin keeping local thread state fresh in the background
	d.mesh.start()  // begin syncing peers' snapshots into the local cache
	go d.evt.run()  // observe the merged mesh + fire [[hooks]]
	if d.mmaint != nil {
		d.mmaint.start() // converge the master cockpit to one window per connected machine
	}

	// Optional network API (remote clients / mobile). A bind failure is fatal — do
	// not silently fall back to unix-only when the user asked to expose the API.
	if err := d.startAPI(); err != nil {
		if d.mmaint != nil {
			d.mmaint.stopAndWait()
		}
		d.mesh.stopAndWait()
		d.maint.stopAndWait()
		ln.Close()
		return err
	}

	if err := os.WriteFile(d.cfg.PIDPath(), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("daemon: write pid: %w", err)
	}
	// Clean up the on-disk markers in the foreground, after Serve returns. Doing
	// this here (rather than in the async Shutdown goroutine) makes removal
	// deterministic: the process cannot exit before these run. The listener
	// close also unlinks the socket, but we remove it explicitly to be sure.
	defer func() {
		os.Remove(d.cfg.PIDPath())
		os.Remove(d.cfg.SocketPath())
	}()

	err = d.srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops the HTTP server and closes the store. Safe to call
// once. On-disk markers (pid/socket) are removed by Serve's deferred cleanup, in
// the foreground, so they are gone deterministically before the process exits.
func (d *Daemon) Shutdown(ctx context.Context) error {
	d.stopAPI(ctx)
	d.evt.shutdown()
	if d.mmaint != nil {
		d.mmaint.stopAndWait()
	}
	d.mesh.stopAndWait()
	d.maint.stopAndWait()
	srvErr := d.srv.Shutdown(ctx)
	storeErr := d.store.Close()
	if srvErr != nil {
		return srvErr
	}
	return storeErr
}

// socketAlive reports whether a daemon is actually answering on the socket (not
// just whether the socket file exists). A dial that connects but a health probe
// that fails is treated as not-alive (stale).
func socketAlive(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		return false, nil
	}
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false, nil // file exists but nothing listening => stale
	}
	conn.Close()
	return true, nil
}
