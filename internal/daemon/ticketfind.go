package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"sync"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
	"github.com/lukastk/sesh/internal/store"
)

// handleTicketFind serves GET /v1/tickets/find?id=<id> — a MESH-WIDE lookup of a
// ticket by id. Tickets are per-daemon (each daemon owns its own; they are spread
// across the mesh), so this resolves THIS daemon's store first and, if absent, fans
// out to every peer (each answering local-only, so there is no recursion) and returns
// the first hit. The reply carries the ticket record PLUS its owning machine and
// bound-thread context, so a remote client (the Obsidian ticket note) renders its
// decorator + panel from one call without knowing which machine the ticket is on.
//
// found=false is returned with a 200, not a 404: a ticket simply not existing on the
// mesh is a LEGITIMATE state (a draft note has no ticket; a deleted ticket resolves to
// nothing) that the client uses for validation. A genuine transport failure to a peer
// is reported in Unreachable (the not-found answer may be incomplete), never silently
// dropped. `&local=1` makes this daemon answer ONLY from its own store (the leaf form
// the fan-out calls on each peer).
func (d *Daemon) handleTicketFind(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "ticket find: id is required")
		return
	}
	localOnly := r.URL.Query().Get("local") == "1"

	resp, found, err := d.findTicketLocal(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if found {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if localOnly {
		// Leaf form: this daemon does not have it; stop (the hub asked just us).
		writeJSON(w, http.StatusOK, api.TicketFindResponse{Schema: api.SchemaVersion, Found: false})
		return
	}

	// Hub form: not here — fan out to peers (each local-only), first hit wins.
	hit, unreachable := d.fanOutFindTicket(id)
	if hit != nil {
		hit.Unreachable = unreachable
		writeJSON(w, http.StatusOK, *hit)
		return
	}
	writeJSON(w, http.StatusOK, api.TicketFindResponse{Schema: api.SchemaVersion, Found: false, Unreachable: unreachable})
}

// findTicketLocal resolves a ticket id against THIS daemon's store only, returning the
// aggregate (ticket + owning machine + bound-thread context). found=false (nil error)
// when the id is not in this store. If the ticket is bound to a thread that no longer
// exists (deleted thread, ticket retains its binding), Thread is left nil — the client
// renders it as unattached rather than erroring.
func (d *Daemon) findTicketLocal(id string) (api.TicketFindResponse, bool, error) {
	t, err := d.store.GetTicket(id)
	if errors.Is(err, store.ErrTicketNotFound) {
		return api.TicketFindResponse{Schema: api.SchemaVersion, Found: false}, false, nil
	}
	if err != nil {
		return api.TicketFindResponse{}, false, err
	}
	resp := api.TicketFindResponse{
		Schema:  api.SchemaVersion,
		Found:   true,
		Machine: d.cfg.Machine,
		Ticket:  t,
	}
	if t.ThreadID != "" {
		th, err := d.store.GetThread(t.ThreadID)
		if err == nil {
			resp.Thread = &api.TicketThread{
				ID:      th.ID,
				Name:    th.Name,
				Parent:  th.Parent,
				Machine: d.cfg.Machine,
			}
		} else if !errors.Is(err, store.ErrThreadNotFound) {
			return api.TicketFindResponse{}, false, err
		}
	}
	return resp, true, nil
}

// fanOutFindTicket queries every peer's find endpoint (local-only) CONCURRENTLY and
// returns the first hit in registry order (deterministic), plus the machines it could
// not reach. A peer error is non-fatal (the ticket might be elsewhere or nowhere) — it
// is reported in unreachable so the caller knows a not-found answer may be incomplete.
func (d *Daemon) fanOutFindTicket(id string) (*api.TicketFindResponse, []string) {
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return nil, nil
	}
	list := reg.List()
	if len(list) == 0 {
		return nil, nil
	}
	type result struct {
		resp api.TicketFindResponse
		ok   bool // transport succeeded (resp valid, found may be true/false)
	}
	results := make([]result, len(list))
	var wg sync.WaitGroup
	for i, p := range list {
		i, p := i, p
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := fetchPeerTicketFind(p, id)
			if err != nil {
				results[i] = result{ok: false}
				return
			}
			results[i] = result{resp: resp, ok: true}
		}()
	}
	wg.Wait()

	var unreachable []string
	for i, p := range list {
		if !results[i].ok {
			unreachable = append(unreachable, p.Machine)
			continue
		}
		if results[i].resp.Found {
			hit := results[i].resp
			return &hit, unreachable
		}
	}
	return nil, unreachable
}

