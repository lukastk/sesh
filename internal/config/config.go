// Package config resolves the per-machine paths and identity the daemon and CLI
// share. Everything is overridable via environment so tests (and the honest
// "remote = second daemon" setup) can run many isolated daemons on one box.
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Config is the resolved environment for one sesh daemon/CLI invocation.
type Config struct {
	// Home is the base directory holding the socket, db, and pid file.
	Home string
	// Machine is this daemon's identity in the mesh.
	Machine string
	// TmuxSocket is the tmux server socket NAME (tmux -L) that sesh threads run
	// on. Per the spec this is just a regular tmux server; the name carries no
	// semantics. Default "mytmux"; overridable so tests use an isolated server.
	TmuxSocket string
	// TicketOwner is the canonical always-on machine that owns the ticket store
	// (the single writer). When set and not this machine, ticket commands route
	// to the owner. Empty = this machine owns its own tickets (no mesh).
	TicketOwner string
	// CodexHome overrides codex's home dir (CODEX_HOME) for spawned codex threads
	// — sesh writes per-cwd trust there and injects CODEX_HOME into the pane. Empty
	// = codex's default ~/.codex. Tests point this at an isolated dir.
	CodexHome string
}

// Load resolves config from the environment:
//
//	SESH_HOME     base dir (default ~/.sesh)
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
		home = filepath.Join(uh, ".sesh")
	}

	machine := os.Getenv("SESH_MACHINE")
	if machine == "" {
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

	return Config{
		Home:        home,
		Machine:     machine,
		TmuxSocket:  socket,
		TicketOwner: os.Getenv("SESH_TICKET_OWNER"),
		CodexHome:   os.Getenv("SESH_CODEX_HOME"),
	}
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
