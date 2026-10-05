package daemon

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/agents/claude"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesResume(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/resume", d.handleThreadResume)
}

// prepAgentEnv runs the per-agent groundwork every HEADED launch needs (new,
// into-pane, resume) so the agent comes up at a clean input prompt:
//   - claude: pre-trust the cwd in claude's global config (its workspace-trust
//     dialog otherwise eats the first keystrokes — ticket 4b069b88; see
//     claude.EnsureTrust for why --dangerously-skip-permissions is not enough);
//   - claude: also pin CLAUDE_CODE_DISABLE_AGENT_VIEW=1 (see claudeAgentViewEnv);
//   - codex: pre-trust the cwd (same prompt class), wire the turn-end notify
//     reporter, and inject CODEX_HOME.
//
// Headless turns (`--print`/`exec`) are non-interactive and never show either
// dialog, so they do not come through here. pi has no trust prompt.
func (d *Daemon) prepAgentEnv(kind agents.Kind, env map[string]string, cwd string) error {
	switch kind {
	case agents.Claude:
		cfgPath, err := claude.GlobalConfigPath()
		if err != nil {
			return err
		}
		if err := claude.EnsureTrust(cfgPath, cwd); err != nil {
			return err
		}
		pinClaudeAgentView(env)
		return nil
	case agents.Codex:
		return d.prepCodexEnv(env, cwd)
	}
	return nil
}

// prepCodexEnv pre-trusts the cwd (so codex's directory-trust prompt does not eat
// input), wires the notify reporter, and injects CODEX_HOME, for any codex spawn
// (new or resume).
func (d *Daemon) prepCodexEnv(env map[string]string, cwd string) error {
	codexHome, err := agents.CodexHome(d.cfg.CodexHome)
	if err != nil {
		return err
	}
	if err := agents.EnsureCodexTrust(codexHome, cwd); err != nil {
		return err
	}
	// codex's shared app-server daemon breaks stop/resume/adopt (sesh#15): pin it
	// off before every headed launch. Fatal for this launch — a codex started with
	// the daemon would hold its writer lock past the pane.
	if err := d.ensureCodexNoDaemon(codexHome, "spawn"); err != nil {
		return err
	}
	// Wire the turn-end reporter (the flagged system, schema 44): materialize
	// the embedded script and reference it from the codex config's notify key
	// (left alone if the user already has one — see EnsureCodexNotify).
	script, err := agents.WriteCodexNotifyScript(d.cfg.Home)
	if err != nil {
		return err
	}
	if err := agents.EnsureCodexNotify(codexHome, script); err != nil {
		return err
	}
	if d.cfg.CodexHome != "" {
		env["CODEX_HOME"] = d.cfg.CodexHome
	}
	return nil
}

// claimedAgentSessions returns every agent session id already recorded on a
// thread OTHER than excludeThreadID (archived included — an archived thread
// still owns its conversation). Fed to DiscoverCodexSession so the legacy
// cwd+time fallback can never "discover" a sibling's conversation.
func (d *Daemon) claimedAgentSessions(excludeThreadID string) (map[string]bool, error) {
	threads, err := d.store.ListThreads(true)
	if err != nil {
		return nil, err
	}
	claimed := map[string]bool{}
	for _, th := range threads {
		if th.ID != excludeThreadID && th.AgentSessionID != "" {
			claimed[th.AgentSessionID] = true
		}
	}
	return claimed, nil
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
	d.reviveThread(w, r, req.ID, req.Force)
}

