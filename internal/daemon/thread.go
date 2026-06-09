package daemon

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

func (d *Daemon) routesThreads(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads", d.handleThreadNew)
	mux.HandleFunc("GET /v1/threads", d.handleThreadList)
	mux.HandleFunc("POST /v1/threads/kill", d.handleThreadKill)
	mux.HandleFunc("GET /v1/threads/pane", d.handleThreadPane)
	mux.HandleFunc("GET /v1/threads/status", d.handleThreadStatus)
	mux.HandleFunc("POST /v1/threads/send", d.handleThreadSend)
}

// handleThreadSend delivers a message into a headed thread's LIVE pane (resolved
// from the marker) and submits it. A thread with no live pane is dead — sending
// is a loud error, not a silent no-op.
func (d *Daemon) handleThreadSend(w http.ResponseWriter, r *http.Request) {
	var req api.ThreadSendRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "thread send: id and text are required")
		return
	}
	if _, err := d.store.GetThread(req.ID); err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, "thread not found: "+req.ID)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	loc, found, err := d.tmux.FindPaneByThreadID(req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusConflict, "thread has no live pane (dead); cannot send")
		return
	}
	if err := d.tmux.SendText(loc.Pane, req.Text, true); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "sent": req.ID})
}

// handleThreadNew spawns a headed thread: a real agent in a real tmux session,
// the pane stamped with the thread's @sesh-thread-id marker, SESH_THREAD_ID
// injected into the pane environment. Headless lands in a later phase and is an
// explicit NOT IMPLEMENTED error here (never a silent headed fallback).
func (d *Daemon) handleThreadNew(w http.ResponseWriter, r *http.Request) {
	var req api.NewThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kind, err := agents.ParseKind(req.Agent)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "thread: name is required")
		return
	}
	if req.Cwd == "" || !strings.HasPrefix(req.Cwd, "/") {
		writeError(w, http.StatusBadRequest, "thread: cwd must be an absolute path")
		return
	}
	if req.Headless {
		writeError(w, http.StatusNotImplemented, "NOT IMPLEMENTED: headless threads land in Phase 3b")
		return
	}

	id := uuid.NewString()
	session := "sesh_" + sanitizeName(req.Name)
	if d.tmux.HasSession(session) {
		writeError(w, http.StatusConflict, "thread: tmux session "+session+" already exists")
		return
	}

	env := map[string]string{agents.EnvThreadID: id}
	if err := d.tmux.CreateSessionCmd(session, req.Cwd, env, agents.HeadedCommand(kind)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Stamp the marker on the session's pane so the thread's pane is resolvable
	// at runtime (never stored).
	pane, err := d.tmux.SessionFirstPane(session)
	if err != nil {
		d.tmux.KillSession(session) // don't leak a half-spawned session
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := d.tmux.SetPaneThreadID(pane, id); err != nil {
		d.tmux.KillSession(session)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	thread := api.Thread{
		ID:            id,
		Machine:       d.cfg.Machine,
		SessionName:   session,
		Cwd:           req.Cwd,
		AgentKind:     string(kind),
		Name:          req.Name,
		Tags:          []string{},
		Headless:      false,
		CreatedAtUnix: time.Now().Unix(),
	}
	if err := d.store.InsertThread(thread); err != nil {
		d.tmux.KillSession(session) // keep store and runtime consistent
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}

func (d *Daemon) handleThreadList(w http.ResponseWriter, r *http.Request) {
	threads, err := d.store.ListThreads()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if threads == nil {
		threads = []api.Thread{}
	}
	writeJSON(w, http.StatusOK, api.ThreadListResponse{Schema: api.SchemaVersion, Threads: threads})
}

// handleThreadKill terminates the thread: kill its tmux session (which kills the
// agent) and delete the record. Killing a session that no longer exists is not
// an error — the runtime was already gone — but a missing record is.
func (d *Daemon) handleThreadKill(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "thread kill: id is required")
		return
	}
	thread, err := d.store.GetThread(id)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, "thread not found: "+id)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if d.tmux.HasSession(thread.SessionName) {
		if err := d.tmux.KillSession(thread.SessionName); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := d.store.DeleteThread(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "killed": id})
}

