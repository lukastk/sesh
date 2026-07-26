package daemon

// State authority (schema 43, issue #4 — _dev/STATE_AUTHORITY.md): an in-agent
// reporter (a pi extension, a claude hook) POSTs turn-lifecycle facts here, and
// the maintainer prefers them over the pane content-diff heuristic for that
// thread's busy axis. The authority map is pure RUNTIME state (never persisted
// — a daemon restart degrades every thread to the heuristic floor until its
// reporter's next event, which is correct: runtime is always re-derived).
// Authority is bounded by pane liveness: the maintainer clears a thread's entry
// on every no-runtime path, so a reporter that dies silently mid-turn can pin
// busy only while its agent's pane is actually still alive.

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
)

// authorityState is one thread's live reported state. Guarded by d.authMu.
type authorityState struct {
	busy           bool
	blocked        bool
	blockedReason  string
	seq            int64
	source         string
	reportedAtUnix int64
}

// spawnEnv is the env injected into every spawned/revived/into-pane agent: the
// thread id (self-identification) and this daemon's own binary path (SESH_BIN)
// for in-agent state reporters — see agents.EnvSeshBin for why PATH resolution
// is not trustworthy from inside a pane.
func (d *Daemon) spawnEnv(id string) map[string]string {
	env := map[string]string{agents.EnvThreadID: id}
	if exe, err := os.Executable(); err == nil {
		env[agents.EnvSeshBin] = exe
	}
	return env
}

func (d *Daemon) routesReportState(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/report-state", d.handleThreadReportState)
}

// reportState validates and applies one reporter event. It is the only writer
// of the authority map besides clearAuthority. Returns the HTTP status to
// answer with on error (the handler is a thin wrapper; tests call this too).
func (d *Daemon) reportState(req api.ReportStateRequest, nowUnix int64) (int, error) {
	if req.ThreadID == "" {
		return http.StatusBadRequest, fmt.Errorf("report-state: thread_id is required")
	}
	if req.Source == "" {
		return http.StatusBadRequest, fmt.Errorf("report-state: source is required")
	}
	switch req.Event {
	case api.ReportTurnStarted, api.ReportTurnEnded, api.ReportTurnEndedNoAuthority, api.ReportBlocked, api.ReportUnblocked, api.ReportRelease:
	default:
		return http.StatusBadRequest, fmt.Errorf("report-state: unknown event %q (want %s|%s|%s|%s|%s|%s)",
			req.Event, api.ReportTurnStarted, api.ReportTurnEnded, api.ReportTurnEndedNoAuthority, api.ReportBlocked, api.ReportUnblocked, api.ReportRelease)
	}
	th, err := d.store.GetThread(req.ThreadID)
	if err != nil {
		return http.StatusNotFound, fmt.Errorf("report-state: %w", err)
	}
	if api.NonAgentKind(th.AgentKind) {
		return http.StatusConflict, fmt.Errorf("report-state: thread %s is a %s node — it runs no agent", th.ID, th.AgentKind)
	}
	// Schema 46: a reporter that knows the agent's own conversation id stamps
	// it here — the authoritative capture for codex's late-minted id (before
	// this, a headed codex thread's id was only recovered by ambiguous
	// cwd+time rollout discovery at revive, which mis-lands when two codex
	// threads share a cwd — ticket 49d4299b). The live pane's reporter IS the
	// thread's conversation, so a differing stored id (a past mis-discovery,
	// or the agent starting a new conversation in place) is corrected, loudly.
	if req.AgentSessionID != "" && req.AgentSessionID != th.AgentSessionID {
		if th.AgentSessionID != "" {
			log.Printf("report-state: thread %s agent session id corrected %s -> %s (reported by %s; the stored id was stale or mis-discovered)",
				th.ID, th.AgentSessionID, req.AgentSessionID, req.Source)
		}
		if serr := d.store.SetThreadAgentSession(th.ID, req.AgentSessionID); serr != nil {
			return http.StatusInternalServerError, fmt.Errorf("report-state: stamp agent session id: %w", serr)
		}
	}
	if req.Event == api.ReportTurnEndedNoAuthority {
		// codex's notify path: flag the turn end — attended or not (the gate
		// was removed 2026-07-25) — and touch NOTHING else: in particular no
		// authority entry, which would pin idle through real turns for a
		// turn-end-only reporter.
		if _, ferr := d.store.AutoFlag(req.ThreadID, "turn ended"); ferr != nil {
			return http.StatusInternalServerError, ferr
		}
		return 0, nil
	}

	d.authMu.Lock()
	defer d.authMu.Unlock()
	prev := d.auth[req.ThreadID]
	if prev != nil && req.Seq <= prev.seq {
		// Loud, never reordered: a late turn_started racing in after the
		// turn_ended that superseded it must not resurrect busy.
		return http.StatusConflict, fmt.Errorf("report-state: stale seq %d for thread %s (have %d from %s)",
			req.Seq, req.ThreadID, prev.seq, prev.source)
	}
	if req.Event == api.ReportRelease {
		// Idempotent: releasing absent authority is fine (the pane may have
		// died first, which already cleared it).
		delete(d.auth, req.ThreadID)
		return 0, nil
	}
	if d.auth == nil {
		d.auth = map[string]*authorityState{}
	}
	next := &authorityState{seq: req.Seq, source: req.Source, reportedAtUnix: nowUnix}
	switch req.Event {
	case api.ReportTurnStarted:
		next.busy = true // a fresh turn is never still blocked
	case api.ReportTurnEnded:
		// busy false, blocked cleared: a finished turn is never blocked.
	case api.ReportBlocked:
		// A blocked agent is MID-TURN by definition (stalled on the human), so
		// blocked always implies busy — even if a late/lost report left the
		// entry idle, or the daemon restarted mid-turn and has no entry.
		next.busy = true
		next.blocked = true
		next.blockedReason = req.Reason
		// Within ONE stall episode the FIRST reason wins: claude fires
		// PreToolUse (the actual question text) and then a generic
		// Notification ("needs your permission") for the same prompt, and the
		// specific reason must not be overwritten by the backstop.
		if prev != nil && prev.blocked && prev.blockedReason != "" {
			next.blockedReason = prev.blockedReason
		}
	case api.ReportUnblocked:
		// The stall resolved and the turn continues: busy stays (or becomes)
		// true; turn_ended is what ends it.
		next.busy = true
	}
	d.auth[req.ThreadID] = next
	return 0, nil
}

// reportedState returns a copy of the thread's live authority entry, if one
// exists. Read by the maintainer each tick.
func (d *Daemon) reportedState(id string) (authorityState, bool) {
	d.authMu.Lock()
	defer d.authMu.Unlock()
	st := d.auth[id]
	if st == nil {
		return authorityState{}, false
	}
	return *st, true
}

// reportedBusy is reportedState reduced to the busy axis (test convenience).
func (d *Daemon) reportedBusy(id string) (busy, ok bool) {
	st, ok := d.reportedState(id)
	return st.busy, ok
}

// clearAuthority drops a thread's authority entry. The maintainer calls it on
// every no-runtime path (pane gone, agent exited, pane vanished mid-tick) —
// the pane-liveness bound that stops a dead reporter from pinning busy.
func (d *Daemon) clearAuthority(id string) {
	d.authMu.Lock()
	defer d.authMu.Unlock()
	delete(d.auth, id)
}

func (d *Daemon) handleThreadReportState(w http.ResponseWriter, r *http.Request) {
	var req api.ReportStateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if status, err := d.reportState(req, time.Now().Unix()); err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "reported": req.ThreadID})
}
