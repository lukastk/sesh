// Package peers is the local view of the mesh: how to reach each OTHER machine's
// daemon. A peer is reached by ssh-ing to it and running sesh there against its
// own SESH_HOME (so the command hits that machine's local daemon). This is the
// honest remote path — a real ssh hop into a real remote daemon — and it is what
// makes `--machine X` route to X instead of silently acting locally (the v1 bug).
package peers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lukastk/sesh/internal/config"
)

// Keepalive cadence for every ssh sesh opens: probe the far end every
// sshAliveInterval seconds and give up after sshAliveCountMax unanswered probes
// (~45s to notice). See SSHKeepaliveArgs for why this is not optional.
const (
	sshAliveInterval = "15"
	sshAliveCountMax = "3"
)

// SSHKeepaliveArgs returns the ssh options that make a DEAD connection actually
// die. Without them ssh only notices a peer that vanished when it next has bytes
// to send -- and a master-window attach into an idle work server has none for
// hours, so a path that dies silently (no FIN, no RST: a laptop sleeping, a
// network changing under it) leaves ssh blocked forever on a socket nobody is
// listening to. That is not a hypothetical: it wedged the macbook cockpit after
// every sleep. The window kept painting its last pre-sleep frame, and because the
// far side's sshd kept the pty open, the remote tmux still listed that client, so
// the master-client marker still matched it and `tmux nav` cheerfully switched a
// dead client and reported success. Nothing recovered it short of rebuilding the
// whole master (mmt-kill/mmt-start), because `sesh master window`'s supervisor
// re-establishes only when the attach process EXITS -- which a blocked ssh never
// does.
//
// The OS TCP keepalive is not a backstop worth having here: it is ~2h idle on
// both macOS and Linux (the same reason the terminal bridge pings its websocket
// itself -- see internal/daemon/terminal.go).
//
// A false positive costs one reconnect (sub-second, via the supervisor's existing
// backoff) plus a redraw; a missed detection costs a cockpit wedged until the user
// notices and restarts it. So this deliberately errs toward disconnecting.
func SSHKeepaliveArgs() []string {
	return []string{
		"-o", "ServerAliveInterval=" + sshAliveInterval,
		"-o", "ServerAliveCountMax=" + sshAliveCountMax,
	}
}

// SSHMultiplexArgs returns ssh options that reuse ONE persistent connection per
// peer (ControlMaster/ControlPersist), so callers piggyback on an already-open
// master instead of paying a fresh TCP+SSH handshake every invocation. The
// daemon's mesh-sync keeps this connection warm (re-touched every ~1s), so an
// interactive `tmux nav` over ssh rides it and switches near-instantly. The
// ControlPath uses %C (a fixed-length hash of the connection target) so the
// daemon and the CLI compute the SAME socket and share it.
//
// The socket dir is rooted in SESH_HOME (config.ResolveHome), NOT os.TempDir():
// on macOS TempDir is the ~49-char /var/folders/<...>/T path, and with the 40-char
// %C hash plus the ".<16 random>" suffix ssh appends while CREATING the master
// socket, the path overran the 104-byte macOS unix-socket limit — so opening the
// master failed with "path ... too long for Unix domain socket" (connecting to an
// existing one, which skips the suffix, still worked). SESH_HOME is short (~/.sesh),
// per-user, and is already the dir the daemon and CLI agree on (daemon.sock lives
// there too, so SESH_HOME is already length-constrained), so they still share ONE
// socket.
// ConnectTimeout bounds only the HANDSHAKE; the keepalive bounds the rest. A
// ControlMaster that was established before the machine slept is already past the
// handshake, so without SSHKeepaliveArgs a routed command riding that dead mux
// socket hangs with no timeout at all (the nav inner switch over ssh -- cmd/sesh
// tmux.go -- runs the ssh with no context deadline).
func SSHMultiplexArgs() []string {
	dir := filepath.Join(config.ResolveHome(), "ssh-cm")
	os.MkdirAll(dir, 0o700) //nolint:errcheck — best-effort; ssh falls back to no mux
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=6",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(dir, "%C"),
		"-o", "ControlPersist=60s",
	}
	return append(args, SSHKeepaliveArgs()...)
}

