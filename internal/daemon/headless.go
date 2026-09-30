package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/procs"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesHeadless(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/send-headless", d.handleThreadSendHeadless)
	mux.HandleFunc("GET /v1/threads/headless-reply", d.handleThreadHeadlessReply)
	mux.HandleFunc("GET /v1/threads/turn-identity", d.handleTurnIdentity)
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
		Model:          req.Model,
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
	if conversationGate(w, thread, "send-headless") {
		return
	}
	// Unified model: a headless turn is valid on any IDLE thread — including one
	// that previously ran headed (its conversation resumes per-turn). A LIVE pane
	// owns the conversation, so a concurrent headless turn would fork it: loud 409.
	if _, found, err := d.tmux.FindPaneByThreadID(req.ID); err == nil && found {
		writeError(w, http.StatusConflict, "thread has a live pane — send into the pane (thread send), or stop it first")
		return
	}

	// Expand @blob(…) references before the turn; an unknown blob is a loud 400 here
	// (before claiming the in-flight slot), never a dangling token sent to the agent.
	text, err := d.expandPrompt(req.Text)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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

	turnMode, err := d.resolveSpawnMode(agents.Kind(thread.AgentKind), req.Mode)
	if err != nil {
		d.hlMu.Lock()
		delete(d.hlInFlight, req.ID)
		d.hlMu.Unlock()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	codexHome := ""
	if thread.AgentKind == string(agents.Codex) {
		codexHome, _ = agents.CodexHome(d.cfg.CodexHome)
		// `codex exec` does not start the shared daemon itself, but it must not find
		// one a hand-started codex left enabled either (sesh#15).
		if err := d.ensureCodexNoDaemon(codexHome, "headless turn"); err != nil {
			d.hlMu.Lock()
			delete(d.hlInFlight, req.ID)
			d.hlMu.Unlock()
			writeError(w, http.StatusInternalServerError, "send-headless: "+err.Error())
			return
		}
	}

	// A HEADED-BORN codex thread began its conversation in a pane, but codex mints
	// its session id itself — recover it from the rollouts (the legacy fallback,
	// same as revive; since schema 46 the notify reporter stamps it at each turn
	// end) so the turn CONTINUES that conversation instead of silently starting a
	// fresh one. No rollout = no turn ever ran in the pane = nothing to continue:
	// loud N/A. Sessions claimed by OTHER threads are never discovered.
	sessionID, started := thread.AgentSessionID, thread.HeadlessStarted
	if thread.AgentKind == string(agents.Codex) && started && sessionID == "" {
		releaseSlot := func() {
			d.hlMu.Lock()
			delete(d.hlInFlight, req.ID)
			d.hlMu.Unlock()
		}
		claimed, err := d.claimedAgentSessions(req.ID)
		if err != nil {
			releaseSlot()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		discovered, found, err := agents.DiscoverCodexSession(codexHome, thread.Cwd, thread.CreatedAtUnix, claimed)
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

	// A claude conversation that compacted between turns lives in a NEW <id>.jsonl;
	// --resume the LEAF of the compaction chain so this turn continues the live
	// conversation instead of the stale pre-compaction one. (codex/pi don't drift.)
	if thread.AgentKind == string(agents.Claude) && started && sessionID != "" {
		homes := agents.ResolveHomes(d.cfg.CodexHome)
		resolved, rerr := agents.ResolveCurrentSession(agents.Claude, sessionID, thread.Cwd, homes)
		if rerr != nil {
			d.hlMu.Lock()
			delete(d.hlInFlight, req.ID)
			d.hlMu.Unlock()
			writeError(w, http.StatusInternalServerError, "send-headless: resolve current session: "+rerr.Error())
			return
		}
		sessionID = resolved
	}

	// The per-turn model override (send-headless --model) wins for THIS turn only;
	// otherwise the thread's pinned model applies (both '' = the agent default).
	turnModel := req.Model
	if turnModel == "" {
		turnModel = thread.Model
	}

	go func() {
		reply, newSessionID, runErr := agents.HeadlessTurn(agents.TurnSpec{
			Kind: agents.Kind(thread.AgentKind), ThreadID: req.ID, SessionID: sessionID,
			Cwd: thread.Cwd, Started: started, Prompt: text, CodexHome: codexHome,
			Mode: turnMode, Model: turnModel, SeshBin: seshBinPath(),
			// The turn's root pid is this daemon's evidence that a process asking
			// "who am I?" really is the worker it launched — the only verified
			// identity a pane-less turn can have. Registered under the same lock as
			// hlInFlight and dropped with it below, so it is live for exactly as
			// long as the turn is.
			OnStart: func(pid int) {
				d.hlMu.Lock()
				d.turnPID[req.ID] = pid
				d.hlMu.Unlock()
			},
		})

		d.hlMu.Lock()
		// ONE critical section for both, deliberately: the in-flight flag and the
		// turn's root pid are the same fact with the same lifetime, and an
		// identity that outlived its turn would be the stale-id bug in a new
		// costume. Keep these two lines adjacent — a separate cleanup elsewhere is
		// exactly how they would drift apart.
		delete(d.hlInFlight, req.ID)
		delete(d.turnPID, req.ID)
		if runErr == nil {
			d.hlReply[req.ID] = reply
		} else {
			d.hlReply[req.ID] = "ERROR: " + runErr.Error()
		}
		d.hlMu.Unlock()

		if runErr == nil {
			// Persist the (possibly newly discovered) session id + started flag.
			d.store.SetHeadlessSession(req.ID, newSessionID) //nolint:errcheck
			// Deterministic subscription delivery: the daemon RAN this turn, so
			// it knows precisely when it completed — trigger delivery directly
			// instead of waiting for the eventer to (maybe) catch the busy→idle
			// edge between its polling ticks. The Tracker dedups on the reply
			// count, so the eventer path firing too is harmless. (Pane turns,
			// which the daemon does NOT run, still rely on the eventer.)
			if th, gerr := d.store.GetThread(req.ID); gerr == nil {
				if sn, ok := d.maint.stateOf(req.ID); ok {
					d.deliverSubscriptions(sn)
				} else {
					d.deliverSubscriptions(api.ThreadSnapshot{Thread: th, Head: api.Headless, Busy: api.BusyIdle})
				}
			}
			// A scheduled spawn's headless run ends here, deterministically.
			if d.sched != nil {
				d.sched.onHeadlessDone(req.ID)
			}
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

// handleTurnIdentity answers GET /v1/threads/turn-identity?pid=N: is that
// process running inside a headless turn THIS daemon launched, and if so for
// which thread? See api.TurnIdentityResponse for why the question exists and
// internal/procs for why the answer is an ancestry walk rather than a token.
//
// Always 200 on a well-formed pid: "you are not a turn" is an answer (the caller
// is a plain shell, a pane agent, or a detached job), not a failure.
func (d *Daemon) handleTurnIdentity(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("pid")))
	if err != nil || pid <= 0 {
		writeError(w, http.StatusBadRequest, "turn-identity: pid must be a positive integer")
		return
	}
	id, turnPID, reason := d.turnOwnerOf(pid)
	writeJSON(w, http.StatusOK, api.TurnIdentityResponse{
		Schema: api.SchemaVersion, ID: id, Machine: d.cfg.Machine, TurnPID: turnPID, Reason: reason,
	})
}

// turnOwnerOf resolves the thread whose IN-FLIGHT turn owns the process tree
// containing pid. It returns ("", 0, why) for every kind of no.
//
// THE RULES, each load-bearing:
//   - only turns that are in flight RIGHT NOW are considered, because turnPID is
//     dropped when the turn ends. An identity that outlived its turn would be the
//     stale-$SESH_THREAD_ID bug with extra steps.
//   - an unreadable process tree is a REFUSAL carrying the read error, never a
//     quiet "not a descendant": the difference between "provably not yours" and
//     "could not tell" is the whole point of the gate this feeds.
//   - two in-flight turns both claiming the pid is impossible by construction (a
//     turn's tree cannot contain another turn's root — the daemon starts each one
//     itself, as a child of itself) and is therefore refused rather than guessed,
//     loudly naming both, because if it ever happens the assumption is wrong.
func (d *Daemon) turnOwnerOf(pid int) (threadID string, turnPID int, reason string) {
	d.hlMu.Lock()
	live := make(map[string]int, len(d.turnPID))
	for id, p := range d.turnPID {
		live[id] = p
	}
	d.hlMu.Unlock()

	if len(live) == 0 {
		return "", 0, "no headless turn is in flight on this machine"
	}
	var hits []string
	var hitPID int
	var walkErr error
	for id, root := range live {
		ok, err := procs.IsAncestor(root, pid)
		if err != nil {
			walkErr = err
			continue
		}
		if ok {
			hits = append(hits, id)
			hitPID = root
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], hitPID, ""
	case 0:
		if walkErr != nil {
			return "", 0, fmt.Sprintf("could not read the process tree above pid %d: %v", pid, walkErr)
		}
		return "", 0, fmt.Sprintf("pid %d is not inside any of the %d turn(s) in flight on this machine", pid, len(live))
	default:
		sort.Strings(hits)
		return "", 0, fmt.Sprintf("pid %d resolves to %d in-flight turns (%s) — refusing to guess",
			pid, len(hits), strings.Join(hits, ", "))
	}
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
