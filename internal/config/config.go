// Package config resolves the per-machine paths and identity the daemon and CLI
// share. Everything is overridable via environment so tests (and the honest
// "remote = second daemon" setup) can run many isolated daemons on one box.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the resolved environment for one sesh daemon/CLI invocation.
type Config struct {
	// Home is the base directory holding the socket, db, and pid file.
	Home string
	// Machine is this daemon's identity in the mesh. MachineExplicit reports
	// whether it came from $SESH_MACHINE (vs the hostname fallback, which only
	// CLIENT reads may use — the daemon refuses to start on a guessed identity).
	Machine         string
	MachineExplicit bool
	// TmuxSocket is the tmux server socket NAME (tmux -L) that sesh threads run
	// on. Per the spec this is just a regular tmux server; the name carries no
	// semantics. Default "mytmux"; overridable so tests use an isolated server.
	TmuxSocket string
	// MasterSocket is the "mymastertmux" socket — the master view with one window
	// per machine. `tmux nav` switches the outer client there. Default
	// "mymastertmux".
	MasterSocket string
	// TmuxConf, when set (SESH_TMUX_CONF), is the `tmux -f` config the WORK server
	// starts with — so sesh's tmux can carry its own UI (e.g. the per-thread status
	// bar) separate from the user's default ~/.tmux.conf. Empty = tmux default.
	TmuxConf string
	// MasterSelfheal (default ON) runs the daemon's cockpit-convergence loop: while
	// a master server is up, every machine that is CONNECTED (self + mesh-reachable
	// peers) gets a window — a killed window comes back, and a newly-reachable
	// machine gains one. SESH_MASTER_SELFHEAL=off disables it (test isolation for
	// the manual `master ensure` cell; production leaves it on).
	MasterSelfheal bool
	// TicketOwner is the canonical always-on machine that owns the ticket store
	// (the single writer). When set and not this machine, ticket commands route
	// to the owner. Empty = this machine owns its own tickets (no mesh).
	TicketOwner string
	// CodexHome overrides codex's home dir (CODEX_HOME) for spawned codex threads
	// — sesh writes per-cwd trust there and injects CODEX_HOME into the pane. Empty
	// = codex's default ~/.codex. Tests point this at an isolated dir.
	CodexHome string
	// APIAddr, when set, makes the daemon ALSO listen on this TCP address (e.g. a
	// tailscale-bound "100.x.x.x:7373"), serving the full HTTP+JSON API for remote
	// clients (mobile/Obsidian). Empty = unix socket only. APIToken is the bearer
	// token required on the TCP listener; it MUST be set if APIAddr is (the daemon
	// refuses to expose an unauthenticated network API).
	APIAddr  string
	APIToken string
	// RemoteAddr/RemoteToken, when set, make the CLI/TUI target a REMOTE daemon's
	// TCP API (SESH_REMOTE + SESH_API_TOKEN) instead of the local unix socket.
	RemoteAddr  string
	RemoteToken string
}

// Load resolves config from the environment:
//
//	SESH_HOME     base dir (default ~/.sesh-v2)
//	SESH_MACHINE  machine identity (default hostname)
//
// It panics only on a truly broken environment (no home dir AND no SESH_HOME,
// or no hostname) — there is no sane fallback for "who am I / where do I live",
// and guessing would scatter state silently.
func Load() Config {
	home := os.Getenv("SESH_HOME")
	if home == "" {
		uh, err := os.UserHomeDir()
		if err != nil {
			panic("config: SESH_HOME unset and cannot resolve user home dir: " + err.Error())
		}
		// ~/.sesh-v2, NOT ~/.sesh: the live v1 installation owns ~/.sesh on
		// Lukas's machines — a bare v2 invocation must never read or write v1's
		// store/config. Revisit when v1 retires.
		home = filepath.Join(uh, ".sesh-v2")
	}

	machine := os.Getenv("SESH_MACHINE")
	machineExplicit := machine != ""
	if machine == "" {
		// Hostname fallback is CLIENT-only convenience (reads persist nothing).
		// The DAEMON refuses to run on it — machine identity is load-bearing
		// (records, routing, ownership, delivery) and must never be guessed for
		// anything that persists. See MachineExplicit.
		h, err := os.Hostname()
		if err != nil {
			panic("config: SESH_MACHINE unset and cannot resolve hostname: " + err.Error())
		}
		machine = h
	}

	socket := os.Getenv("SESH_TMUX_SOCKET")
	if socket == "" {
		socket = "mytmux"
	}

	masterSocket := os.Getenv("SESH_MASTER_SOCKET")
	if masterSocket == "" {
		masterSocket = "mymastertmux"
	}

	return Config{
		Home:         home,
		Machine:         machine,
		MachineExplicit: machineExplicit,
		TmuxSocket:   socket,
		MasterSocket: masterSocket,
		TmuxConf:     os.Getenv("SESH_TMUX_CONF"),
		MasterSelfheal: func() bool {
			switch os.Getenv("SESH_MASTER_SELFHEAL") {
			case "off", "0", "false":
				return false
			}
			return true
		}(),
		TicketOwner: os.Getenv("SESH_TICKET_OWNER"),
		CodexHome:   os.Getenv("SESH_CODEX_HOME"),
		APIAddr:     os.Getenv("SESH_API_ADDR"),
		APIToken:    resolveToken(os.Getenv("SESH_API_TOKEN"), os.Getenv("SESH_API_TOKEN_FILE")),
		RemoteAddr:  os.Getenv("SESH_REMOTE"),
		RemoteToken: resolveToken(os.Getenv("SESH_API_TOKEN"), os.Getenv("SESH_API_TOKEN_FILE")),
	}
}

// resolveToken returns the token from the literal value, else read from file.
func resolveToken(literal, file string) string {
	if literal != "" {
		return literal
	}
	if file != "" {
		if b, err := os.ReadFile(file); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// EnsureHome creates the home directory if missing.
func (c Config) EnsureHome() error {
	if c.Home == "" {
		return fmt.Errorf("config: empty Home")
	}
	return os.MkdirAll(c.Home, 0o700)
}

// SocketPath is the unix domain socket the daemon listens on and clients dial.
func (c Config) SocketPath() string { return filepath.Join(c.Home, "daemon.sock") }

// DBPath is the SQLite database file.
func (c Config) DBPath() string { return filepath.Join(c.Home, "sesh.db") }

// PIDPath records the running daemon's pid.
func (c Config) PIDPath() string { return filepath.Join(c.Home, "daemon.pid") }

// PeersPath is the local mesh registry (how to reach other machines).
func (c Config) PeersPath() string { return filepath.Join(c.Home, "peers.json") }
