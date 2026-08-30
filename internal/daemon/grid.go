package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/peers"
	"github.com/lukastk/sesh/internal/store"
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

	tickets, err := d.store.OpenTicketDigests()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Effective hold deadlines over the FULL thread set (incl. archived) so a child's
	// inherited hold resolves even when this view hides its archived ancestor.
	holdThreads := threads
	if !includeArchived {
		if all, aerr := d.store.ListThreads(true); aerr == nil {
			holdThreads = all
		}
	}
	effHolds := effectiveHolds(holdThreads, time.Now().Unix())
	rows := make([]api.ThreadRow, len(threads))
	var wg sync.WaitGroup
	sem := make(chan struct{}, gridConcurrency)
	for i := range threads {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i] = d.resolveRow(threads[i], tickets, effHolds[threads[i].ID])
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
func (d *Daemon) resolveRow(th api.Thread, tickets map[string]store.TicketDigest, effHoldUntil int64) api.ThreadRow {
	dg := tickets[th.ID]
	// "On hold now" derives from the EFFECTIVE (own + inherited) deadline vs this
	// machine's clock (the owner is authoritative); auto-expires once now passes, and a
	// child stays held while an ancestor's hold is in the future.
	onHold := effHoldUntil > time.Now().Unix()
	// Use the FRESH digest (not snap.TicketsOpen) so a ticket created/closed since the
	// last maintainer tick shows immediately; needs-input combines it with the
	// maintained head/busy.
	if snap, ok := d.maint.stateOf(th.ID); ok {
		return api.ThreadRow{Thread: th, Head: snap.Head, Busy: snap.Busy, Attachment: snap.Attachment,
			TicketsOpen: dg.Count, TicketName: dg.NewestName,
			TicketNeedsInput: dg.HasActive && snap.Head == api.Headful && snap.Busy == api.BusyIdle,
			CwdRel:           snap.CwdRel, OnHold: onHold, OnHoldEffectiveUnix: effHoldUntil,
			StateAuthority:   snap.StateAuthority}
	}
	// NB the on-demand fallback below never runs the rolling probe, so it
	// carries no state_authority/blocked (unknown, omitted) by design.
	row := api.ThreadRow{Thread: th, Attachment: api.Detached, TicketsOpen: dg.Count, TicketName: dg.NewestName,
		CwdRel: config.TildeRelative(th.Cwd, d.maint.home), OnHold: onHold, OnHoldEffectiveUnix: effHoldUntil}
	head, busy, err := d.resolveState(th)
	if err != nil {
		head, busy = api.Headless, api.BusyIdle
	}
	row.Head, row.Busy = head, busy
	row.TicketNeedsInput = dg.HasActive && head == api.Headful && busy == api.BusyIdle
	if loc, found, _ := d.tmux.FindPaneByThreadID(th.ID); found {
		if clients, _ := d.tmux.ClientCount(loc.Session); clients > 0 {
			row.Attachment = api.Attached
		}
	}
	return row
}

// fanOutGrid asks each peer for its grid (status included) over its configured
// transport. Peers the liveness cache already knows are down are skipped up front, so
// one offline machine no longer costs the caller a full per-peer timeout.
func (d *Daemon) fanOutGrid(includeArchived bool) ([]api.ThreadRow, []string) {
	d.noteMeshDemand() // all-machines demand — see fanOutThreads
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return nil, nil
	}
	down := d.knownOfflinePeers()
	var rows []api.ThreadRow
	var unreachable []string
	for _, p := range reg.List() {
		if down[p.Machine] {
			unreachable = append(unreachable, p.Machine)
			continue
		}
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
	sshArgs := append(peers.SSHMultiplexArgs(), p.SSHArgs()...)
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
