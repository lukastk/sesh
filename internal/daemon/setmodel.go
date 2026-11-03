package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// Changing a thread's model (schema 58). The REGISTERED model is the record's
// Thread.Model — what spawn, resume and every headless turn pass as --model.
// `thread set-model` changes only that. `thread switch-model` (Apply) also
// switches the live agent, per harness, always session-only — no harness's own
// global default is ever written:
//
//   - claude: there is no session-only way to switch a running claude from
//     outside. A typed `/model <m>` also SAVES <m> as claude's global default
//     (~/.claude/settings.json), and only its interactive picker has a
//     session-only key. So sesh restarts the pane IN PLACE (respawn-pane -k:
//     same pane id, layout and marker) on `claude --resume <session> --model
//     <m>`. The conversation resumes; the claude process — and any turn in
//     flight — does not survive.
//   - codex: `/model <m>` typed into the pane switches for "this session only"
//     (codex says so). It goes through the typing guard and must be confirmed
//     by codex's "Model changed to <m>" line.
//   - pi / mypi: pi-rpc-socket's set_model, which resolves the pattern like
//     --model and never persists a default.
//   - no live agent (headless, idle, or a pane without an agent): nothing to
//     switch; the next spawn, resume or headless turn uses the new model.

// codexModelConfirmWithin bounds the wait for codex to print its confirmation.
const codexModelConfirmWithin = 10 * time.Second

func (d *Daemon) handleThreadModel(w http.ResponseWriter, r *http.Request) {
	var req api.SetThreadModelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch {
	case req.ID == "":
		writeError(w, http.StatusBadRequest, "model: id is required")
		return
	case req.Clear && req.Model != "":
		writeError(w, http.StatusBadRequest, "model: --clear and --model are exclusive")
		return
	case req.Clear && req.Apply:
		writeError(w, http.StatusBadRequest, "model: a live agent cannot be switched to \"no model\"; clear the registration with set-model --clear (it applies at the next spawn or resume)")
		return
	case !req.Clear && strings.TrimSpace(req.Model) == "":
		writeError(w, http.StatusBadRequest, "model: --model is required (or --clear to remove the registered model)")
		return
	case strings.TrimSpace(req.Model) != req.Model:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("model: %q has leading or trailing whitespace", req.Model))
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
	kind, err := agents.ParseKind(thread.AgentKind)
	if err != nil {
		writeError(w, http.StatusBadRequest, "model: thread "+req.ID+" is a "+thread.AgentKind+" thread, which runs no agent model")
		return
	}

	if !req.Apply {
		d.recordModel(w, req, api.ModelRecorded, "registered; the next spawn, resume or headless turn uses it — a live agent keeps its current model (switch-model changes that too)")
		return
	}

	loc, found, err := d.tmux.FindPaneByThreadID(thread.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	live := false
	if found {
		_, live = tmux.AgentUnderPane(loc.PanePID)
	}
	if !live {
		detail := "no live agent to switch; the next spawn, resume or headless turn uses it"
		if d.turnInFlight(thread.ID) {
			detail = "the headless turn in flight finishes on its old model; the next turn uses the new one"
		}
		d.recordModel(w, req, api.ModelNextTurn, detail)
		return
	}
	if snap, ok := d.maint.stateOf(thread.ID); ok && snap.Busy == api.BusyBusy && !req.Force {
		consequence := "it switches mid-turn"
		if kind == agents.Claude {
			consequence = "for claude the pane restart KILLS the turn in flight"
		}
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"model: thread %s is mid-turn; wait for it to finish, or pass --force (%s)", thread.ID, consequence))
		return
	}

	switch {
	case kind == agents.Claude:
		// Record first: the restart reads the new model from the request, and once
		// the old process is killed the record must already say what to resume on.
		if err := d.store.SetThreadModel(thread.ID, req.Model); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := d.restartClaudeOnModel(thread, loc.Pane, req.Model); err != nil {
			writeError(w, http.StatusConflict, "model: registered "+req.Model+", but restarting the claude pane failed: "+err.Error())
			return
		}
		d.respondModel(w, thread.ID, api.ModelRestarted, "the claude pane was restarted in place on "+req.Model+"; its conversation resumed")
	case kind == agents.Codex:
		confirmed, err := d.switchCodexModel(thread, req.Model)
		if err != nil {
			writeError(w, http.StatusConflict, "model: "+err.Error())
			return
		}
		d.recordModel(w, req, api.ModelSwitched, confirmed)
	case agents.Family(kind) == agents.Pi:
		if base, level, ok := strings.Cut(req.Model, ":"); ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"model: %q carries a thinking level (:%s), which pi-rpc-socket cannot set on a live session; switch to %q, or register it with set-model (it applies at the next spawn or resume)", req.Model, level, base))
			return
		}
		c, err := d.openPi(thread, loc.Pane)
		if err != nil {
			writeError(w, http.StatusConflict, "model: "+err.Error())
			return
		}
		ref, err := c.SetModel(req.Model)
		c.Close()
		if err != nil {
			writeError(w, http.StatusConflict, "model: "+err.Error())
			return
		}
		d.recordModel(w, req, api.ModelSwitched, "pi switched this session to "+ref.String())
	default:
		writeError(w, http.StatusInternalServerError, "NOT IMPLEMENTED: live model switch for "+string(kind))
	}
}

