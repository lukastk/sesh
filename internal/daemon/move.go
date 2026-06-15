package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/blobs"
	"github.com/lukastk/sesh/internal/peers"
)

// handleTicketMove is the daemon-coordinated cross-machine relocation. THIS daemon
// (the one you invoked it on) is the coordinator: it pulls the ticket and every
// blob its prompt references from `from`, pushes them to `to`, then deletes the
// source — over its OWN peer transport (same machinery as fanout/meshsync), so only
// the coordinator must reach both ends. It NEVER deletes the source unless the push
// fully succeeded (a duplicate is content-addressed and recoverable; data loss is not).
func (d *Daemon) handleTicketMove(w http.ResponseWriter, r *http.Request) {
	var req api.MoveTicketRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	from := req.From
	if from == "" {
		from = d.cfg.Machine
	}
	if req.ID == "" || req.To == "" {
		writeError(w, http.StatusBadRequest, "ticket move: id and --to are required")
		return
	}
	if from == req.To {
		writeError(w, http.StatusBadRequest, "ticket move: --from and --to are the same machine")
		return
	}

	// 1. Pull the record from the source.
	tk, err := d.getTicket(from, req.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("ticket move: read %s from %s: %v", req.ID, from, err))
		return
	}
	// 2. Carry every blob the prompt references (content-addressed ⇒ token stays valid).
	for _, ref := range blobs.References(tk.Prompt) {
		name, data, err := d.getBlob(from, ref)
		if err != nil {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("ticket move: prompt references blob @blob(%s) not on %s: %v", ref, from, err))
			return
		}
		if err := d.addBlob(req.To, name, data); err != nil {
			writeError(w, http.StatusBadGateway,
				fmt.Sprintf("ticket move: copy blob @blob(%s) to %s: %v", ref, req.To, err))
			return
		}
	}
	// 3. Push the record to the destination. Only after this succeeds do we delete.
	if err := d.importTicket(req.To, tk); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("ticket move: import to %s: %v", req.To, err))
		return
	}
	// 4. Delete the source (the move is now durable on the destination).
	if err := d.deleteTicket(from, req.ID); err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Sprintf("ticket move: imported to %s but FAILED to delete source on %s (duplicate left): %v", req.To, from, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "moved": req.ID, "from": from, "to": req.To})
}

// peer resolves a machine name to a registered peer (loud if unknown).
func (d *Daemon) peer(machine string) (peers.Peer, error) {
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return peers.Peer{}, err
	}
	p, ok := reg.Get(machine)
	if !ok {
		return peers.Peer{}, fmt.Errorf("unknown machine %q: no peer registered", machine)
	}
	return p, nil
}

// isSelf reports whether a machine name is this daemon's own machine.
func (d *Daemon) isSelf(machine string) bool {
	return machine == "" || machine == d.cfg.Machine
}

// sshSesh runs `sesh <args>` on a peer over a real ssh hop (the peer's env makes it
// hit the peer's own daemon), optionally feeding stdin. Mirrors the fanout ssh path.
func (d *Daemon) sshSesh(p peers.Peer, stdin []byte, args ...string) ([]byte, error) {
	remote := []string{"env", "SESH_HOME=" + shQuote(p.Home), "SESH_MACHINE=" + shQuote(p.Machine)}
	if p.CodexHome != "" {
		remote = append(remote, "SESH_CODEX_HOME="+shQuote(p.CodexHome))
	}
	remote = append(remote, shQuote(p.Binary))
	for _, a := range args {
		remote = append(remote, shQuote(a))
	}
	sshArgs := append([]string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no"}, p.SSHArgs()...)
	sshArgs = append(sshArgs, p.SSH, strings.Join(remote, " "))
	cmd := exec.Command("ssh", sshArgs...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// --- cross-machine ticket + blob primitives (local / http / ssh) ---

func (d *Daemon) getTicket(machine, id string) (api.Ticket, error) {
	if d.isSelf(machine) {
		return d.store.GetTicket(id)
	}
	p, err := d.peer(machine)
	if err != nil {
		return api.Ticket{}, err
	}
	if p.Transport() == "http" {
		c, err := peerRemoteClient(p)
		if err != nil {
			return api.Ticket{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
		defer cancel()
		resp, err := c.TicketGet(ctx, id)
		return resp.Ticket, err
	}
	out, err := d.sshSesh(p, nil, "ticket", "get", "--id", id, "--json")
	if err != nil {
		return api.Ticket{}, err
	}
	var tk api.Ticket
	return tk, json.Unmarshal(bytes.TrimSpace(out), &tk)
}

func (d *Daemon) importTicket(machine string, tk api.Ticket) error {
	if d.isSelf(machine) {
		_, err := d.importTicketLocal(tk)
		return err
	}
	p, err := d.peer(machine)
	if err != nil {
		return err
	}
	if p.Transport() == "http" {
		c, err := peerRemoteClient(p)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
		defer cancel()
		_, err = c.TicketImport(ctx, api.ImportTicketRequest{Ticket: tk})
		return err
	}
	body, err := json.Marshal(tk)
	if err != nil {
		return err
	}
	_, err = d.sshSesh(p, body, "ticket", "import")
	return err
}

func (d *Daemon) deleteTicket(machine, id string) error {
	if d.isSelf(machine) {
		return d.store.DeleteTicket(id)
	}
	p, err := d.peer(machine)
	if err != nil {
		return err
	}
	if p.Transport() == "http" {
		c, err := peerRemoteClient(p)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
		defer cancel()
		return c.TicketDelete(ctx, id)
	}
	_, err = d.sshSesh(p, nil, "ticket", "delete", "--id", id)
	return err
}

func (d *Daemon) getBlob(machine, hash string) (name string, data []byte, err error) {
	if d.isSelf(machine) {
		return d.blobs.Bytes(hash)
	}
	p, err := d.peer(machine)
	if err != nil {
		return "", nil, err
	}
	if p.Transport() == "http" {
		c, err := peerRemoteClient(p)
		if err != nil {
			return "", nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
		defer cancel()
		return c.BlobGet(ctx, hash)
	}
	// Over ssh: one `blob ls --json` to map the prefix → full hash + name, then `get`.
	lsOut, err := d.sshSesh(p, nil, "blob", "ls", "--json")
	if err != nil {
		return "", nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(lsOut)), "\n") {
		if line == "" {
			continue
		}
		var b api.BlobInfo
		if err := json.Unmarshal([]byte(line), &b); err != nil {
			return "", nil, err
		}
		if strings.HasPrefix(b.Hash, strings.ToLower(hash)) {
			data, err := d.sshSesh(p, nil, "blob", "get", b.Hash)
			return b.Name, data, err
		}
	}
	return "", nil, fmt.Errorf("no blob matches %q on %s", hash, machine)
}

func (d *Daemon) addBlob(machine, name string, data []byte) error {
	if d.isSelf(machine) {
		_, err := d.blobs.Add(name, data)
		return err
	}
	p, err := d.peer(machine)
	if err != nil {
		return err
	}
	if p.Transport() == "http" {
		c, err := peerRemoteClient(p)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
		defer cancel()
		_, err = c.BlobAdd(ctx, name, data)
		return err
	}
	_, err = d.sshSesh(p, data, "blob", "add", "--stdin", "--name", name)
	return err
}
