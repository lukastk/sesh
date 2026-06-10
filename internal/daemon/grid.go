package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"sync"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
)

func (d *Daemon) routesGrid(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/threads/grid", d.handleThreadGrid)
}

// gridConcurrency bounds how many thread statuses are probed at once. Each headed
// probe takes ~a probe window; running them concurrently keeps the whole grid to
// roughly ONE window regardless of thread count (so a live TUI stays responsive).
const gridConcurrency = 8

// handleThreadGrid returns every thread with its LIVE status, computed
// concurrently. With ?all-machines it fans out across the mesh. This is the one
// cheap call the TUI polls to render the grid.
func (d *Daemon) handleThreadGrid(w http.ResponseWriter, r *http.Request) {
	includeArchived := r.URL.Query().Get("archived") == "1"
	threads, err := d.store.ListThreads(includeArchived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	tickets, err := d.store.OpenTicketCounts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows := make([]api.ThreadRow, len(threads))
	var wg sync.WaitGroup
	sem := make(chan struct{}, gridConcurrency)
	for i := range threads {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i] = d.resolveRow(threads[i], tickets)
		}(i)
	}
	wg.Wait()

	resp := api.ThreadGridResponse{Schema: api.SchemaVersion, Rows: rows}
	if r.URL.Query().Get("all-machines") == "1" {
		peerRows, unreachable := d.fanOutGrid(includeArchived)
		resp.Rows = append(resp.Rows, peerRows...)
		resp.Unreachable = unreachable
	}
	if resp.Rows == nil {
		resp.Rows = []api.ThreadRow{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// resolveRow returns a thread's live state axes + attachment. Fast path: the
// background maintainer's O(1) maintained state (so the whole grid is a memory
// read, not N concurrent ~3s probes). Fallback: an on-demand resolve for a thread
// the maintainer has not ticked yet (just created) — correctness without a tick.
func (d *Daemon) resolveRow(th api.Thread, tickets map[string]int) api.ThreadRow {
	if snap, ok := d.maint.stateOf(th.ID); ok {
		return api.ThreadRow{Thread: th, Head: snap.Head, Busy: snap.Busy, Attachment: snap.Attachment, TicketsOpen: tickets[th.ID]}
	}
	row := api.ThreadRow{Thread: th, Attachment: api.Detached, TicketsOpen: tickets[th.ID]}
	head, busy, err := d.resolveState(th)
	if err != nil {
		head, busy = api.Headless, api.BusyIdle
	}
	row.Head, row.Busy = head, busy
	if loc, found, _ := d.tmux.FindPaneByThreadID(th.ID); found {
		if clients, _ := d.tmux.ClientCount(loc.Session); clients > 0 {
			row.Attachment = api.Attached
		}
	}
	return row
}

// fanOutGrid asks each peer for its grid (status included) over a real ssh hop.
func (d *Daemon) fanOutGrid(includeArchived bool) ([]api.ThreadRow, []string) {
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return nil, nil
	}
	var rows []api.ThreadRow
	var unreachable []string
	for _, p := range reg.List() {
		pr, err := fetchPeerGrid(p, includeArchived)
		if err != nil {
			unreachable = append(unreachable, p.Machine)
			continue
		}
		rows = append(rows, pr...)
	}
	return rows, unreachable
}

// fetchPeerGrid asks a peer's daemon for its live-status grid over the peer's
// EXPLICIT transport (http via its TCP API, else a real ssh hop).
func fetchPeerGrid(p peers.Peer, includeArchived bool) ([]api.ThreadRow, error) {
	if p.Transport() == "http" {
		c, err := peerRemoteClient(p)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
		defer cancel()
		resp, err := c.ThreadGrid(ctx, includeArchived, false)
		if err != nil {
			return nil, err
		}
		return resp.Rows, nil
	}
	args := []string{
		"env", "SESH_HOME=" + shQuote(p.Home), "SESH_MACHINE=" + shQuote(p.Machine),
		shQuote(p.Binary), "thread", "grid", "--json",
	}
	if includeArchived {
		args = append(args, "--archived")
	}
	sshArgs := append([]string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no"}, p.SSHArgs()...)
	sshArgs = append(sshArgs, p.SSH, strings.Join(args, " "))
	out, err := exec.Command("ssh", sshArgs...).Output()
	if err != nil {
		return nil, err
	}
	var rows []api.ThreadRow
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var row api.ThreadRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}
