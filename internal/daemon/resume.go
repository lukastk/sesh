package daemon

import (
	"errors"
	"net/http"
	"strings"

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

// handleThreadResume revives an IDLE thread into a pane (the unified model:
// "dead" and "headless" are not modes — an idle thread is a durable conversation,
// and this is its pane-shaped revival; `headful` is the same operation). It
// recreates the tmux session and relaunches the agent with --resume so the
// conversation continues. A codex thread that has never had a turn has no captured
// session id and legitimately cannot be revived — an explicit error (N/A), never faked.
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
	d.reviveThread(w, req.ID)
}

// reviveThread is the single revive-into-a-pane implementation behind BOTH
// `thread resume` and `thread headful`. Valid only on an IDLE thread:
//   - a headless turn in flight => 409 (a pane spawned mid-turn would fork the
//     conversation). The in-flight slot is RESERVED for the duration so a turn
//     cannot start underneath the revival.
//   - a live pane already bearing the marker => 409 (already live).
//   - a codex thread with no minted session id (no turn ever) => explicit N/A.
func (d *Daemon) reviveThread(w http.ResponseWriter, id string) {
	thread, err := d.store.GetThread(id)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, "thread not found: "+id)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if nonAgentGate(w, thread, "revive") {
		return
	}

	d.hlMu.Lock()
	if d.hlInFlight[id] {
		d.hlMu.Unlock()
		writeError(w, http.StatusConflict, "revive: a turn is in flight for this thread — try again when it is idle")
		return
	}
	d.hlInFlight[id] = true
	d.hlMu.Unlock()
	defer func() {
		d.hlMu.Lock()
		delete(d.hlInFlight, id)
		d.hlMu.Unlock()
	}()

	if _, found, err := d.tmux.FindPaneByThreadID(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if found {
		writeError(w, http.StatusConflict, "revive: thread is already live")
		return
	}

	// The session to revive into. A never-paned thread carries the logical
	// "headless-<id>" placeholder (see newHeadlessThread) — mint its real name;
	// a previously-paned thread keeps its own.
	session := thread.SessionName
	if strings.HasPrefix(session, "headless-") {
		var err error
		if session, err = d.sessionNameFor(thread.Cwd, thread.ID, thread.Name); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	kind := agents.Kind(thread.AgentKind)
	sessionID := thread.AgentSessionID
	// codex mints its session id on the first turn; recover it from rollouts, and if
	// there is none (no turn ever) it legitimately cannot be revived => N/A.
	if kind == agents.Codex && sessionID == "" {
		codexHome, err := agents.CodexHome(d.cfg.CodexHome)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		discovered, found, err := agents.DiscoverCodexSession(codexHome, thread.Cwd, thread.CreatedAtUnix)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found {
			writeError(w, http.StatusUnprocessableEntity,
				"revive: codex thread has no session id (no turn has ever run) — nothing to resume (N/A)")
			return
		}
		sessionID = discovered
		d.store.SetThreadAgentSession(id, sessionID) //nolint:errcheck
	}
	if sessionID == "" {
		writeError(w, http.StatusUnprocessableEntity, "revive: thread has no captured agent session id")
		return
	}
	// claude's session id drifts across compaction (a new <id>.jsonl each time); resume
	// the LEAF of that chain, not the stale stored anchor, or we'd revive a pre-compaction
	// session and lose the post-compaction history. Resolve-on-read: the record keeps the
	// stable anchor; ResolveCurrentSession follows the chain forward to the live session.
	if kind == agents.Claude {
		homes := agents.ResolveHomes(d.cfg.CodexHome)
		resolved, rerr := agents.ResolveCurrentSession(kind, sessionID, thread.Cwd, homes)
		if rerr != nil {
			writeError(w, http.StatusInternalServerError, "revive: resolve current session: "+rerr.Error())
			return
		}
		sessionID = resolved
	}

	env := map[string]string{agents.EnvThreadID: id}
	if err := d.prepCodexEnv(kind, env, thread.Cwd); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	mode, merr := d.resolveSpawnMode(kind, "")
	if merr != nil {
		writeError(w, http.StatusBadRequest, merr.Error())
		return
	}
	resumeCmd := agents.ResumeCommand(kind, sessionID, thread.Model, mode, d.spawn.ArgsFor(string(kind)))

	// If the session still exists, a SIBLING thread is keeping it alive — revive
	// into a new WINDOW of it rather than colliding on the name (multiple threads
	// per session). Otherwise mint the session fresh. teardown undoes exactly what
	// we created (kill the whole session only if WE made it; else just our pane,
	// so siblings survive).
	var pane string
	createdSession := false
	if d.tmux.HasSession(session) {
		var werr error
		if pane, werr = d.tmux.CreateWindowCmd(session, thread.Cwd, env, resumeCmd); werr != nil {
			writeError(w, http.StatusInternalServerError, werr.Error())
			return
		}
	} else {
		if err := d.tmux.CreateSessionCmd(session, thread.Cwd, env, resumeCmd); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		createdSession = true
		var perr error
		if pane, perr = d.tmux.SessionFirstPane(session); perr != nil {
			d.tmux.KillSession(session) //nolint:errcheck
			writeError(w, http.StatusInternalServerError, perr.Error())
			return
		}
	}
	teardown := func() {
		if createdSession {
			d.tmux.KillSession(session) //nolint:errcheck
		} else if pane != "" {
			d.tmux.KillPane(pane) //nolint:errcheck
		}
	}
	if err := d.tmux.SetPaneThreadID(pane, id); err != nil {
		teardown()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Confirm the agent actually launched and did not exit immediately — e.g. `claude
	// --resume` refusing a session another live process holds. Without this a revive that
	// self-destructed a beat after spawning still returned success and left the thread
	// silently un-enterable. Tear down what we created and report the reason LOUDLY.
	if err := d.confirmAgentLaunched(id); err != nil {
		teardown()
		writeError(w, http.StatusConflict, "revive: "+err.Error())
		return
	}

	// Persist the (possibly newly minted) session name; reviving also marks the
	// conversation begun. On failure tear down exactly what we created — store
	// and runtime stay consistent, siblings untouched.
	if err := d.store.SetThreadHeaded(id, session); err != nil {
		teardown()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	thread, _ = d.store.GetThread(id)
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}