// recordModel writes the registered model and answers with the outcome.
func (d *Daemon) recordModel(w http.ResponseWriter, req api.SetThreadModelRequest, outcome, detail string) {
	if err := d.store.SetThreadModel(req.ID, req.Model); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	d.respondModel(w, req.ID, outcome, detail)
}

func (d *Daemon) respondModel(w http.ResponseWriter, id, outcome, detail string) {
	th, err := d.store.GetThread(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.SetThreadModelResponse{Schema: api.SchemaVersion, Thread: th, Outcome: outcome, Detail: detail})
}

// restartClaudeOnModel replaces the claude process in the thread's marked pane
// with `claude --resume <live session> --model <m>` — the same command `thread
// resume` builds — and confirms the new agent is up.
func (d *Daemon) restartClaudeOnModel(th api.Thread, pane, model string) error {
	if th.AgentSessionID == "" {
		return errors.New("the thread has no captured claude session id to resume")
	}
	kind := agents.Claude
	// The stored id is the compaction chain's anchor; resume its live leaf.
	sessionID, err := agents.ResolveCurrentSession(kind, th.AgentSessionID, th.Cwd, agents.ResolveHomes(d.cfg.CodexHome))
	if err != nil {
		return fmt.Errorf("resolve the current claude session: %w", err)
	}
	env := d.spawnEnv(th.ID)
	if err := d.prepAgentEnv(kind, env, th.Cwd, th.ID); err != nil {
		return err
	}
	mode, err := d.resolveSpawnMode(kind, "")
	if err != nil {
		return err
	}
	extra, err := d.launchArgs(kind, th.ID)
	if err != nil {
		return err
	}
	cmd := agents.ResumeCommand(kind, sessionID, model, mode, extra)
	if err := d.tmux.RespawnPaneCmd(pane, th.Cwd, env, cmd); err != nil {
		return err
	}
	return d.confirmAgentLaunched(th.ID)
}

// switchCodexModel types `/model <m>` into the codex pane — through the typing
// guard, refusing rather than queueing when someone is typing — and waits for
// codex to confirm. Returns codex's confirmation line.
func (d *Daemon) switchCodexModel(th api.Thread, model string) (string, error) {
	resolve := func() (string, string, bool, error) {
		loc, found, err := d.tmux.FindPaneByThreadID(th.ID)
		if err != nil || !found {
			return "", "", false, err
		}
		return loc.Pane, loc.Session, true, nil
	}
	pane, _, _, err := resolve()
	if err != nil {
		return "", err
	}
	before, err := d.tmux.CapturePane(pane)
	if err != nil {
		return "", err
	}
	marker := "Model changed to " + model
	already := strings.Count(before, marker)
	preq := pasteRequest{thread: th, text: "/model " + model, guard: d.pasteGuardFor(nil, nil), sender: "switch-model", resolve: resolve}
	if _, err := d.pasteNow(preq); err != nil {
		var typing errTyping
		if errors.As(err, &typing) {
			return "", fmt.Errorf("%v — not switched (codex's composer is in use; try again when it is idle)", err)
		}
		return "", err
	}
	deadline := time.Now().Add(codexModelConfirmWithin)
	for time.Now().Before(deadline) {
		out, err := d.tmux.CapturePane(pane)
		if err == nil && strings.Count(out, marker) > already {
			for _, line := range strings.Split(out, "\n") {
				if i := strings.Index(line, marker); i >= 0 {
					return "codex: " + strings.TrimSpace(line[i:]), nil
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return "", fmt.Errorf("sent `/model %s` to codex but it never confirmed the switch within %s (a draft in its composer, or a model it rejected?) — check the pane; the registered model is unchanged", model, codexModelConfirmWithin)
}
