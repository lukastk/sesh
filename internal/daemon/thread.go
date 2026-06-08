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
)

func (d *Daemon) routesThreads(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads", d.handleThreadNew)
	mux.HandleFunc("GET /v1/threads", d.handleThreadList)
	mux.HandleFunc("POST /v1/threads/kill", d.handleThreadKill)
	mux.HandleFunc("GET /v1/threads/pane", d.handleThreadPane)
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
