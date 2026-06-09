package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/peers"
)

// fanOutThreads is the mesh fan-out behind GET /v1/threads?all-machines: it
// returns this machine's threads plus every reachable peer's threads (each
// already stamped with its owning machine), and the list of peers it could not
// reach. An offline peer is an expected state (read-replica/offline browsing),
// so it is reported in Unreachable rather than failing the whole call.
func (d *Daemon) fanOutThreads(local []api.Thread, includeArchived bool) ([]api.Thread, []string) {
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return local, nil
	}
	merged := append([]api.Thread{}, local...)
	var unreachable []string
	for _, p := range reg.List() {
		peerThreads, err := fetchPeerThreads(p, includeArchived)
		if err != nil {
			unreachable = append(unreachable, p.Machine)
			continue
		}
		merged = append(merged, peerThreads...)
	}
	return merged, unreachable
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
	sshArgs := append([]string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no"}, p.SSHArgs()...)
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

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
