package daemon

// VIRTUAL threads: a thread record with agent_kind "virtual" is a pure grouping
// node — no agent, no pane, no transcript — so threads can be parented under
// something that is not (yet) a real thread. All record machinery (tree,
// reparent, hold inheritance, tags, archive, mesh sync) applies unchanged; the
// maintainer resolves it headless·idle for free (no pane to probe). Agent-shaped
// verbs refuse loudly via nonAgentGate. `thread realize` converts a virtual
// thread IN PLACE into a real one: the result is exactly a fresh never-started
// headless thread (see newHeadlessThread) — id, children, tags, holds and ticket
// bindings all survive because nothing but the record changes.

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

func (d *Daemon) routesRealize(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/realize", d.handleThreadRealize)
}

// The two gates below are the taxonomy split (api.HasConversation /
// api.HasRuntime). Which one a verb uses is a real decision, not a style choice:
// a SHELL thread has a runtime but no conversation, so it must pass the runtime
// gate (enter, send, capture, revive — reviving one creates its session) and be
// refused by the conversation gate (fork, transcript, send-headless, --model).
// Keying a verb on the wrong gate is exactly the plausible-but-wrong failure
// this project exists to prevent. See _dev/SHELL.md.

// nodeRefusal is the loud, actionable 409 body for a gate, tailored to the kind
// (the remedy differs: realize a virtual thread, delete a divider, and a shell
// thread simply has no conversation to operate on).
func nodeRefusal(thread api.Thread, verb string) string {
	name := thread.Name
	if name == "" {
		name = thread.ID
	}
	switch thread.AgentKind {
	case api.DividerAgentKind:
		return verb + ": " + name + " is a divider (a visual separator — no conversation); delete it with sesh thread delete --id " + thread.ID
	case api.ShellAgentKind:
		return verb + ": " + name + " is a shell thread (a tracked tmux session — it has no agent conversation, so there is nothing to " + verb + "); enter it with sesh tmux nav, or start an agent in it"
	default:
		return verb + ": " + name + " is a virtual thread (a grouping node — no agent); convert it first: sesh thread realize --id " + thread.ID + " --agent claude|codex|pi"
	}
}

// conversationGate refuses a CONVERSATION-shaped operation (fork, transcript,
// send-headless, a model pin) on a node that has none — virtual, divider, or
// shell — and reports whether it fired.
func conversationGate(w http.ResponseWriter, thread api.Thread, verb string) bool {
	if api.HasConversation(thread.AgentKind) {
		return false
	}
	writeError(w, http.StatusConflict, nodeRefusal(thread, verb))
	return true
}

// runtimeGate refuses a RUNTIME-shaped operation (enter/nav, send, capture,
// revive) on a node that can never have one — virtual or divider. A SHELL thread
// passes: its runtime is a tmux session.
func runtimeGate(w http.ResponseWriter, thread api.Thread, verb string) bool {
	if api.HasRuntime(thread.AgentKind) {
		return false
	}
	writeError(w, http.StatusConflict, nodeRefusal(thread, verb))
	return true
}

