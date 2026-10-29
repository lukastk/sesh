package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/peers"
)

// knownOfflinePeers returns the set of peer machines the background liveness probe
// currently has cached as unreachable (peer_snapshots.reachable = 0). It is
// authoritative-but-conservative: a peer with NO cached snapshot yet (never synced
// since this daemon started) is ABSENT from the map, and a read error yields an empty
// map — so callers treat ONLY an explicit true as "known offline" and still attempt an
// unknown peer live. This lets a fan-out skip a peer we DEFINITIVELY know is down
// (turning an 8-15s dial into an instant "unreachable" row) without ever wrongly
// skipping a reachable peer on a fresh daemon.
func (d *Daemon) knownOfflinePeers() map[string]bool {
	return d.view.knownOffline()
}

// peerFanOutConcurrency bounds how many peers are asked at once. Until 2026-10-29
// every fan-out walked the registry SERIALLY, so the wall-clock cost was the SUM of
// the per-peer latencies and one slow peer delayed every peer behind it: on a 4-peer
// mesh a grid read cost ~300-550ms, and up to 4 x meshFetchTimeout when peers were
// merely slow rather than cached-offline. Asking them concurrently makes the cost the
// MAX of the per-peer latencies instead, so the ceiling is one meshFetchTimeout
// however many machines are registered.
const peerFanOutConcurrency = 8

// peerFanOut asks every peer for its slice CONCURRENTLY and flattens the answers.
//
// A peer in `down` (the liveness cache's definitively-offline set) is reported
// unreachable without a dial. Every other peer is attempted, and one whose fetch
// fails is reported unreachable rather than failing the whole call — an offline peer
// is an expected state here (read-replica/offline browsing), not an error.
//
// Output order is the REGISTRY's, not completion order: what a fan-out returns must
// not depend on which peer happened to answer first.
func peerFanOut[T any](list []peers.Peer, down map[string]bool, fetch func(peers.Peer) ([]T, error)) ([]T, []string) {
	slices := make([][]T, len(list))
	failed := make([]bool, len(list))
	var wg sync.WaitGroup
	sem := make(chan struct{}, peerFanOutConcurrency)
	for i, p := range list {
		if down[p.Machine] {
			failed[i] = true
			continue
		}
		wg.Add(1)
		go func(i int, p peers.Peer) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			slice, err := fetch(p)
			if err != nil {
				failed[i] = true
				return
			}
			slices[i] = slice
		}(i, p)
	}
	wg.Wait()

	var out []T
	var unreachable []string
	for i, p := range list {
		if failed[i] {
			unreachable = append(unreachable, p.Machine)
			continue
		}
		out = append(out, slices[i]...)
	}
	return out, unreachable
}

// fanOutThreads is the mesh fan-out behind GET /v1/threads?all-machines: it
// returns this machine's threads plus every reachable peer's threads (each
// already stamped with its owning machine), and the list of peers it could not
// reach. An offline peer is an expected state (read-replica/offline browsing),
// so it is reported in Unreachable rather than failing the whole call. Peers the
// liveness cache already knows are down are skipped up front (no wasted dial).
func (d *Daemon) fanOutThreads(local []api.Thread, includeArchived bool) ([]api.Thread, []string) {
	// An all-machines read is mesh demand: it doesn't read the snapshot cache
	// itself (it fans out live), but it leans on the cache's reachability gate
	// below — and it means someone is actively looking cross-machine, so the
	// idling mesh sync should come back to full cadence.
	d.noteMeshDemand()
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return local, nil
	}
	peerThreads, unreachable := peerFanOut(reg.List(), d.knownOfflinePeers(),
		func(p peers.Peer) ([]api.Thread, error) { return fetchPeerThreads(p, includeArchived) })
	return append(append([]api.Thread{}, local...), peerThreads...), unreachable
}

// peerRemoteClient builds an HTTP client for a peer on the http transport. Loud if
// the peer's token cannot be resolved — never a silent unauthenticated attempt.
func peerRemoteClient(p peers.Peer) (*client.Client, error) {
	token, err := p.ResolveAPIToken()
	if err != nil {
		return nil, err
	}
	return client.NewRemote(p.ApiAddr, token), nil
}

// fetchPeerThreads asks a peer's daemon for its threads over the peer's EXPLICIT
// transport (http via its TCP API, else a real ssh hop). A failure is returned to
// the caller, which reports the peer Unreachable (offline browsing) — never a silent
// transport downgrade.
func fetchPeerThreads(p peers.Peer, includeArchived bool) ([]api.Thread, error) {
	if p.Transport() == "http" {
		c, err := peerRemoteClient(p)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
		defer cancel()
		resp, err := c.ThreadList(ctx, includeArchived, false)
		if err != nil {
			return nil, err
		}
		return resp.Threads, nil
	}
	args := []string{
		"env",
		"SESH_HOME=" + shQuote(p.Home),
		"SESH_MACHINE=" + shQuote(p.Machine),
		shQuote(p.Binary),
		"thread", "list", "--json",
	}
	if includeArchived {
		args = append(args, "--archived")
	}
	sshArgs := append(peers.SSHMultiplexArgs(), p.SSHArgs()...)
	sshArgs = append(sshArgs, p.SSH, strings.Join(args, " "))
	cmd := exec.Command("ssh", sshArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var out []api.Thread
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line == "" {
			continue
		}
		var th api.Thread
		if err := json.Unmarshal([]byte(line), &th); err != nil {
			return nil, err
		}
		out = append(out, th)
	}
	return out, nil
}

// fetchPeerMasterCurrent resolves master-current for `origin` on `machine`'s work server
// by asking that peer's daemon — over the peer's EXPLICIT transport (http via its warm TCP
// API connection, else a real ssh hop). The peer is queried with origin only (it resolves
// in-process), so a pre-36 peer serves it unchanged. A failure is returned to the handler
// (which surfaces it as 502 → the TUI treats a failed resolve as an empty, no-op preselect)
// — never a silent transport downgrade.
func (d *Daemon) fetchPeerMasterCurrent(machine, origin string) (api.MasterCurrentResponse, error) {
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return api.MasterCurrentResponse{}, err
	}
	p, ok := reg.Get(machine)
	if !ok {
		return api.MasterCurrentResponse{}, fmt.Errorf("master-current: unknown machine %q: no peer registered", machine)
	}
	if p.Transport() == "http" {
		c, err := peerRemoteClient(p)
		if err != nil {
			return api.MasterCurrentResponse{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
		defer cancel()
		session, tid, window, err := c.TmuxMasterCurrent(ctx, origin, "")
		if err != nil {
			return api.MasterCurrentResponse{}, err
		}
		return api.MasterCurrentResponse{Schema: api.SchemaVersion, ThreadID: tid, Session: session, Window: window}, nil
	}
	args := []string{
		"env",
		"SESH_HOME=" + shQuote(p.Home),
		"SESH_MACHINE=" + shQuote(p.Machine),
		shQuote(p.Binary),
		"tmux", "master-current", "--origin", shQuote(origin), "--json",
	}
	sshArgs := append(peers.SSHMultiplexArgs(), p.SSHArgs()...)
	sshArgs = append(sshArgs, p.SSH, strings.Join(args, " "))
	out, err := exec.Command("ssh", sshArgs...).Output()
	if err != nil {
		return api.MasterCurrentResponse{}, err
	}
	var resp api.MasterCurrentResponse
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
		return api.MasterCurrentResponse{}, err
	}
	return resp, nil
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