// Peer is how to reach one remote machine's daemon. Two transports:
//   - ssh (always available): ssh into the machine and run sesh against its own
//     SESH_HOME, hitting that machine's daemon over its unix socket.
//   - http (opt-in, when ApiAddr is set): talk DIRECTLY to the peer's daemon's TCP
//     API over the network, hitting its already-running process (no remote fork).
//
// The transport is EXPLICIT per peer (Transport()), never an automatic fallback: a
// peer with an ApiAddr uses http and an http failure is a LOUD error, not a silent
// ssh downgrade (a silent downgrade would mask a broken path — the v1 `--machine X`
// anti-pattern). ssh remains the bootstrap/admin transport regardless.
type Peer struct {
	Machine    string `json:"machine"`               // the remote machine's identity (SESH_MACHINE)
	SSH        string `json:"ssh"`                   // ssh destination, e.g. user@host or localhost
	Port       string `json:"port,omitempty"`        // ssh port (empty = default 22)
	Home       string `json:"home"`                  // the remote SESH_HOME (locates its daemon socket)
	Binary     string `json:"binary"`                // path to the sesh binary on the remote machine
	TmuxSocket string `json:"tmux_socket,omitempty"` // the remote mytmux socket NAME (for tmux nav)
	CodexHome  string `json:"codex_home,omitempty"`  // the remote SESH_CODEX_HOME (test isolation; '' = the peer's default ~/.codex)
	TmuxConf   string `json:"tmux_conf,omitempty"`   // the remote work tmux `-f` config (master's remote window starts the peer's work server with it)

	// HTTP transport (opt-in). ApiAddr set => this peer is reached over its TCP API.
	ApiAddr      string `json:"api_addr,omitempty"`       // the peer daemon's TCP API addr (host:port)
	ApiToken     string `json:"api_token,omitempty"`      // bearer token literal (plaintext on disk — prefer the file)
	ApiTokenFile string `json:"api_token_file,omitempty"` // path to a file holding the bearer token (preferred)
}

// Transport reports how this peer is reached: "http" if it has a TCP API configured
// (ApiAddr), else "ssh". This is the single explicit decision point — no fallback.
func (p Peer) Transport() string {
	if p.ApiAddr != "" {
		return "http"
	}
	return "ssh"
}

// ResolveAPIToken returns the bearer token for this peer's HTTP API — the literal
// ApiToken or the contents of ApiTokenFile. It is LOUD: a peer with an ApiAddr but
// no resolvable token is an error, never a silent unauthenticated attempt.
func (p Peer) ResolveAPIToken() (string, error) {
	if p.ApiToken != "" {
		return p.ApiToken, nil
	}
	if p.ApiTokenFile != "" {
		b, err := os.ReadFile(p.ApiTokenFile)
		if err != nil {
			return "", fmt.Errorf("peers: %s: read api_token_file: %w", p.Machine, err)
		}
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return "", fmt.Errorf("peers: %s: api_token_file %q is empty", p.Machine, p.ApiTokenFile)
		}
		return tok, nil
	}
	return "", fmt.Errorf("peers: %s has api_addr but no api_token/api_token_file", p.Machine)
}

// SSHArgs returns the ssh option args for reaching this peer (the port, if
// non-default), to be placed before the destination in an `ssh` invocation. Every
// ssh-to-peer call site MUST include these so non-22 machines are reachable.
func (p Peer) SSHArgs() []string {
	if p.Port != "" && p.Port != "22" {
		return []string{"-p", p.Port}
	}
	return nil
}

// Registry is the set of known peers, keyed by machine.
type Registry struct {
	Peers map[string]Peer `json:"peers"`
}

// Load reads the registry from path. A missing file is an empty registry (no
// peers configured yet), not an error.
func Load(path string) (Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Registry{Peers: map[string]Peer{}}, nil
		}
		return Registry{}, err
	}
	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return Registry{}, fmt.Errorf("peers: parse %s: %w", path, err)
	}
	if r.Peers == nil {
		r.Peers = map[string]Peer{}
	}
	return r, nil
}

// Save writes the registry to path.
func (r Registry) Save(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Get returns a peer by machine.
func (r Registry) Get(machine string) (Peer, bool) {
	p, ok := r.Peers[machine]
	return p, ok
}

// Add inserts or replaces a peer (loud on an empty machine name).
func (r *Registry) Add(p Peer) error {
	if p.Machine == "" {
		return fmt.Errorf("peers: empty machine name")
	}
	if r.Peers == nil {
		r.Peers = map[string]Peer{}
	}
	r.Peers[p.Machine] = p
	return nil
}

// List returns all peers sorted by machine.
// Remove deletes a peer by machine name. Removing an unknown peer is a loud
// error (a typo must not silently "succeed").
func (r *Registry) Remove(machine string) error {
	if _, ok := r.Peers[machine]; !ok {
		return fmt.Errorf("peers: no peer named %q", machine)
	}
	delete(r.Peers, machine)
	return nil
}

func (r Registry) List() []Peer {
	out := make([]Peer, 0, len(r.Peers))
	for _, p := range r.Peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Machine < out[j].Machine })
	return out
}
