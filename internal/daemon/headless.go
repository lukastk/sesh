package daemon

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesHeadless(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/send-headless", d.handleThreadSendHeadless)
	mux.HandleFunc("GET /v1/threads/headless-reply", d.handleThreadHeadlessReply)
}

// newHeadlessThread creates a headless thread record (stateless-per-turn): a
// durable conversation with NO tmux window. pi/claude get a sesh-assigned agent
// session id up front; codex generates its own on the first turn (left empty).
func (d *Daemon) newHeadlessThread(w http.ResponseWriter, kind agents.Kind, req api.NewThreadRequest) {
	id := uuid.NewString()
	agentSessionID := ""
	if kind == agents.Pi || kind == agents.Claude {
		agentSessionID = uuid.NewString() // sesh pre-assigns; codex cannot
	}
	thread := api.Thread{
		ID:             id,
		Machine:        d.cfg.Machine,
		SessionName:    "headless-" + id, // logical name; no tmux session exists
		Cwd:            req.Cwd,
		AgentKind:      string(kind),
		Name:           req.Name,
		Tags:           []string{},
		CreatedAtUnix:  time.Now().Unix(),
		AgentSessionID: agentSessionID,
		Parent:         req.Parent,
		Notify:         d.defaults.NotifyDefault(),
		// HeadlessStarted stays false: the conversation begins on the first turn
		// (codex mints its session id there; claude/pi create from the pre-assigned id).
	}
	if err := d.store.InsertThread(thread); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}

// handleThreadSendHeadless delivers a turn to a headless thread. It runs the turn
// in the BACKGROUND and returns immediately; the thread is "working" (live) while
// the turn process is in flight and "waiting" once it completes, with the reply
// retrievable via headless-reply. This is how sesh tells a headless thread is
// still working.
func (d *Daemon) handleThreadSendHeadless(w http.ResponseWriter, r *http.Request) {
	var req api.ThreadSendRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "send-headless: id and text are required")
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
	// Unified model: a headless turn is valid on any IDLE thread — including one
	// that previously ran headed (its conversation resumes per-turn). A LIVE pane
	// owns the conversation, so a concurrent headless turn would fork it: loud 409.
	if _, found, err := d.tmux.FindPaneByThreadID(req.ID); err == nil && found {
		writeError(w, http.StatusConflict, "thread has a live pane — send into the pane (thread send), or stop it first")
		return
	}

	// Refuse to overlap turns (two concurrent resumes of one session fork the
	// conversation) — a loud conflict, not a silent race.
	d.hlMu.Lock()
	if d.hlInFlight[req.ID] {
		d.hlMu.Unlock()
		writeError(w, http.StatusConflict, "a turn is already in flight for this thread")
		return
	}
	d.hlInFlight[req.ID] = true
	d.hlMu.Unlock()

	codexHome := ""
	if thread.AgentKind == string(agents.Codex) {
		codexHome, _ = agents.CodexHome(d.cfg.CodexHome)
	}

	// A HEADED-BORN codex thread began its conversation in a pane, but codex mints
	// its session id itself — recover it from the rollouts (same as revive) so the
	// turn CONTINUES that conversation instead of silently starting a fresh one.
	// No rollout = no turn ever ran in the pane = nothing to continue: loud N/A.
	sessionID, started := thread.AgentSessionID, thread.HeadlessStarted
	if thread.AgentKind == string(agents.Codex) && started && sessionID == "" {
		releaseSlot := func() {
			d.hlMu.Lock()
			delete(d.hlInFlight, req.ID)
			d.hlMu.Unlock()
		}
		discovered, found, err := agents.DiscoverCodexSession(codexHome, thread.Cwd, thread.CreatedAtUnix)
		if err != nil {
			releaseSlot()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found {
			releaseSlot()
			writeError(w, http.StatusUnprocessableEntity,
				"send-headless: codex thread has no session id (no turn has ever run) — nothing to continue (N/A)")
			return
		}
		sessionID = discovered
		d.store.SetThreadAgentSession(req.ID, sessionID) //nolint:errcheck
	}

	go func() {
		reply, newSessionID, runErr := agents.HeadlessTurn(
			agents.Kind(thread.AgentKind), req.ID, sessionID, thread.Cwd, started, req.Text, codexHome)

		d.hlMu.Lock()
		delete(d.hlInFlight, req.ID)
		if runErr == nil {
			d.hlReply[req.ID] = reply
		} else {
			d.hlReply[req.ID] = "ERROR: " + runErr.Error()
		}
		d.hlMu.Unlock()

		if runErr == nil {
			// Persist the (possibly newly discovered) session id + started flag.
			d.store.SetHeadlessSession(req.ID, newSessionID) //nolint:errcheck
		}
	}()

	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "started": req.ID})
}

func (d *Daemon) handleThreadHeadlessReply(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "headless-reply: id is required")
		return
	}
	d.hlMu.Lock()
	inFlight := d.hlInFlight[id]
	reply, have := d.hlReply[id]
	d.hlMu.Unlock()
	writeJSON(w, http.StatusOK, api.HeadlessReplyResponse{
		Schema:    api.SchemaVersion,
		ID:        id,
		Working:   inFlight,
		HaveReply: have,
		Reply:     reply,
	})
}

// headlessActivity reports a headless thread's activity from the in-flight
// registry: working while a turn process runs, waiting otherwise.
// turnInFlight reports whether a headless turn process is currently running for
// the thread (the busy axis for headless threads).
func (d *Daemon) turnInFlight(id string) bool {
	d.hlMu.Lock()
	defer d.hlMu.Unlock()
	return d.hlInFlight[id]
}
