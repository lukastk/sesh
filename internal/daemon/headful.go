package daemon

import (
	"errors"
	"net/http"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesHeadful(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/headful", d.handleThreadHeadful)
}

// handleThreadHeadful promotes a LIVE headless thread to headed: it spawns the agent
// in a real tmux pane resuming the conversation (the same machinery as resume), and
// flips the record to headed. Honesty:
//   - a turn in flight => 409 (never spawn a pane mid-turn — it would fork the
//     conversation; mirrors send-headless's in-flight guard). We RESERVE the in-flight
//     slot for the duration so a turn cannot start underneath the promotion.
//   - a codex thread with no minted session id yet (no first turn) => explicit N/A,
//     never faked.
func (d *Daemon) handleThreadHeadful(w http.ResponseWriter, r *http.Request) {
	var req api.ThreadHeadfulRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "headful: id is required")
		return
	}
	thread, err := d.store.GetThread(req.ID)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, "thread not found: "+req.ID)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !thread.Headless {
		writeError(w, http.StatusBadRequest, "headful: thread is already headed")
		return
	}

	// Reserve the in-flight slot: 409 if a turn is already running, else claim it so no
	// turn can start while we resume the session into a pane. Released on every exit
	// (on success the thread is headed, so the slot is moot, but we clean it up anyway).
	d.hlMu.Lock()
	if d.hlInFlight[req.ID] {
		d.hlMu.Unlock()
		writeError(w, http.StatusConflict, "headful: a turn is in flight for this thread — try again when it is waiting")
		return
	}
	d.hlInFlight[req.ID] = true
	d.hlMu.Unlock()
	defer func() {
		d.hlMu.Lock()
		delete(d.hlInFlight, req.ID)
		d.hlMu.Unlock()
	}()

	kind := agents.Kind(thread.AgentKind)
	sessionID := thread.AgentSessionID
	// codex mints its session id on the first turn; recover it from rollouts, and if
	// there is none (no first turn yet) it legitimately cannot be resumed => N/A.
	if kind == agents.Codex && sessionID == "" {
		codexHome, err := agents.CodexHome(d.cfg.CodexHome)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		id, found, err := agents.DiscoverCodexSession(codexHome, thread.Cwd, thread.CreatedAtUnix)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found {
			writeError(w, http.StatusUnprocessableEntity,
				"headful: codex thread has no session id (no first turn yet) — nothing to resume into a pane (N/A)")
			return
		}
		sessionID = id
		d.store.SetThreadAgentSession(req.ID, sessionID) //nolint:errcheck
	}
	if sessionID == "" {
		writeError(w, http.StatusUnprocessableEntity, "headful: thread has no captured agent session id")
		return
	}

	session := "sesh_" + sanitizeName(thread.Name)
	if d.tmux.HasSession(session) {
		writeError(w, http.StatusConflict, "headful: a tmux session "+session+" already exists")
		return
	}

	env := map[string]string{agents.EnvThreadID: req.ID}
	if err := d.prepCodexEnv(kind, env, thread.Cwd); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := d.tmux.CreateSessionCmd(session, thread.Cwd, env, agents.ResumeCommand(kind, sessionID)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pane, err := d.tmux.SessionFirstPane(session)
	if err != nil {
		d.tmux.KillSession(session) //nolint:errcheck
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := d.tmux.SetPaneThreadID(pane, req.ID); err != nil {
		d.tmux.KillSession(session) //nolint:errcheck
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Flip the record to headed with its real session name. On failure, tear the pane
	// back down so we don't leave a live pane with a still-headless record.
	if err := d.store.SetThreadHeaded(req.ID, session); err != nil {
		d.tmux.KillSession(session) //nolint:errcheck
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	thread, _ = d.store.GetThread(req.ID)
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}
