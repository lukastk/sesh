package daemon

import (
	"errors"
	"net/http"
	"os"
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
	if req.Parent != "" {
		if _, err := d.store.GetThread(req.Parent); err != nil {
			writeError(w, http.StatusBadRequest, "thread: parent "+req.Parent+" does not exist")
			return
		}
	}
	if req.Headless {
		d.newHeadlessThread(w, kind, req)
		return
	}

	id := uuid.NewString()
	session, err := d.sessionNameFor(req.Cwd, id, req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if d.tmux.HasSession(session) {
		writeError(w, http.StatusConflict, "thread: tmux session "+session+" already exists")
		return
	}

	env := map[string]string{agents.EnvThreadID: id}
	if err := d.prepCodexEnv(kind, env, req.Cwd); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Capture the agent session id at spawn for pi/claude (what resume needs);
	// codex mints its own on the first turn and is discovered later.
	agentSessionID := ""
	if kind == agents.Pi || kind == agents.Claude {
		agentSessionID = uuid.NewString()
	}

	if err := d.tmux.CreateSessionCmd(session, req.Cwd, env, agents.HeadedCommand(kind, agentSessionID)); err != nil {
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
		ID:             id,
		Machine:        d.cfg.Machine,
		SessionName:    session,
		Cwd:            req.Cwd,
		AgentKind:      string(kind),
		Name:           req.Name,
		Tags:           []string{},
		CreatedAtUnix:  time.Now().Unix(),
		AgentSessionID: agentSessionID,
		Parent:         req.Parent,
		// A headed spawn BEGINS the conversation (the agent launches with this
		// session id) — so a later headless turn on the idle thread must RESUME,
		// not create. See api.Thread.HeadlessStarted.
		HeadlessStarted: true,
	}
	if err := d.store.InsertThread(thread); err != nil {
		d.tmux.KillSession(session) // keep store and runtime consistent
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}

func (d *Daemon) handleThreadList(w http.ResponseWriter, r *http.Request) {
	includeArchived := r.URL.Query().Get("archived") == "1"
	threads, err := d.store.ListThreads(includeArchived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := api.ThreadListResponse{Schema: api.SchemaVersion}
	if r.URL.Query().Get("all-machines") == "1" {
		// Mesh fan-out: this machine's threads + every reachable peer's.
		threads, resp.Unreachable = d.fanOutThreads(threads, includeArchived)
	}
	if threads == nil {
		threads = []api.Thread{}
	}
	resp.Threads = threads
	writeJSON(w, http.StatusOK, resp)
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

// The content-diff probe samples a pane repeatedly and declares working when
// change is SUSTAINED (>= activityChangedNeeded intervals), not a one-off. It
// early-exits the instant it has enough changes, so detecting a busy agent is
// fast; only confirming IDLE runs the full window. The window is long enough to
// catch the slowest working animation (codex's ~1s "thinking" timer), while
// requiring >1 change still rejects a single idle blip (a rotating hint, an MCP
// server finishing startup). When settled, all three agent TUIs are byte-stable.
const (
	activityMaxSamples    = 10                     // up to ~3s of confirming idle
	activityInterval      = 300 * time.Millisecond //
	activityChangedNeeded = 2                      // changed intervals => working
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
		// No pane: headless; busy iff a turn is in flight. Necessarily detached.
		resp.Head = api.Headless
		resp.Busy = api.BusyIdle
		if d.turnInFlight(id) {
			resp.Busy = api.BusyBusy
		}
		resp.Attachment = api.Detached
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Head = api.Headful
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
		// A marked pane but no live agent of the right kind: no runtime —
		// headless·idle (revivable).
		resp.Head = api.Headless
		resp.Busy = api.BusyIdle
		writeJSON(w, http.StatusOK, resp)
		return
	}
	working, err := d.paneChanging(loc.Pane)
	if err != nil {
		// The pane vanished mid-probe (e.g. the session was killed concurrently).
		// An unreachable pane is no runtime, not a server error.
		resp.Head = api.Headless
		resp.Busy = api.BusyIdle
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Busy = api.BusyIdle
	if working {
		resp.Busy = api.BusyBusy
	}
	writeJSON(w, http.StatusOK, resp)
}

// resolveState computes the two runtime axes for a thread — the signals ticket
// needs-input/needs-revival depend on. Shared with the status endpoint's logic.
func (d *Daemon) resolveState(thread api.Thread) (api.Head, api.Busy, error) {
	loc, found, err := d.tmux.FindPaneByThreadID(thread.ID)
	if err != nil {
		return "", "", err
	}
	if !found {
		if d.turnInFlight(thread.ID) {
			return api.Headless, api.BusyBusy, nil
		}
		return api.Headless, api.BusyIdle, nil
	}
	agent, running := tmux.AgentUnderPane(loc.PanePID)
	if !(running && agent.Kind == thread.AgentKind) {
		return api.Headless, api.BusyIdle, nil
	}
	working, err := d.paneChanging(loc.Pane)
	if err != nil {
		return api.Headless, api.BusyIdle, nil // pane vanished mid-probe => no runtime
	}
	if working {
		return api.Headful, api.BusyBusy, nil
	}
	return api.Headful, api.BusyIdle, nil
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
	for i := 0; i < activityMaxSamples; i++ {
		time.Sleep(activityInterval)
		cur, err := d.tmux.CapturePane(pane)
		if err != nil {
			return false, err
		}
		if cur != prev {
			changed++
			if changed >= activityChangedNeeded {
				return true, nil // early exit: a busy agent is detected fast
			}
		}
		prev = cur
	}
	return false, nil
}

// sanitizeName maps a thread name to a tmux-safe session suffix. tmux session
// names may not contain ":" or "." and we avoid whitespace; everything else
// becomes "_".
// sessionNameFor resolves a thread's tmux session name: the user's
// [[session_name]] config policy when a rule matches the cwd, else the default
// sesh_<sanitized-name> convention. Policy errors (bad placeholder, empty
// expansion) are loud — never a silently wrong name.
func (d *Daemon) sessionNameFor(cwd, threadID, threadName string) (string, error) {
	home, _ := os.UserHomeDir()
	if name, matched, err := d.naming.SessionNameFor(cwd, threadID, threadName, home); err != nil {
		return "", err
	} else if matched {
		return name, nil
	}
	return "sesh_" + sanitizeName(threadName), nil
}

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