// reviveThread is the single revive-into-a-pane implementation behind BOTH
// `thread resume` and `thread headful`. Valid only on an IDLE thread:
//   - a headless turn in flight => 409 (a pane spawned mid-turn would fork the
//     conversation). The in-flight slot is RESERVED for the duration so a turn
//     cannot start underneath the revival.
//   - a live pane already bearing the marker => 409 (already live).
//   - a codex thread with no minted session id (no turn ever) => explicit N/A.
func (d *Daemon) reviveThread(w http.ResponseWriter, r *http.Request, id string, force bool) {
	// cleared records the background session --force released, so the response can
	// report it: a client that ASKED to force and gets no such field back is talking
	// to a daemon that predates the flag, and must say so rather than assume it worked.
	cleared := ""
	thread, err := d.store.GetThread(id)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, "thread not found: "+id)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runtimeGate(w, thread, "revive") {
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

	// SHELL THREAD: its runtime is a tmux SESSION, not an agent pane, so reviving
	// it means recreating that session in the thread's recorded cwd — the exact
	// mirror of resuming an agent's conversation. This is why the TUI needs no
	// special case for Enter on a shell thread.
	if thread.AgentKind == api.ShellAgentKind {
		if _, live, err := d.tmux.FindSessionByShellID(id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		} else if live {
			writeError(w, http.StatusConflict, "revive: shell thread is already live")
			return
		}
		if err := d.startShellSession(thread); err != nil {
			writeError(w, http.StatusInternalServerError, "revive: "+err.Error())
			return
		}
		revived, err := d.store.GetThread(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: revived})
		return
	}

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
	// Legacy fallback (pre-46 threads only — the codex notify reporter stamps
	// the id at each turn end now): recover the late-minted id from rollouts,
	// never one another thread already claims; none (no turn ever) => N/A.
	if kind == agents.Codex && sessionID == "" {
		codexHome, err := agents.CodexHome(d.cfg.CodexHome)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		claimed, err := d.claimedAgentSessions(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		discovered, found, err := agents.DiscoverCodexSession(codexHome, thread.Cwd, thread.CreatedAtUnix, claimed)
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

	// claude's BACKGROUND SESSIONS (sesh#16). The leaf we are about to resume may be
	// OWNED by a background session — in production, because ← ← on an empty claude
	// prompt opened claude's agents view, which moves the conversation there. sesh's
	// panes run with that view disabled (claudeAgentViewEnv), and with it disabled
	// claude REFUSES to `--resume` a held conversation (measured: without the switch a
	// same-build holder made claude silently re-exec as `claude attach`, a pane that
	// only looked revived). The pane dies a beat after spawning, which
	// confirmAgentLaunched catches; the post-spawn re-check below names the holder.
	//
	// sesh does NOT pre-refuse: claude is the authority on whether a hold blocks, and
	// uncertainty never blocks a revive. What sesh adds is (a) --force, an explicit way
	// to stop the holder and get a real resume, and (b) naming the holder when the
	// revive does fail (below, after confirmAgentLaunched).
	if force {
		bg, held, herr := d.heldBackgroundSession(r.Context(), kind, sessionID)
		switch {
		case herr != nil:
			// --force was asked for explicitly, so failing to even check is an error
			// rather than something to shrug past: the caller would otherwise be told a
			// hold was cleared when sesh never looked.
			writeError(w, http.StatusConflict, fmt.Sprintf(
				"revive --force: could not read claude's background-session registry, so the hold could not be released: %v", herr))
			return
		case held:
			if cerr := d.clearHeldBackgroundSession(r.Context(), bg); cerr != nil {
				writeError(w, http.StatusConflict, fmt.Sprintf(
					"revive --force: could not release background session %s holding this conversation: %v", bg.ID, cerr))
				return
			}
			log.Printf("revive %s: --force stopped background session %s (%q) holding leaf %s", id, bg.ID, bg.Name, sessionID)
			cleared = bg.ID
		}
	}

	env := d.spawnEnv(id)
	if err := d.prepAgentEnv(kind, env, thread.Cwd); err != nil {
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
		msg := "revive: " + err.Error()
		// The pre-flight above can legitimately miss a hold: the registry may have
		// been unreadable, or the session may have been backgrounded in the moment
		// between the check and the spawn. Re-check now, so the failure still names
		// the holder and the remedy instead of leaving claude's 409 buried in the
		// captured pane output.
		if bg, held, herr := d.heldBackgroundSession(r.Context(), kind, sessionID); herr == nil && held {
			msg = heldSessionRefusal(id, bg, sessionID)
		}
		writeError(w, http.StatusConflict, msg)
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
	writeJSON(w, http.StatusOK, api.ReviveThreadResponse{
		Schema: api.SchemaVersion, Thread: thread, ClearedBackgroundSession: cleared,
	})
}

// claudeAgentViewEnv is claude's own switch for its AGENT VIEW (sesh#16). Documented
// in claude's settings schema as "Disable agent view (`claude agents`, `--bg`,
// /background, the on-demand daemon). Equivalent to CLAUDE_CODE_DISABLE_AGENT_VIEW=1."
//
// WHY sesh pins it in every claude pane it launches. Pressing ← twice on an empty
// claude prompt opens that view, and opening it MOVES THE CONVERSATION INTO A CLAUDE
// BACKGROUND SESSION (it writes a `continued-in` record, the daemon logs the source as
// `(slash)` — meaning "from the REPL" — and mid-tool it prints "Backgrounding after
// the current tool finishes…"). sesh's record then points at a conversation it no
// longer owns, and the thread cannot be revived until the hold is released. Two of
// Lukas's threads were stranded that way for 5 and 10 days, with nothing typed — ←
// means "fold" in the sesh TUI and "go to the sidebar" in the cockpit, so it is an
// easy slip with focus in the agent pane. Both production signatures reproduced from
// ← ← alone; with this variable set, ← ← does nothing.
//
// Scoped to sesh-launched panes on purpose (Lukas's choice): it removes the agents
// view, `--bg` and /background ONLY where sesh owns the lifecycle. Subagents and
// `run_in_background` shells are NOT affected — measured with the variable set.
// A user-set value is overridden: inside a sesh pane the feature is exactly what
// strands the thread, so there is no setting of it that sesh can work with.
const claudeAgentViewEnv = "CLAUDE_CODE_DISABLE_AGENT_VIEW"

func pinClaudeAgentView(env map[string]string) {
	env[claudeAgentViewEnv] = "1"
}