func (d *Daemon) handleThreadPane(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "thread pane: id is required")
		return
	}
	loc, found, err := d.tmux.FindPaneByThreadID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ResolvePaneResponse{Schema: api.SchemaVersion, Found: found, Pane: loc})
}

// The content-diff probe samples a pane several times and declares working only
// when change is SUSTAINED, not a one-off. A working TUI animates a spinner/
// timer continuously (many changes); an otherwise-idle TUI may still blip once
// (a rotating hint, an MCP server finishing startup), which must NOT read as
// working. Requiring a majority of intervals to change rejects the single blip
// while still catching a real turn.
const (
	activitySamples       = 4                      // captures
	activityInterval      = 380 * time.Millisecond // between captures (~1.14s total)
	activityChangedNeeded = 2                      // of activitySamples-1 intervals
)

// handleThreadStatus resolves a thread's LIVE runtime state (never stored) as
// two ORTHOGONAL axes (Phase 3b decision; see _dev/SPEC.md §3):
//
//   - Activity   {working|waiting|dead}: dead = no live agent under a marked
//     pane; otherwise working/waiting from a pane content-diff probe.
//   - Attachment {attached|detached}: from `tmux list-clients`.
//
// They are independent: a detached agent can still be working, and an idle agent
// still needs input whether or not anyone is attached.
func (d *Daemon) handleThreadStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "thread status: id is required")
		return
	}
	thread, err := d.store.GetThread(id)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, "thread not found: "+id)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := api.ThreadStatusResponse{Schema: api.SchemaVersion, ID: id}

	loc, found, err := d.tmux.FindPaneByThreadID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		// No marked live pane: dead and (necessarily) detached.
		resp.Activity = api.ActivityDead
		resp.Attachment = api.Detached
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Pane = loc.Pane

	// Attachment axis (independent of activity).
	clients, err := d.tmux.ClientCount(loc.Session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Clients = clients
	resp.Attachment = api.Detached
	if clients > 0 {
		resp.Attachment = api.Attached
	}

	// Activity axis.
	agent, running := tmux.AgentUnderPane(loc.PanePID)
	resp.AgentRunning = running && agent.Kind == thread.AgentKind
	if !resp.AgentRunning {
		// A marked pane but no live agent of the right kind: the agent exited.
		// dead — needs a restart.
		resp.Activity = api.ActivityDead
		writeJSON(w, http.StatusOK, resp)
		return
	}
	working, err := d.paneChanging(loc.Pane)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if working {
		resp.Activity = api.ActivityWorking
	} else {
		resp.Activity = api.ActivityWaiting
	}
	writeJSON(w, http.StatusOK, resp)
}

// paneChanging samples a pane's visible content several times and reports whether
// it is SUSTAINEDLY changing — the agent-agnostic "the agent is producing output /
// its TUI is animating a turn" signal that distinguishes working from waiting.
// A single idle blip (one changed interval) reads as waiting; a real turn (its
// spinner/output animating) changes a majority of intervals.
func (d *Daemon) paneChanging(pane string) (bool, error) {
	prev, err := d.tmux.CapturePane(pane)
	if err != nil {
		return false, err
	}
	changed := 0
	for i := 1; i < activitySamples; i++ {
		time.Sleep(activityInterval)
		cur, err := d.tmux.CapturePane(pane)
		if err != nil {
			return false, err
		}
		if cur != prev {
			changed++
		}
		prev = cur
	}
	return changed >= activityChangedNeeded, nil
}

// sanitizeName maps a thread name to a tmux-safe session suffix. tmux session
// names may not contain ":" or "." and we avoid whitespace; everything else
// becomes "_".
func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := b.String()
	if s == "" {
		s = "thread"
	}
	return s
}
