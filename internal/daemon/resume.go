package daemon

import (
	"errors"
	"net/http"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesResume(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/resume", d.handleThreadResume)
}

// prepCodexEnv pre-trusts the cwd (so codex's directory-trust prompt does not eat
// input) and injects CODEX_HOME, for any codex spawn (new or resume). No-op for
// other agents.
func (d *Daemon) prepCodexEnv(kind agents.Kind, env map[string]string, cwd string) error {
	if kind != agents.Codex {
		return nil
	}
	codexHome, err := agents.CodexHome(d.cfg.CodexHome)
	if err != nil {
		return err
	}
	if err := agents.EnsureCodexTrust(codexHome, cwd); err != nil {
		return err
	}
	if d.cfg.CodexHome != "" {
		env["CODEX_HOME"] = d.cfg.CodexHome
	}
	return nil
}

// handleThreadResume revives a DEAD headed thread: it recreates the tmux session
// and relaunches the agent with --resume so the conversation continues. A codex
// thread that died before its first turn has no captured session id and legitimately
// cannot be resumed — an explicit error (N/A), never faked.
func (d *Daemon) handleThreadResume(w http.ResponseWriter, r *http.Request) {
	var req api.ThreadResumeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "resume: id is required")
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
	if thread.Headless {
		writeError(w, http.StatusBadRequest, "resume: headless threads resume on the next send; resume is for headed threads")
		return
	}

	// Resume only a DEAD thread (no live pane bearing its marker).
	if _, found, err := d.tmux.FindPaneByThreadID(req.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if found {
		writeError(w, http.StatusConflict, "resume: thread is already live")
		return
	}
	if d.tmux.HasSession(thread.SessionName) {
		writeError(w, http.StatusConflict, "resume: a tmux session "+thread.SessionName+" already exists")
		return
	}

	kind := agents.Kind(thread.AgentKind)
	sessionID := thread.AgentSessionID

	// codex: recover its minted session id from its rollouts if we don't have it.
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
			// N/A: codex thread died before its first turn — nothing to resume.
			writeError(w, http.StatusUnprocessableEntity,
				"resume: codex thread has no session id (it died before its first turn) — nothing to resume (N/A)")
			return
		}
		sessionID = id
		d.store.SetThreadAgentSession(req.ID, sessionID) //nolint:errcheck
	}
	if sessionID == "" {
		writeError(w, http.StatusUnprocessableEntity, "resume: thread has no captured agent session id")
		return
	}

	env := map[string]string{agents.EnvThreadID: req.ID}
	if err := d.prepCodexEnv(kind, env, thread.Cwd); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := d.tmux.CreateSessionCmd(thread.SessionName, thread.Cwd, env, agents.ResumeCommand(kind, sessionID)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pane, err := d.tmux.SessionFirstPane(thread.SessionName)
	if err != nil {
		d.tmux.KillSession(thread.SessionName)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := d.tmux.SetPaneThreadID(pane, req.ID); err != nil {
		d.tmux.KillSession(thread.SessionName)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	thread, _ = d.store.GetThread(req.ID)
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}
