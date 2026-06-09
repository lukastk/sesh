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
}

// New opens the store and prepares (but does not start) the daemon. It refuses
// to start if a live daemon already owns the socket — two writers on one store
// is exactly the kind of silent corruption sesh must avoid.
func New(cfg config.Config) (*Daemon, error) {
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

	d := &Daemon{
		cfg:        cfg,
		store:      st,
		tmux:       tmux.NewServer(cfg.TmuxSocket),
		hlInFlight: map[string]bool{},
		hlReply:    map[string]string{},
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