// handleTicketListAll serves GET /v1/tickets/list-all — a MESH-WIDE ticket listing
// for the ticket browser. This daemon's own tickets (each stamped with this machine
// and its bound-thread name) plus, unless `&local=1`, every reachable peer's tickets
// (each peer answering local-only — no recursion). Unreachable peers are reported,
// not dropped, so an incomplete listing is visible (offline browsing).
func (d *Daemon) handleTicketListAll(w http.ResponseWriter, r *http.Request) {
	localOnly := r.URL.Query().Get("local") == "1"
	local, err := d.listTicketsLocalEntries()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := api.TicketListAllResponse{Schema: api.SchemaVersion, Tickets: local}
	if !localOnly {
		peerEntries, unreachable := d.fanOutListTickets()
		out.Tickets = append(out.Tickets, peerEntries...)
		out.Unreachable = unreachable
	}
	if out.Tickets == nil {
		out.Tickets = []api.TicketListEntry{}
	}
	writeJSON(w, http.StatusOK, out)
}

// listTicketsLocalEntries lists THIS daemon's tickets as browser entries: each
// stamped with this machine and, if bound, its thread's display name (resolved from
// a single threads snapshot, so it is O(threads) not O(tickets·queries)).
func (d *Daemon) listTicketsLocalEntries() ([]api.TicketListEntry, error) {
	tickets, err := d.store.ListTickets()
	if err != nil {
		return nil, err
	}
	threads, err := d.store.ListThreads(true)
	if err != nil {
		return nil, err
	}
	nameByID := make(map[string]string, len(threads))
	for _, th := range threads {
		nameByID[th.ID] = th.Name
	}
	out := make([]api.TicketListEntry, 0, len(tickets))
	for _, t := range tickets {
		out = append(out, api.TicketListEntry{
			Ticket:     t,
			Machine:    d.cfg.Machine,
			ThreadName: nameByID[t.ThreadID], // "" if unbound or thread gone
		})
	}
	return out, nil
}

// fanOutListTickets queries every peer's list-all endpoint (local-only) CONCURRENTLY
// and merges the entries (already stamped with each peer's machine). A peer error is
// non-fatal — reported in unreachable so the caller knows the listing is incomplete.
func (d *Daemon) fanOutListTickets() ([]api.TicketListEntry, []string) {
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return nil, nil
	}
	list := reg.List()
	if len(list) == 0 {
		return nil, nil
	}
	type result struct {
		entries []api.TicketListEntry
		ok      bool
	}
	results := make([]result, len(list))
	var wg sync.WaitGroup
	for i, p := range list {
		i, p := i, p
		wg.Add(1)
		go func() {
			defer wg.Done()
			entries, err := fetchPeerTicketList(p)
			if err != nil {
				results[i] = result{ok: false}
				return
			}
			results[i] = result{entries: entries, ok: true}
		}()
	}
	wg.Wait()

	var merged []api.TicketListEntry
	var unreachable []string
	for i, p := range list {
		if !results[i].ok {
			unreachable = append(unreachable, p.Machine)
			continue
		}
		merged = append(merged, results[i].entries...)
	}
	return merged, unreachable
}

// fetchPeerTicketList asks ONE peer's daemon for its tickets (local-only, no further
// fan-out) over the peer's EXPLICIT transport — http via its TCP API, else a real ssh
// hop. A failure is returned to the caller (reported Unreachable); never a silent
// transport downgrade.
func fetchPeerTicketList(p peers.Peer) ([]api.TicketListEntry, error) {
	if p.Transport() == "http" {
		c, err := peerRemoteClient(p)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
		defer cancel()
		resp, err := c.TicketListAll(ctx, true)
		if err != nil {
			return nil, err
		}
		return resp.Tickets, nil
	}
	args := []string{
		"env",
		"SESH_HOME=" + shQuote(p.Home),
		"SESH_MACHINE=" + shQuote(p.Machine),
		shQuote(p.Binary),
		"ticket", "list", "--all-machines", "--local", "--json",
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
	var out []api.TicketListEntry
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line == "" {
			continue
		}
		var e api.TicketListEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// fetchPeerTicketFind asks ONE peer's daemon to resolve a ticket id from its own store
// (local-only, no further fan-out) over the peer's EXPLICIT transport — http via its
// TCP API, else a real ssh hop. A failure is returned to the caller (reported
// Unreachable); never a silent transport downgrade.
func fetchPeerTicketFind(p peers.Peer, id string) (api.TicketFindResponse, error) {
	if p.Transport() == "http" {
		c, err := peerRemoteClient(p)
		if err != nil {
			return api.TicketFindResponse{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
		defer cancel()
		return c.TicketFind(ctx, id, true)
	}
	args := []string{
		"env",
		"SESH_HOME=" + shQuote(p.Home),
		"SESH_MACHINE=" + shQuote(p.Machine),
		shQuote(p.Binary),
		"ticket", "find", "--id", shQuote(id), "--local", "--json",
	}
	sshArgs := append([]string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no"}, p.SSHArgs()...)
	sshArgs = append(sshArgs, p.SSH, strings.Join(args, " "))
	cmd := exec.Command("ssh", sshArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return api.TicketFindResponse{}, err
	}
	var out api.TicketFindResponse
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &out); err != nil {
		return api.TicketFindResponse{}, err
	}
	return out, nil
}
