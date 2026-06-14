package daemon

// Adopt (PARITY_ROADMAP D5): bring an agent sesh DIDN'T spawn under
// management. POST /v1/threads/adopt {pane, name}: the pane must live on
// sesh's WORK server (pane ids are per-server; nav/status/send all assume the
// work socket — adopting elsewhere would mint a record every other feature
// mis-handles). Detection is per-agent, v1's exp15 walkers ported: claude
// from argv (--session-id/--resume), pi from its RPC socket (authoritative:
// the socket reports its own pane), codex from the rollout file the process
// holds open (needs a first turn). Every ambiguity is LOUD — adopt never
// guesses an identity.

import (
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents"
	codex "github.com/lukastk/sesh/internal/agents/codexfs"
	"github.com/lukastk/sesh/internal/agents/pi"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/tmux"
)

func (d *Daemon) routesAdopt(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/adopt", d.handleThreadAdopt)
}

var argvSessionIDRe = regexp.MustCompile(`--(?:session-id|resume)[= ]([0-9a-fA-F-]{36})`)

func (d *Daemon) handleThreadAdopt(w http.ResponseWriter, r *http.Request) {
	var req api.AdoptThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Pane == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "adopt: pane and name are required")
		return
	}
	info, found, err := d.tmux.FindPaneByID(req.Pane)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "adopt: pane "+req.Pane+" is not on sesh's work server (adopt is work-server-only: pane ids are per-server and every thread feature assumes the work socket)")
		return
	}
	if info.ThreadID != "" {
		writeError(w, http.StatusConflict, "adopt: pane "+req.Pane+" already belongs to thread "+info.ThreadID)
		return
	}
	agent, running := tmux.AgentUnderPane(info.PanePID)
	if !running {
		writeError(w, http.StatusConflict, "adopt: no coding agent is running in pane "+req.Pane)
		return
	}

	// An EXPLICIT --session-id bypasses per-agent detection: the caller asserts the
	// conversation id (e.g. a claude launched with a bare `-r`, where the id is not
	// in argv). The pane must still hold a live agent (checked above).
	sessionID := req.SessionID
	if sessionID == "" {
		switch agents.Kind(agent.Kind) {
		case agents.Claude:
			m := argvSessionIDRe.FindStringSubmatch(agent.Command)
			if m == nil {
				writeError(w, http.StatusConflict, "adopt: the claude in "+req.Pane+" carries no --session-id/--resume in its argv — its conversation can't be identified (pass --session-id <uuid> explicitly, or relaunch it with --session-id)")
				return
			}
			sessionID = m[1]
		case agents.Pi:
			id, err := pi.ResolveUUIDByPane(pi.DefaultSocketsDir(), info.Pane, d.tmux.SocketPath())
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if id == "" {
				writeError(w, http.StatusConflict, "adopt: no live pi RPC socket reports pane "+req.Pane+" — can't identify its conversation")
				return
			}
			sessionID = id
		case agents.Codex:
			id, found, err := codex.ResolveUUIDByPID(agent.PID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if !found {
				writeError(w, http.StatusConflict, "adopt: the codex in "+req.Pane+" holds no rollout file open (no turn yet) — take a turn, then adopt")
				return
			}
			sessionID = id
		default:
			writeError(w, http.StatusBadRequest, "adopt: unknown agent kind "+agent.Kind)
			return
		}
	}

	id := uuid.NewString()
	thread := api.Thread{
		ID:             id,
		Machine:        d.cfg.Machine,
		SessionName:    info.Session,
		Cwd:            info.Cwd,
		AgentKind:      agent.Kind,
		Name:           req.Name,
		Tags:           []string{},
		CreatedAtUnix:  time.Now().Unix(),
		AgentSessionID: sessionID,
		Notify:         d.defaults.NotifyDefault(),
		// The conversation exists (the agent is live in the pane).
		HeadlessStarted: true,
	}
	// Insert the record BEFORE stamping the pane: a failed insert must never
	// leave a pane stamped with an id that has no row (the partial-state bug).
	// session_name is no longer unique (a pane may join a session that already
	// hosts threads), so the only collision left is the pane already being
	// marked — caught loudly above as a clean 409, never a raw DB error.
	if err := d.store.InsertThread(thread); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := d.tmux.StampPaneThreadID(info.Pane, id); err != nil {
		// Roll the record back so store and runtime stay consistent.
		d.store.DeleteThread(id) //nolint:errcheck
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("adopt: stamp pane: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}
