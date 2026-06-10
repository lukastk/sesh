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
	"sort"
	"strings"
)

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
func (r Registry) List() []Peer {
	out := make([]Peer, 0, len(r.Peers))
	for _, p := range r.Peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Machine < out[j].Machine })
	return out
}
