package daemon

// Server-owned thread-state waits (schema 43, issue #7 — the herdr
// `agent wait --until` idea). One request = one BOUNDED wait: the daemon
// polls its own maintained state every waitPollInterval for up to
// waitMaxPerRequest, then answers reached=true/false with the current state.
// Clients loop until their own deadline — this keeps each HTTP request short
// (the client transport has a hard 15s timeout) while remote waits still cost
// ONE routed hop total (the `--machine` router re-execs the whole CLI on the
// owner, so the loop runs against the owner's local socket).

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

const (
	waitPollInterval  = 100 * time.Millisecond
	waitMaxPerRequest = 10 * time.Second
)

func (d *Daemon) routesWait(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/threads/wait", d.handleThreadWait)
}

// waitConditionMet is the `until` vocabulary over a maintained snapshot.
func waitConditionMet(until string, s api.ThreadSnapshot) (bool, error) {
	switch until {
	case "busy":
		return s.Busy == api.BusyBusy, nil
	case "idle":
		return s.Busy == api.BusyIdle, nil
	case "blocked":
		return s.Blocked, nil
	case "settled":
		// The agent stopped running on its own: turn over, or stalled on the
		// human. (A blocked thread reads busy on the execution axis, so
		// `idle` alone would wait out an approval prompt forever.)
		return s.Busy == api.BusyIdle || s.Blocked, nil
	}
	return false, fmt.Errorf("wait: unknown until %q (want busy|idle|blocked|settled)", until)
}

func (d *Daemon) handleThreadWait(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	until := r.URL.Query().Get("until")
	if id == "" {
		writeError(w, http.StatusBadRequest, "wait: id is required")
		return
	}
	if _, err := waitConditionMet(until, api.ThreadSnapshot{}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	timeout := waitMaxPerRequest
	if ms := r.URL.Query().Get("timeout_ms"); ms != "" {
		n, err := strconv.Atoi(ms)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "wait: bad timeout_ms")
			return
		}
		if t := time.Duration(n) * time.Millisecond; t < timeout {
			timeout = t
		}
	}
	if _, err := d.store.GetThread(id); err != nil {
		d.threadOpErr(w, err)
		return
	}

	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(waitPollInterval)
	defer tick.Stop()
	for {
		// A just-created thread may not have a published snapshot yet; that
		// simply reads as condition-not-met until the maintainer's first tick.
		snap, tracked := d.maint.stateOf(id)
		met := false
		if tracked {
			met, _ = waitConditionMet(until, snap) // until validated above
		}
		if met || time.Now().After(deadline) {
			writeJSON(w, http.StatusOK, api.ThreadWaitResponse{
				Schema: api.SchemaVersion, ID: id, Reached: met,
				Head: snap.Head, Busy: snap.Busy, Blocked: snap.Blocked,
				LastActiveUnix: snap.LastActiveUnix,
			})
			return
		}
		select {
		case <-r.Context().Done():
			return // client went away; nothing to answer
		case <-tick.C:
		}
	}
}