// newVirtualThread records a VIRTUAL thread (see the package comment above).
// Reached from handleThreadNew BEFORE agent parsing: a virtual thread has no
// agent, so every agent-shaped request field is a loud refusal, and cwd is
// OPTIONAL (kept, if given, as the default for a later realize).
func (d *Daemon) newVirtualThread(w http.ResponseWriter, req api.NewThreadRequest) {
	switch {
	case req.Agent != "":
		writeError(w, http.StatusBadRequest, "thread: --virtual takes no --agent (a virtual thread has none; realize it later)")
		return
	case req.Headless:
		writeError(w, http.StatusBadRequest, "thread: --virtual and --headless are mutually exclusive (a virtual thread has no agent to run)")
		return
	case req.ForkFrom != "":
		writeError(w, http.StatusBadRequest, "thread: --virtual cannot fork (no conversation to branch)")
		return
	case req.IntoSession != "" || req.IntoWindow != "" || req.IntoPane != "":
		writeError(w, http.StatusBadRequest, "thread: --virtual takes no placement (nothing is spawned)")
		return
	case req.Msg != "":
		writeError(w, http.StatusBadRequest, "thread: --virtual takes no --msg (no agent to receive it)")
		return
	case req.Mode != "":
		writeError(w, http.StatusBadRequest, "thread: --virtual takes no spawn mode (nothing is spawned)")
		return
	case req.Model != "":
		writeError(w, http.StatusBadRequest, "thread: --virtual takes no --model (set one at realize time)")
		return
	}
	if req.Cwd != "" && !strings.HasPrefix(req.Cwd, "/") {
		writeError(w, http.StatusBadRequest, "thread: cwd must be an absolute path")
		return
	}
	if req.Parent != "" {
		if _, err := d.store.GetThread(req.Parent); err != nil {
			writeError(w, http.StatusBadRequest, "thread: parent "+req.Parent+" does not exist")
			return
		}
	}
	id := uuid.NewString()
	thread := api.Thread{
		ID:            id,
		Machine:       d.cfg.Machine,
		SessionName:   "virtual-" + id, // logical name; no tmux session ever exists
		Cwd:           req.Cwd,
		AgentKind:     api.VirtualAgentKind,
		Name:          req.Name,
		Tags:          []string{},
		CreatedAtUnix: time.Now().Unix(),
		Parent:        req.Parent,
		Notify:        d.defaults.NotifyDefault(),
	}
	if err := d.store.InsertThread(thread); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}

// handleThreadRealize converts a VIRTUAL thread in place into a real,
// never-started headless one. Mirrors newHeadlessThread's bookkeeping: the
// session id is pre-minted for pi/claude (codex mints its own on the first
// turn), HeadlessStarted stays false (the first turn creates the conversation),
// and the session name becomes "headless-<id>" so a later headful revival mints
// a real session name. The store's guarded UPDATE (WHERE agent_kind = virtual)
// makes a concurrent double-realize lose loudly instead of double-converting.
func (d *Daemon) handleThreadRealize(w http.ResponseWriter, r *http.Request) {
	var req api.RealizeThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "realize: id is required")
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
	if thread.AgentKind != api.VirtualAgentKind {
		writeError(w, http.StatusConflict,
			"realize: thread is "+thread.AgentKind+", not virtual — realize only converts virtual grouping threads")
		return
	}
	kind, err := agents.ParseKind(req.Agent) // refuses "virtual" too
	if err != nil {
		writeError(w, http.StatusBadRequest, "realize: "+err.Error())
		return
	}
	// A definite cwd is required from here on (agents need one): the request's,
	// else the cwd stored at creation. ~ resolves against THIS (owner) home.
	cwd := expandHomeCwd(req.Cwd)
	if cwd == "" {
		cwd = thread.Cwd
	}
	if cwd == "" || !strings.HasPrefix(cwd, "/") {
		writeError(w, http.StatusBadRequest, "realize: a cwd is required (this virtual thread has none stored) — pass --cwd")
		return
	}
	agentSessionID := ""
	if kind == agents.Pi || kind == agents.Claude {
		agentSessionID = uuid.NewString() // sesh pre-assigns; codex cannot
	}
	if err := d.store.RealizeThread(req.ID, string(kind), agentSessionID, cwd, "headless-"+req.ID, req.Model); err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			// The guarded UPDATE lost: re-read to report what the row is NOW
			// (a concurrent realize converted it first).
			if cur, gerr := d.store.GetThread(req.ID); gerr == nil {
				writeError(w, http.StatusConflict, "realize: thread was concurrently realized to "+cur.AgentKind)
				return
			}
			writeError(w, http.StatusNotFound, "thread not found: "+req.ID)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	d.respondThread(w, req.ID)
}
